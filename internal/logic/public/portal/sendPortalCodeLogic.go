package portal

import (
	"context"

	commonLogic "github.com/perfect-panel/server/internal/logic/common"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
)

const (
	portalVerificationCodeResendAfterSeconds = 60
	portalVerificationCodeExpiresInSeconds   = 300
)

type SendPortalCodeLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewSendPortalCodeLogic(ctx context.Context, svcCtx *svc.ServiceContext) *SendPortalCodeLogic {
	return &SendPortalCodeLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *SendPortalCodeLogic) SendPortalCode(req *types.PortalSendCodeRequest) (*types.PortalSendCodeResponse, error) {
	req.AuthType = normalizePortalAuthType(req.AuthType)
	if _, err := requirePortalPurchaseIdentity(req.AuthType, req.Identifier); err != nil {
		return nil, err
	}

	state, err := resolvePortalPreviewState(l.ctx, l.svcCtx, req.AuthType, req.Identifier, "")
	if err != nil {
		return nil, err
	}

	requirement := resolvePortalVerificationRequirement(state)
	if requirement == nil {
		return &types.PortalSendCodeResponse{
			Status:           false,
			AccountMode:      state.AccountMode,
			VerificationType: state.VerificationType,
			NextAction:       state.NextAction,
			ResendAfter:      portalVerificationCodeResendAfterSeconds,
			ExpiresIn:        portalVerificationCodeExpiresInSeconds,
		}, nil
	}

	if err := verifyPortalTurnstile(l.ctx, l.svcCtx, req.TurnstileToken, req.IP); err != nil {
		return nil, err
	}
	if err := takePortalSendCodeIPLimit(l.ctx, l.svcCtx, req.IP); err != nil {
		return nil, err
	}
	if requirement.AccountMode == portalAccountModeNewEmail {
		if err := validatePortalEmailDomain(l.svcCtx, req.Identifier); err != nil {
			return nil, err
		}
	}

	sendCodeLogic := commonLogic.NewSendEmailCodeLogic(l.ctx, l.svcCtx)
	if _, err = sendCodeLogic.SendEmailCode(&types.SendCodeRequest{
		Email: req.Identifier,
		Type:  portalVerificationTypeToConstant(requirement.VerificationType),
	}); err != nil {
		return nil, err
	}

	return &types.PortalSendCodeResponse{
		Status:           true,
		AccountMode:      requirement.AccountMode,
		VerificationType: requirement.VerificationType,
		NextAction:       portalNextActionVerifyEmail,
		ResendAfter:      portalVerificationCodeResendAfterSeconds,
		ExpiresIn:        portalVerificationCodeExpiresInSeconds,
	}, nil
}
