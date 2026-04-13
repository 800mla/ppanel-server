package emailLogic

import (
	"bytes"
	"context"
	"encoding/json"
	"text/template"
	"time"

	"github.com/perfect-panel/server/pkg/logger"

	"github.com/hibiken/asynq"
	"github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/email"
	"github.com/perfect-panel/server/queue/types"
)

type SendEmailLogic struct {
	svcCtx *svc.ServiceContext
}

func NewSendEmailLogic(svcCtx *svc.ServiceContext) *SendEmailLogic {
	return &SendEmailLogic{
		svcCtx: svcCtx,
	}
}
func (l *SendEmailLogic) ProcessTask(ctx context.Context, task *asynq.Task) error {
	var payload types.SendEmailPayload
	if err := json.Unmarshal(task.Payload(), &payload); err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] Unmarshal payload failed",
			logger.Field("error", err.Error()),
			logger.Field("payload", task.Payload()),
		)
		return nil
	}
	messageLog := log.Message{
		Source:                  payload.Source,
		TraceID:                 payload.TraceID,
		Platform:                l.svcCtx.Config.Email.Platform,
		To:                      payload.Email,
		Subject:                 payload.Subject,
		Content:                 payload.Content,
		Template:                payload.Type,
		Status:                  0,
		RequestTime:             time.Now().UnixMilli(),
		ProviderStatus:          "sending",
		ProviderResponseExcerpt: "worker started email send",
		UpdatedAt:               time.Now().UnixMilli(),
	}
	systemLogEntry, err := l.ensureSystemLogEntry(ctx, payload.LogID, &messageLog)
	if err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] ensure system log entry failed", logger.Field("error", err.Error()))
		return nil
	}
	sender, err := email.NewSender(l.svcCtx.Config.Email.Platform, l.svcCtx.Config.Email.PlatformConfig, l.svcCtx.Config.Site.SiteName)
	if err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] NewSender failed", logger.Field("error", err.Error()))
		l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "sender_init_failed", err.Error(), "email sender initialization failed", "", 0)
		return nil
	}
	var content string
	switch payload.Type {
	case types.EmailTypeVerify:
		tpl, _ := template.New("verify").Parse(l.svcCtx.Config.Email.VerifyEmailTemplate)
		var result bytes.Buffer

		payload.Content["Type"] = uint8(payload.Content["Type"].(float64))

		err = tpl.Execute(&result, payload.Content)
		if err != nil {
			logger.WithContext(ctx).Error("[SendEmailLogic] Execute template failed",
				logger.Field("error", err.Error()),
				logger.Field("data", payload.Content),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", err.Error(), "verify email template render failed", "", 0)
			return nil
		}
		content = result.String()
	case types.EmailTypeMaintenance:
		tpl, _ := template.New("maintenance").Parse(l.svcCtx.Config.Email.MaintenanceEmailTemplate)
		var result bytes.Buffer
		err = tpl.Execute(&result, payload.Content)
		if err != nil {
			logger.WithContext(ctx).Error("[SendEmailLogic] Execute template failed",
				logger.Field("error", err.Error()),
				logger.Field("template", l.svcCtx.Config.Email.MaintenanceEmailTemplate),
				logger.Field("data", payload.Content),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", err.Error(), "maintenance email template render failed", "", 0)
			return nil
		}
		content = result.String()
	case types.EmailTypeExpiration:
		tpl, _ := template.New("expiration").Parse(l.svcCtx.Config.Email.ExpirationEmailTemplate)
		var result bytes.Buffer
		err = tpl.Execute(&result, payload.Content)
		if err != nil {
			logger.WithContext(ctx).Error("[SendEmailLogic] Execute template failed",
				logger.Field("error", err.Error()),
				logger.Field("template", l.svcCtx.Config.Email.ExpirationEmailTemplate),
				logger.Field("data", payload.Content),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", err.Error(), "expiration email template render failed", "", 0)
			return nil
		}
		content = result.String()
	case types.EmailTypeTrafficExceed:
		tpl, _ := template.New("traffic_exceed").Parse(l.svcCtx.Config.Email.TrafficExceedEmailTemplate)
		var result bytes.Buffer
		err = tpl.Execute(&result, payload.Content)
		if err != nil {
			logger.WithContext(ctx).Error("[SendEmailLogic] Execute template failed",
				logger.Field("error", err.Error()),
				logger.Field("template", l.svcCtx.Config.Email.TrafficExceedEmailTemplate),
				logger.Field("data", payload.Content),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", err.Error(), "traffic exceed email template render failed", "", 0)
			return nil
		}
		content = result.String()
	case types.EmailTypeCustom:
		if payload.Content == nil {
			logger.WithContext(ctx).Error("[SendEmailLogic] Custom email content is empty",
				logger.Field("payload", payload),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", "custom email content is empty", "custom email content is empty", "", 0)
			return nil
		}
		if tpl, ok := payload.Content["content"].(string); !ok {
			logger.WithContext(ctx).Error("[SendEmailLogic] Custom email content is not a string",
				logger.Field("payload", payload),
			)
			l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "template_failed", "custom email content is not a string", "custom email content is not a string", "", 0)
			return nil
		} else {
			content = tpl
		}
	default:
		logger.WithContext(ctx).Error("[SendEmailLogic] Unsupported email type",
			logger.Field("type", payload.Type),
			logger.Field("payload", payload),
		)
		l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "unsupported_type", "unsupported email type", "unsupported email type", "", 0)
		return nil
	}

	providerMessageID, providerExcerpt, err := sender.Send([]string{payload.Email}, payload.Subject, content, map[string]string{
		"X-Bingka-Mail-Trace-ID":  payload.TraceID,
		"Resend-Idempotency-Key": payload.TraceID,
	})
	if err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] Send email failed", logger.Field("error", err.Error()))
		l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 2, "smtp_failed", err.Error(), "sender.Send returned error", "", 0)
		return nil
	}
	if providerExcerpt == "" {
		providerExcerpt = "sender.Send returned success"
	}
	l.updateSystemLogStatus(ctx, systemLogEntry, &messageLog, 1, "smtp_accepted", "", providerExcerpt, providerMessageID, 0)
	return nil
}

