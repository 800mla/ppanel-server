package portal

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/perfect-panel/server/internal/model/order"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/redis/go-redis/v9"
)

const (
	portalVerificationTicketTTL     = 10 * time.Minute
	portalVerificationTicketLockTTL = 30 * time.Second
	portalPendingOrderTTL           = 20 * time.Minute
	portalCheckoutScene             = "portal_checkout"
)

type portalVerificationTicketPayload struct {
	Scene            string `json:"scene"`
	AuthType         string `json:"auth_type"`
	Identifier       string `json:"identifier"`
	AccountMode      string `json:"account_mode"`
	VerificationType string `json:"verification_type"`
	UserId           int64  `json:"user_id,omitempty"`
	AuthMethodId     int64  `json:"auth_method_id,omitempty"`
	IPHash           string `json:"ip_hash,omitempty"`
	UserAgentHash    string `json:"user_agent_hash,omitempty"`
	CreatedAt        int64  `json:"created_at"`
	ExpiresAt        int64  `json:"expires_at"`
}

func hashPortalBindingValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	return tool.Md5Encode(value, false)
}

func createPortalVerificationTicket(ctx context.Context, svcCtx *svc.ServiceContext, payload *portalVerificationTicketPayload) (string, error) {
	ticket := "pvt_" + strings.ReplaceAll(uuidx.NewUUID().String(), "-", "")
	if payload.CreatedAt == 0 {
		payload.CreatedAt = time.Now().UnixMilli()
	}
	if payload.ExpiresAt == 0 {
		payload.ExpiresAt = time.Now().Add(portalVerificationTicketTTL).UnixMilli()
	}

	value, err := json.Marshal(payload)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf(constant.PortalVerificationTicketCacheKey, ticket)
	if err = svcCtx.Redis.Set(ctx, key, string(value), portalVerificationTicketTTL).Err(); err != nil {
		return "", err
	}
	return ticket, nil
}

func acquirePortalVerificationTicketLock(ctx context.Context, svcCtx *svc.ServiceContext, ticket string) (func(), error) {
	lockKey := fmt.Sprintf(constant.PortalVerificationTicketLockKey, ticket)
	ok, err := svcCtx.Redis.SetNX(ctx, lockKey, "1", portalVerificationTicketLockTTL).Result()
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, newPortalVerificationTicketInvalidErr()
	}
	return func() {
		_ = svcCtx.Redis.Del(context.Background(), lockKey).Err()
	}, nil
}

func loadPortalVerificationTicket(ctx context.Context, svcCtx *svc.ServiceContext, ticket string) (*portalVerificationTicketPayload, error) {
	if strings.TrimSpace(ticket) == "" {
		return nil, newPortalVerificationRequiredErr()
	}

	key := fmt.Sprintf(constant.PortalVerificationTicketCacheKey, ticket)
	value, err := svcCtx.Redis.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, newPortalVerificationTicketInvalidErr()
		}
		return nil, err
	}

	var payload portalVerificationTicketPayload
	if err = json.Unmarshal([]byte(value), &payload); err != nil {
		return nil, err
	}
	if payload.ExpiresAt > 0 && time.Now().UnixMilli() > payload.ExpiresAt {
		return nil, newPortalVerificationTicketExpiredErr()
	}
	return &payload, nil
}

func consumePortalVerificationTicket(ctx context.Context, svcCtx *svc.ServiceContext, ticket string) error {
	if strings.TrimSpace(ticket) == "" {
		return nil
	}

	key := fmt.Sprintf(constant.PortalVerificationTicketCacheKey, ticket)
	usedKey := fmt.Sprintf(constant.PortalVerificationTicketUsedCacheKey, ticket)
	pipe := svcCtx.Redis.TxPipeline()
	pipe.Del(ctx, key)
	pipe.Set(ctx, usedKey, "1", time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}

func validatePortalVerificationTicketPayload(payload *portalVerificationTicketPayload, authType, identifier, ip, userAgent, expectedAccountMode string) error {
	if payload == nil {
		return newPortalVerificationRequiredErr()
	}
	if payload.Scene != portalCheckoutScene {
		return newPortalVerificationTicketInvalidErr()
	}
	if normalizePortalAuthType(authType) != normalizePortalAuthType(payload.AuthType) {
		return newPortalVerificationTicketInvalidErr()
	}
	if !strings.EqualFold(strings.TrimSpace(identifier), strings.TrimSpace(payload.Identifier)) {
		return newPortalVerificationTicketInvalidErr()
	}
	if expectedAccountMode != "" && payload.AccountMode != expectedAccountMode {
		return newPortalVerificationTicketInvalidErr()
	}
	if payload.IPHash != "" && payload.IPHash != hashPortalBindingValue(ip) {
		return newPortalVerificationTicketInvalidErr()
	}
	if payload.UserAgentHash != "" && payload.UserAgentHash != hashPortalBindingValue(userAgent) {
		return newPortalVerificationTicketInvalidErr()
	}
	if payload.ExpiresAt > 0 && time.Now().UnixMilli() > payload.ExpiresAt {
		return newPortalVerificationTicketExpiredErr()
	}
	return nil
}

func findPortalPendingOrder(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier string) (*order.Order, error) {
	key := formatPortalPendingOrderKey(authType, identifier)
	orderNo, err := svcCtx.Redis.Get(ctx, key).Result()
	if err != nil {
		if err == redis.Nil {
			return nil, nil
		}
		return nil, err
	}

	orderInfo, err := svcCtx.OrderModel.FindOneByOrderNo(ctx, orderNo)
	if err != nil {
		_ = svcCtx.Redis.Del(ctx, key).Err()
		return nil, nil
	}
	if orderInfo.Status != 1 {
		_ = svcCtx.Redis.Del(ctx, key).Err()
		return nil, nil
	}
	return orderInfo, nil
}

func storePortalPendingOrder(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier, orderNo string) error {
	key := formatPortalPendingOrderKey(authType, identifier)
	return svcCtx.Redis.Set(ctx, key, orderNo, portalPendingOrderTTL).Err()
}

func clearPortalPendingOrder(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier string) error {
	key := formatPortalPendingOrderKey(authType, identifier)
	return svcCtx.Redis.Del(ctx, key).Err()
}
