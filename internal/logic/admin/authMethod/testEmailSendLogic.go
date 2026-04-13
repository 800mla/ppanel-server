package authMethod

import (
	"context"
	"fmt"
	"time"

	systemLog "github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/email"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
)

type TestEmailSendLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// Test email send
func NewTestEmailSendLogic(ctx context.Context, svcCtx *svc.ServiceContext) *TestEmailSendLogic {
	return &TestEmailSendLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *TestEmailSendLogic) TestEmailSend(req *types.TestEmailSendRequest) error {
	traceID := "mail_" + uuidx.NewUUID().String()
	messageLog := &systemLog.Message{
		Source:                  "test_email_send",
		TraceID:                 traceID,
		To:                      req.Email,
		Subject:                 "Test Email Send",
		Content:                 map[string]interface{}{"content": "this a test email send by ppanel"},
		Platform:                l.svcCtx.Config.Email.Platform,
		Template:                "custom",
		Status:                  0,
		RequestTime:             time.Now().UnixMilli(),
		ProviderStatus:          "sending",
		ProviderResponseExcerpt: "admin test email send requested",
		UpdatedAt:               time.Now().UnixMilli(),
	}
	content, _ := messageLog.Marshal()
	systemLogEntry := &systemLog.SystemLog{
		Type:     systemLog.TypeEmailMessage.Uint8(),
		Date:     time.Now().Format("2006-01-02"),
		ObjectID: 0,
		Content:  string(content),
	}
	if err := l.svcCtx.LogModel.Insert(l.ctx, systemLogEntry); err != nil {
		l.Errorw("insert test email log err", logger.Field("error", err.Error()))
		return errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "insert test email log err: %v", err.Error())
	}

	client, err := email.NewSender(l.svcCtx.Config.Email.Platform, l.svcCtx.Config.Email.PlatformConfig, l.svcCtx.Config.Site.SiteName)
	if err != nil {
		l.Errorw("new email sender err", logger.Field("error", err.Error()))
		messageLog.Status = 2
		messageLog.ErrorMessage = err.Error()
		messageLog.ProviderStatus = "sender_init_failed"
		messageLog.ProviderResponseExcerpt = "email.NewSender failed"
		messageLog.UpdatedAt = time.Now().UnixMilli()
		if payload, marshalErr := messageLog.Marshal(); marshalErr == nil {
			systemLogEntry.Content = string(payload)
			_ = l.svcCtx.LogModel.Update(l.ctx, systemLogEntry)
		}
		return errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "new email sender err: %v", err.Error())
	}
	providerMessageID, providerResponseExcerpt, err := client.Send([]string{req.Email}, "Test Email Send", "this a test email send by ppanel", map[string]string{
		"X-Bingka-Mail-Trace-ID":  traceID,
		"Resend-Idempotency-Key": traceID,
	})
	if err != nil {
		messageLog.Status = 2
		messageLog.ErrorMessage = err.Error()
		messageLog.ProviderStatus = "smtp_failed"
		messageLog.ProviderResponseExcerpt = "sender.Send returned error"
		messageLog.UpdatedAt = time.Now().UnixMilli()
		if payload, marshalErr := messageLog.Marshal(); marshalErr == nil {
			systemLogEntry.Content = string(payload)
			_ = l.svcCtx.LogModel.Update(l.ctx, systemLogEntry)
		}
		return errors.Wrapf(xerr.NewErrCodeMsg(500, fmt.Sprintf("send email err: %v", err.Error())), "send email err: %v", err.Error())
	}

	messageLog.Status = 1
	messageLog.ProviderStatus = "smtp_accepted"
	messageLog.ProviderMessageID = providerMessageID
	if providerResponseExcerpt != "" {
		messageLog.ProviderResponseExcerpt = providerResponseExcerpt
	} else {
		messageLog.ProviderResponseExcerpt = "sender.Send returned success"
	}
	messageLog.UpdatedAt = time.Now().UnixMilli()
	if payload, marshalErr := messageLog.Marshal(); marshalErr == nil {
		systemLogEntry.Content = string(payload)
		_ = l.svcCtx.LogModel.Update(l.ctx, systemLogEntry)
	}
	return nil
}