func (l *SendEmailLogic) ensureSystemLogEntry(ctx context.Context, logID int64, messageLog *log.Message) (*log.SystemLog, error) {
	if logID != 0 {
		existing, err := l.svcCtx.LogModel.FindOne(ctx, logID)
		if err == nil {
			return existing, nil
		}
	}

	content, err := messageLog.Marshal()
	if err != nil {
		return nil, err
	}
	entry := &log.SystemLog{
		Type:     log.TypeEmailMessage.Uint8(),
		Date:     time.Now().Format("2006-01-02"),
		ObjectID: 0,
		Content:  string(content),
	}
	if err = l.svcCtx.LogModel.Insert(ctx, entry); err != nil {
		return nil, err
	}
	return entry, nil
}

func (l *SendEmailLogic) updateSystemLogStatus(ctx context.Context, entry *log.SystemLog, messageLog *log.Message, status uint8, providerStatus, errorMessage, excerpt, providerMessageID string, providerEventTime int64) {
	if entry == nil || messageLog == nil {
		return
	}
	messageLog.Status = status
	messageLog.ProviderStatus = providerStatus
	messageLog.ErrorMessage = errorMessage
	messageLog.ProviderMessageID = providerMessageID
	messageLog.ProviderResponseExcerpt = excerpt
	if providerEventTime != 0 {
		messageLog.ProviderEventTime = providerEventTime
	}
	messageLog.UpdatedAt = time.Now().UnixMilli()

	content, err := messageLog.Marshal()
	if err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] Marshal message log failed",
			logger.Field("error", err.Error()),
		)
		return
	}
	entry.Content = string(content)
	if err = l.svcCtx.LogModel.Update(ctx, entry); err != nil {
		logger.WithContext(ctx).Error("[SendEmailLogic] Update email log failed",
			logger.Field("error", err.Error()),
			logger.Field("log_id", entry.Id),
		)
	}
}
