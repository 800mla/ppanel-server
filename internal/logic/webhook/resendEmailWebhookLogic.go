package webhook

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	systemLog "github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
)

const resendWebhookTimeSkew = 5 * time.Minute

type ResendEmailWebhookLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

type resendWebhookEvent struct {
	Type      string `json:"type"`
	CreatedAt string `json:"created_at"`
	Data      struct {
		EmailID   string   `json:"email_id"`
		MessageID string   `json:"message_id"`
		To        []string `json:"to"`
		Subject   string   `json:"subject"`
		From      string   `json:"from"`
		CreatedAt string   `json:"created_at"`
	} `json:"data"`
}

func NewResendEmailWebhookLogic(ctx context.Context, svcCtx *svc.ServiceContext) *ResendEmailWebhookLogic {
	return &ResendEmailWebhookLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *ResendEmailWebhookLogic) Handle(r *http.Request) error {
	if strings.TrimSpace(l.svcCtx.Config.Email.WebhookSecret) == "" {
		return errors.Wrapf(xerr.NewErrCodeMsg(xerr.InvalidParams, "email webhook secret is not configured"), "email webhook secret is not configured")
	}

	body, err := io.ReadAll(r.Body)
	if err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "read webhook body failed: %v", err.Error())
	}
	if err = l.verifySignature(r, body); err != nil {
		return err
	}

	var event resendWebhookEvent
	if err = json.Unmarshal(body, &event); err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.InvalidParams), "invalid webhook payload: %v", err.Error())
	}

	if len(event.Data.To) == 0 || event.Data.Subject == "" {
		return nil
	}

	entry, messageLog, err := l.findMatchingEmailLog(event)
	if err != nil || entry == nil || messageLog == nil {
		return err
	}

	l.applyProviderEvent(messageLog, event)
	content, marshalErr := messageLog.Marshal()
	if marshalErr != nil {
		return marshalErr
	}
	entry.Content = string(content)
	return l.svcCtx.LogModel.Update(l.ctx, entry)
}

func (l *ResendEmailWebhookLogic) verifySignature(r *http.Request, body []byte) error {
	id := firstNonEmpty(r.Header.Get("svix-id"), r.Header.Get("webhook-id"))
	timestamp := firstNonEmpty(r.Header.Get("svix-timestamp"), r.Header.Get("webhook-timestamp"))
	signature := firstNonEmpty(r.Header.Get("svix-signature"), r.Header.Get("webhook-signature"))
	if id == "" || timestamp == "" || signature == "" {
		return errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "missing webhook signature headers")
	}

	sec, err := parseUnixTimestamp(timestamp)
	if err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "invalid webhook timestamp")
	}
	if diff := time.Since(time.Unix(sec, 0)); diff > resendWebhookTimeSkew || diff < -resendWebhookTimeSkew {
		return errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "webhook timestamp expired")
	}

	signedContent := id + "." + timestamp + "." + string(body)
	secret := strings.TrimPrefix(strings.TrimSpace(l.svcCtx.Config.Email.WebhookSecret), "whsec_")
	key, err := base64.StdEncoding.DecodeString(secret)
	if err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "invalid webhook secret")
	}
	mac := hmac.New(sha256.New, key)
	_, _ = mac.Write([]byte(signedContent))
	expected := base64.StdEncoding.EncodeToString(mac.Sum(nil))

	for _, part := range strings.Fields(strings.ReplaceAll(signature, ",", " ")) {
		if strings.HasPrefix(part, "v1=") && hmac.Equal([]byte(strings.TrimPrefix(part, "v1=")), []byte(expected)) {
			return nil
		}
		if strings.HasPrefix(part, "v1") && strings.Contains(part, ".") {
			continue
		}
	}
	for _, part := range strings.Split(signature, " ") {
		if strings.HasPrefix(part, "v1,") && hmac.Equal([]byte(strings.TrimPrefix(part, "v1,")), []byte(expected)) {
			return nil
		}
	}
	return errors.Wrapf(xerr.NewErrCode(xerr.InvalidAccess), "invalid webhook signature")
}

func (l *ResendEmailWebhookLogic) findMatchingEmailLog(event resendWebhookEvent) (*systemLog.SystemLog, *systemLog.Message, error) {
	recipient := event.Data.To[0]
	subject := event.Data.Subject
	since := time.Now().Add(-24 * time.Hour)
	var rows []*systemLog.SystemLog
	err := l.svcCtx.DB.WithContext(l.ctx).
		Model(&systemLog.SystemLog{}).
		Where("type = ? AND created_at >= ? AND content LIKE ? AND content LIKE ?", systemLog.TypeEmailMessage.Uint8(), since, "%\"to\":\""+recipient+"\"%", "%\"subject\":\""+subject+"\"%").
		Order("id DESC").
		Limit(20).
		Find(&rows).Error
	if err != nil {
		return nil, nil, err
	}
	if len(rows) == 0 {
		return nil, nil, nil
	}

	eventTime := parseEventTime(event)
	var bestEntry *systemLog.SystemLog
	var bestMessage *systemLog.Message
	bestDistance := int64(1<<62 - 1)
	for _, row := range rows {
		var msg systemLog.Message
		if err := msg.Unmarshal([]byte(row.Content)); err != nil {
			continue
		}
		if msg.To != recipient || msg.Subject != subject {
			continue
		}
		if msg.ProviderMessageID != "" && event.Data.EmailID != "" && msg.ProviderMessageID == event.Data.EmailID {
			return row, &msg, nil
		}
		refTime := row.CreatedAt.UnixMilli()
		if msg.RequestTime != 0 {
			refTime = msg.RequestTime
		}
		distance := absInt64(eventTime - refTime)
		if distance < bestDistance {
			bestDistance = distance
			bestEntry = row
			bestMessage = &msg
		}
	}
	return bestEntry, bestMessage, nil
}

func (l *ResendEmailWebhookLogic) applyProviderEvent(msg *systemLog.Message, event resendWebhookEvent) {
	if msg == nil {
		return
	}
	msg.ProviderStatus = event.Type
	msg.ProviderEventTime = parseEventTime(event)
	if event.Data.EmailID != "" {
		msg.ProviderMessageID = event.Data.EmailID
	} else if event.Data.MessageID != "" {
		msg.ProviderMessageID = event.Data.MessageID
	}
	msg.ProviderResponseExcerpt = "provider webhook event: " + event.Type
	msg.UpdatedAt = time.Now().UnixMilli()

	switch event.Type {
	case "email.sent", "email.delivered", "email.delivery_delayed":
		msg.Status = 1
	case "email.bounced", "email.complained", "email.failed", "email.suppressed":
		msg.Status = 2
	}
}

func parseEventTime(event resendWebhookEvent) int64 {
	for _, raw := range []string{event.CreatedAt, event.Data.CreatedAt} {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if t, err := time.Parse(time.RFC3339, raw); err == nil {
			return t.UnixMilli()
		}
	}
	return time.Now().UnixMilli()
}

func parseUnixTimestamp(raw string) (int64, error) {
	return strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value != "" {
			return value
		}
	}
	return ""
}

func absInt64(v int64) int64 {
	if v < 0 {
		return -v
	}
	return v
}
