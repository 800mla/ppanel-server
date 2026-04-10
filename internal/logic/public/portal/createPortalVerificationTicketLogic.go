package portal

import (
	"context"
	"time"

	commonLogic "github.com/perfect-panel/server/internal/logic/common"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/authmethod"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
)

type CreatePortalVerificationTicketLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

func NewCreatePortalVerificationTicketLogic(ctx context.Context, svcCtx *svc.ServiceContext) *CreatePortalVerificationTicketLogic {
	return &CreatePortalVerificationTicketLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *CreatePortalVerificationTicketLogic) CreatePortalVerificationTicket(req *types.PortalVerificationTicketRequest) (*types.PortalVerificationTicketResponse, error) {
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
		return nil, newPortalVerificationTicketNotRequiredErr()
	}

	if err := verifyPortalTurnstile(l.ctx, l.svcCtx, req.TurnstileToken, req.IP); err != nil {
		return nil, err
	}
	if requirement.AccountMode == portalAccountModeNewEmail {
		if err := validatePortalEmailDomain(l.svcCtx, req.Identifier); err != nil {
			return nil, err
		}
	}

	checkLogic := commonLogic.NewCheckVerificationCodeLogic(l.ctx, l.svcCtx)
	checkResp, err := checkLogic.CheckVerificationCode(&types.CheckVerificationCodeRequest{
		Method:  authmethod.Email,
		Account: req.Identifier,
		Code:    req.Code,
		Type:    portalVerificationTypeToConstant(requirement.VerificationType),
	})
	if err != nil {
		return nil, err
	}
	if !checkResp.Status {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.VerifyCodeError), "code error")
	}

	payload := &portalVerificationTicketPayload{
		Scene:            portalCheckoutScene,
		AuthType:         req.AuthType,
		Identifier:       req.Identifier,
		AccountMode:      requirement.AccountMode,
		VerificationType: requirement.VerificationType,
		IPHash:           hashPortalBindingValue(req.IP),
		UserAgentHash:    hashPortalBindingValue(req.UserAgent),
		CreatedAt:        time.Now().UnixMilli(),
		ExpiresAt:        time.Now().Add(portalVerificationTicketTTL).UnixMilli(),
	}
	if state.AuthMethod != nil {
		payload.AuthMethodId = state.AuthMethod.Id
		payload.UserId = state.AuthMethod.UserId
	}

	ticket, err := createPortalVerificationTicket(l.ctx, l.svcCtx, payload)
	if err != nil {
		return nil, err
	}

	return &types.PortalVerificationTicketResponse{
		Verified:                 true,
		AccountMode:              requirement.AccountMode,
		VerificationType:         requirement.VerificationType,
		PortalVerificationTicket: ticket,
		ExpiresAt:                payload.ExpiresAt,
		RequirePassword:          true,
	}, nil
}
