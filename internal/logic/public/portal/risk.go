package portal

import (
	"context"
	"fmt"
	"strings"

	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/captcha"
	"github.com/perfect-panel/server/pkg/limit"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
)

const (
	portalSendCodeIPLimitPeriodSeconds = 600
	portalSendCodeIPLimitQuota         = 3
	portalPurchaseIPLimitPeriodSeconds = 600
	portalPurchaseIPLimitQuota         = 5
)

func newPortalVerificationTicketInvalidErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PortalVerificationTicketInvalid, xerr.MapErrMsg(xerr.PortalVerificationTicketInvalid)),
		"portal verification ticket invalid",
	)
}

func newPortalVerificationRequiredErr() error {
	return newPortalCheckoutVerificationRequiredErr()
}

func newPortalVerificationTicketExpiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PortalVerificationTicketExpired, xerr.MapErrMsg(xerr.PortalVerificationTicketExpired)),
		"portal verification ticket expired",
	)
}

func newPortalPendingOrderExistsErr(orderNo string) error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PortalPendingOrderExists, xerr.MapErrMsg(xerr.PortalPendingOrderExists)),
		"portal pending order already exists: %s",
		orderNo,
	)
}

func newPortalTempEmailNotAllowedErr(identifier string) error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PortalTempEmailNotAllowed, xerr.MapErrMsg(xerr.PortalTempEmailNotAllowed)),
		"portal email domain not allowed: %s",
		identifier,
	)
}

func newPortalVerificationTicketNotRequiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.InvalidParams, "Portal verification ticket is not required for verified account"),
		"portal verification ticket is not required for verified account",
	)
}

func verifyPortalTurnstile(ctx context.Context, svcCtx *svc.ServiceContext, token, ip string) error {
	verifyCfg, err := svcCtx.SystemModel.GetVerifyConfig(ctx)
	if err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "GetVerifyConfig error: %v", err.Error())
	}

	var cfg struct {
		CaptchaType     string `json:"captcha_type"`
		TurnstileSecret string `json:"turnstile_secret"`
	}
	tool.SystemConfigSliceReflectToStruct(verifyCfg, &cfg)

	if cfg.CaptchaType != string(captcha.CaptchaTypeTurnstile) || cfg.TurnstileSecret == "" {
		return nil
	}

	if strings.TrimSpace(token) == "" {
		return errors.Wrapf(xerr.NewErrCodeMsg(xerr.VerifyCodeError, "Turnstile verification required"), "turnstile token required")
	}

	return captcha.VerifyCaptcha(ctx, svcCtx.Redis, cfg.CaptchaType, cfg.TurnstileSecret, captcha.VerifyInput{
		CfToken: token,
		IP:      ip,
	})
}

func takePortalSendCodeIPLimit(ctx context.Context, svcCtx *svc.ServiceContext, ip string) error {
	return takePortalIPLimit(ctx, svcCtx, "portal:send_code:ip:", ip, portalSendCodeIPLimitPeriodSeconds, portalSendCodeIPLimitQuota, "portal send code too many requests")
}

func takePortalPurchaseIPLimit(ctx context.Context, svcCtx *svc.ServiceContext, ip string) error {
	return takePortalIPLimit(ctx, svcCtx, "portal:purchase:ip:", ip, portalPurchaseIPLimitPeriodSeconds, portalPurchaseIPLimitQuota, "portal purchase too many requests")
}

func takePortalIPLimit(ctx context.Context, svcCtx *svc.ServiceContext, keyPrefix, ip string, periodSeconds, quota int, message string) error {
	if strings.TrimSpace(ip) == "" {
		return nil
	}

	limiter := limit.NewPeriodLimit(periodSeconds, quota, svcCtx.Redis, keyPrefix)
	permit, err := limiter.TakeCtx(ctx, ip)
	if err != nil {
		return errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "Failed to take limit")
	}
	if limiter.ParsePermitState(permit) {
		return nil
	}

	return errors.Wrapf(xerr.NewErrCode(xerr.TooManyRequests), message)
}

func validatePortalEmailDomain(svcCtx *svc.ServiceContext, identifier string) error {
	if !svcCtx.Config.Email.EnableDomainSuffix {
		return nil
	}

	domainSuffixList := strings.TrimSpace(svcCtx.Config.Email.DomainSuffixList)
	if domainSuffixList == "" {
		return nil
	}

	idx := strings.LastIndex(identifier, "@")
	if idx <= 0 || idx == len(identifier)-1 {
		return newPortalTempEmailNotAllowedErr(identifier)
	}

	domain := strings.ToLower(strings.TrimSpace(identifier[idx+1:]))
	allowed := parsePortalDomainSuffixList(domainSuffixList)
	for _, suffix := range allowed {
		if domain == suffix || strings.HasSuffix(domain, "."+suffix) {
			return nil
		}
	}

	return newPortalTempEmailNotAllowedErr(identifier)
}

func parsePortalDomainSuffixList(raw string) []string {
	replacer := strings.NewReplacer("\n", ",", "\r", ",", ";", ",", " ", ",")
	raw = replacer.Replace(raw)
	parts := strings.Split(raw, ",")
	result := make([]string, 0, len(parts))
	seen := make(map[string]struct{}, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(strings.ToLower(part))
		part = strings.TrimPrefix(part, "@")
		if part == "" {
			continue
		}
		if _, ok := seen[part]; ok {
			continue
		}
		seen[part] = struct{}{}
		result = append(result, part)
	}
	return result
}

func formatPortalPendingOrderKey(authType, identifier string) string {
	return fmt.Sprintf("portal:pending_order:%s:%s", normalizePortalAuthType(authType), strings.ToLower(strings.TrimSpace(identifier)))
}
