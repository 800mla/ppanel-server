package common

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/config"
	systemLog "github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/limit"
	"github.com/perfect-panel/server/pkg/random"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/pkg/errors"
	"gorm.io/gorm"

	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/xerr"
	queue "github.com/perfect-panel/server/queue/types"
)

type SendEmailCodeLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

const (
	IntervalTime = 60
)

type VerifyTemplate struct {
	Type     uint8
	SiteLogo string
	SiteName string
	Expire   uint8
	Code     string
}
type CacheKeyPayload struct {
	Code   string `json:"code"`
	LastAt int64  `json:"lastAt"`
}

// NewSendEmailCodeLogic Get verification code
func NewSendEmailCodeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SendEmailCodeLogic {
	return &SendEmailCodeLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *SendEmailCodeLogic) SendEmailCode(req *types.SendCodeRequest) (resp *types.SendCodeResponse, err error) {
	// Check if there is Redis in the code
	cacheKey := fmt.Sprintf("%s:%s:%s", config.AuthCodeCacheKey, constant.ParseVerifyType(req.Type), req.Email)
	// Check if the limit is exceeded of current request
	limiter := limit.NewPeriodLimit(60, 1, l.svcCtx.Redis, fmt.Sprintf("%s:%s:%s", config.SendIntervalKeyPrefix, "email", constant.ParseVerifyType(req.Type)))
	permit, err := limiter.Take(req.Email)
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to take limit")
	}
	if !limiter.ParsePermitState(permit) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.TooManyRequests), "send email too many requests")
	}
	// Check if the limit is exceeded of today
	permit, err = l.svcCtx.AuthLimiter.Take(fmt.Sprintf("%s:%s:%s", "email", constant.ParseVerifyType(req.Type), req.Email))
	if err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to take limit")
	}
	if !l.svcCtx.AuthLimiter.ParsePermitState(permit) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.TodaySendCountExceedsLimit), "send email too many requests")
	}
	m, err := l.svcCtx.UserModel.FindUserAuthMethodByOpenID(l.ctx, "email", req.Email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "FindUserAuthMethodByOpenID error")
	}
	if constant.ParseVerifyType(req.Type) == constant.Register && m.Id > 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserExist), "mobile already bind")
	} else if constant.ParseVerifyType(req.Type) == constant.Security && m.Id == 0 {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserNotExist), "mobile not bind")
	}

	var payload CacheKeyPayload
	var taskPayload queue.SendEmailPayload
	// Generate verification code
	code := random.Key(6, 0)
	taskPayload.Type = queue.EmailTypeVerify
	taskPayload.Email = req.Email
	taskPayload.Subject = "Verification code"
	taskPayload.Content = map[string]interface{}{
		"Type":     req.Type,
		"SiteLogo": l.svcCtx.Config.Site.SiteLogo,
		"SiteName": l.svcCtx.Config.Site.SiteName,
		"Expire":   5,
		"Code":     code,
	}
	// Save to Redis
	payload = CacheKeyPayload{
		Code:   code,
		LastAt: time.Now().Unix(),
	}
	// Marshal the payload
	val, _ := json.Marshal(payload)
	if err = l.svcCtx.Redis.Set(l.ctx, cacheKey, string(val), time.Second*IntervalTime*5).Err(); err != nil {
		l.Errorw("[SendEmailCode]: Redis Error", logger.Field("error", err.Error()), logger.Field("cacheKey", cacheKey))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to set verification code")
	}

	// Marshal the task payload
	payloadBuy, err := json.Marshal(taskPayload)
	if err != nil {
		l.Errorw("[SendEmailCode]: Marshal Error", logger.Field("error", err.Error()))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to marshal task payload")
	}

	traceID := "mail_" + uuidx.NewUUID().String()

	messageLog := &systemLog.Message{
		Source:                  "send_code",
		TraceID:                 traceID,
		To:                      req.Email,
		Subject:                 taskPayload.Subject,
		Content:                 taskPayload.Content,
		Platform:                l.svcCtx.Config.Email.Platform,
		Template:                taskPayload.Type,
		Status:                  0,
		RequestTime:             time.Now().UnixMilli(),
		ProviderStatus:          "queued",
		ProviderResponseExcerpt: "queued for async email sending",
		UpdatedAt:               time.Now().UnixMilli(),
	}
	messageContent, _ := messageLog.Marshal()
	systemLogEntry := &systemLog.SystemLog{
		Type:     systemLog.TypeEmailMessage.Uint8(),
		Date:     time.Now().Format("2006-01-02"),
		ObjectID: 0,
		Content:  string(messageContent),
	}
	if err = l.svcCtx.LogModel.Insert(l.ctx, systemLogEntry); err != nil {
		l.Errorw("[SendEmailCode]: Insert pending email log failed", logger.Field("error", err.Error()))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to insert pending email log")
	}
	taskPayload.Source = "send_code"
	taskPayload.LogID = systemLogEntry.Id
	taskPayload.TraceID = traceID
	payloadBuy, err = json.Marshal(taskPayload)
	if err != nil {
		l.Errorw("[SendEmailCode]: Marshal Error", logger.Field("error", err.Error()))
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to marshal task payload")
	}
	// Create a queue task
	task := asynq.NewTask(queue.ForthwithSendEmail, payloadBuy, asynq.MaxRetry(3))
	// Enqueue the task
	taskInfo, err := l.svcCtx.Queue.Enqueue(task)
	if err != nil {
		l.Errorw("[SendEmailCode]: Enqueue Error", logger.Field("error", err.Error()), logger.Field("payload", string(payloadBuy)))
		messageLog.Status = 2
		messageLog.ErrorMessage = err.Error()
		messageLog.ProviderStatus = "queue_failed"
		messageLog.ProviderResponseExcerpt = "failed to enqueue async email task"
		messageLog.UpdatedAt = time.Now().UnixMilli()
		if content, marshalErr := messageLog.Marshal(); marshalErr == nil {
			systemLogEntry.Content = string(content)
			_ = l.svcCtx.LogModel.Update(l.ctx, systemLogEntry)
		}
		return nil, errors.Wrap(xerr.NewErrCode(xerr.ERROR), "Failed to enqueue task")
	}
	l.Infow("[SendEmailCode]: Enqueue Success", logger.Field("taskID", taskInfo.ID), logger.Field("payload", string(payloadBuy)))
	if l.svcCtx.Config.Model == constant.DevMode {
		return &types.SendCodeResponse{
			Code:   payload.Code,
			Status: true,
		}, nil
	} else {
		return &types.SendCodeResponse{
			Status: true,
		}, nil
	}
}
