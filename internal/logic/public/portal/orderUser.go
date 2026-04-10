package portal

import (
	"context"
	"strings"

	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const (
	portalCheckoutPasswordRequiredMsg          = "Password required for portal checkout"
	portalCheckoutEmailVerificationRequiredMsg = "Email verification required before portal checkout"
	portalCheckoutEmailOnlyMsg                 = "Portal checkout only supports email"
)

const (
	portalAuthTypeEmail = "email"
)

const (
	portalAccountModeNewEmail          = "new_email"
	portalAccountModeExistingVerified  = "existing_verified"
	portalAccountModeExistingUnverified = "existing_unverified"
)

const (
	portalNextActionNone         = "none"
	portalNextActionInputPassword = "input_password"
	portalNextActionVerifyEmail   = "verify_email"
)

const (
	portalVerificationTypeNone     = ""
	portalVerificationTypeRegister = "register"
	portalVerificationTypeSecurity = "security"
)

type portalPreviewState struct {
	User                *user.User
	AuthMethod          *user.AuthMethods
	AccountMode         string
	CanPurchase         bool
	PurchaseBlockReason string
	NextAction          string
	VerificationType    string
	RequirePassword     bool
}

func normalizePortalAuthType(authType string) string {
	authType = strings.TrimSpace(strings.ToLower(authType))
	if authType == "" {
		return portalAuthTypeEmail
	}
	return authType
}

func resolvePortalPreviewState(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier, password string) (*portalPreviewState, error) {
	authType = normalizePortalAuthType(authType)
	state := &portalPreviewState{
		NextAction:          portalNextActionNone,
		VerificationType:    portalVerificationTypeNone,
		RequirePassword:     strings.TrimSpace(identifier) != "",
		PurchaseBlockReason: "",
	}
	if identifier == "" {
		return state, nil
	}

	if authType != portalAuthTypeEmail {
		state.PurchaseBlockReason = portalCheckoutEmailOnlyMsg
		return state, nil
	}

	authMethod, err := svcCtx.UserModel.FindUserAuthMethodByOpenID(ctx, authType, identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state.AccountMode = portalAccountModeNewEmail
			state.NextAction = portalNextActionVerifyEmail
			state.VerificationType = portalVerificationTypeRegister
			state.PurchaseBlockReason = portalCheckoutEmailVerificationRequiredMsg
			return state, nil
		}
		return nil, err
	}
	state.AuthMethod = authMethod

	if authType == portalAuthTypeEmail && !authMethod.Verified {
		state.AccountMode = portalAccountModeExistingUnverified
		state.NextAction = portalNextActionVerifyEmail
		state.VerificationType = portalVerificationTypeSecurity
		state.PurchaseBlockReason = portalCheckoutEmailVerificationRequiredMsg
		return state, nil
	}

	userInfo, err := svcCtx.UserModel.FindOne(ctx, authMethod.UserId)
	if err != nil {
		return nil, err
	}
	if password == "" {
		state.AccountMode = portalAccountModeExistingVerified
		state.NextAction = portalNextActionInputPassword
		state.PurchaseBlockReason = portalCheckoutPasswordRequiredMsg
		return state, nil
	}
	if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, password, userInfo.Password) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}

	state.User = userInfo
	state.AccountMode = portalAccountModeExistingVerified
	state.CanPurchase = true
	return state, nil
}

func newPortalCheckoutVerificationRequiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PortalVerificationRequired, portalCheckoutEmailVerificationRequiredMsg),
		"email verification required before portal checkout",
	)
}

func newPortalCheckoutPasswordRequiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.PasswordIsEmpty, portalCheckoutPasswordRequiredMsg),
		"password required for portal checkout",
	)
}

func newPortalCheckoutIdentityRequiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.InvalidParams, "auth_type and identifier are required for portal checkout"),
		"auth_type and identifier are required for portal checkout",
	)
}

func newPortalCheckoutEmailOnlyErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.InvalidParams, portalCheckoutEmailOnlyMsg),
		"portal checkout only supports email",
	)
}

func requirePortalPurchaseIdentity(authType, identifier string) (string, error) {
	authType = normalizePortalAuthType(authType)
	if authType == "" || identifier == "" {
		return "", newPortalCheckoutIdentityRequiredErr()
	}
	if authType != portalAuthTypeEmail {
		return "", newPortalCheckoutEmailOnlyErr()
	}
	return authType, nil
}

func resolvePortalAccountState(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, authType, identifier string) (*portalPreviewState, error) {
	authType, err := requirePortalPurchaseIdentity(authType, identifier)
	if err != nil {
		return nil, err
	}

	state := &portalPreviewState{
		AccountMode:      portalAccountModeNewEmail,
		NextAction:       portalNextActionVerifyEmail,
		VerificationType: portalVerificationTypeRegister,
		RequirePassword:  true,
	}

	var authMethod user.AuthMethods
	err = tx.WithContext(ctx).
		Where("auth_type = ? AND auth_identifier = ?", authType, identifier).
		First(&authMethod).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return state, nil
	}
	state.AuthMethod = &authMethod

	if authType == portalAuthTypeEmail && !authMethod.Verified {
		state.AccountMode = portalAccountModeExistingUnverified
		state.VerificationType = portalVerificationTypeSecurity
		return state, nil
	}

	var userInfo user.User
	if err = tx.WithContext(ctx).Where("id = ?", authMethod.UserId).First(&userInfo).Error; err != nil {
		return nil, err
	}
	state.User = &userInfo
	state.AccountMode = portalAccountModeExistingVerified
	state.NextAction = portalNextActionInputPassword
	state.VerificationType = portalVerificationTypeNone
	return state, nil
}

func ensurePortalOrderUser(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, req *types.PortalPurchaseRequest) (*user.User, error) {
	authType, err := requirePortalPurchaseIdentity(req.AuthType, req.Identifier)
	if err != nil {
		return nil, err
	}
	req.AuthType = authType

	state, err := resolvePortalAccountState(ctx, svcCtx, tx, req.AuthType, req.Identifier)
	if err != nil {
		return nil, err
	}

	if state.AccountMode != portalAccountModeExistingVerified || state.User == nil {
		return nil, newPortalCheckoutVerificationRequiredErr()
	}

	if req.Password == "" {
		return nil, newPortalCheckoutPasswordRequiredErr()
	}

	if !tool.MultiPasswordVerify(state.User.Algo, state.User.Salt, req.Password, state.User.Password) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}

	return state.User, nil
}

func resolvePortalOrderUserForPurchase(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, req *types.PortalPurchaseRequest, ticketPayload *portalVerificationTicketPayload) (*user.User, error) {
	authType, err := requirePortalPurchaseIdentity(req.AuthType, req.Identifier)
	if err != nil {
		return nil, err
	}
	req.AuthType = authType

	state, err := resolvePortalAccountState(ctx, svcCtx, tx, req.AuthType, req.Identifier)
	if err != nil {
		return nil, err
	}

	switch state.AccountMode {
	case portalAccountModeExistingVerified:
		return ensurePortalOrderUser(ctx, svcCtx, tx, req)
	case portalAccountModeNewEmail:
		if req.Password == "" {
			return nil, newPortalCheckoutPasswordRequiredErr()
		}
		if err = validatePortalVerificationTicketPayload(ticketPayload, req.AuthType, req.Identifier, req.IP, req.UserAgent, portalAccountModeNewEmail); err != nil {
			return nil, err
		}
		return createPortalVerifiedUser(ctx, svcCtx, tx, req)
	case portalAccountModeExistingUnverified:
		if req.Password == "" {
			return nil, newPortalCheckoutPasswordRequiredErr()
		}
		if err = validatePortalVerificationTicketPayload(ticketPayload, req.AuthType, req.Identifier, req.IP, req.UserAgent, portalAccountModeExistingUnverified); err != nil {
			return nil, err
		}
		if state.AuthMethod == nil {
			return nil, newPortalCheckoutVerificationRequiredErr()
		}
		var userInfo user.User
		if err = tx.WithContext(ctx).Where("id = ?", state.AuthMethod.UserId).First(&userInfo).Error; err != nil {
			return nil, err
		}
		if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, req.Password, userInfo.Password) {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
		}
		if err = tx.WithContext(ctx).
			Model(&user.AuthMethods{}).
			Where("id = ?", state.AuthMethod.Id).
			Update("verified", true).Error; err != nil {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseUpdateError), "update auth method verify status error: %v", err.Error())
		}
		state.AuthMethod.Verified = true
		return &userInfo, nil
	default:
		return nil, newPortalCheckoutVerificationRequiredErr()
	}
}

func createPortalVerifiedUser(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, req *types.PortalPurchaseRequest) (*user.User, error) {
	userInfo := &user.User{
		Password:          tool.EncodePassWord(req.Password),
		Algo:              "default",
		OnlyFirstPurchase: &svcCtx.Config.Invite.OnlyFirstPurchase,
	}
	if err := tx.WithContext(ctx).Create(userInfo).Error; err != nil {
		return nil, err
	}

	userInfo.ReferCode = uuidx.UserInviteCode(userInfo.Id)
	if err := tx.WithContext(ctx).
		Model(&user.User{}).
		Where("id = ?", userInfo.Id).
		Update("refer_code", userInfo.ReferCode).Error; err != nil {
		return nil, err
	}

	authRecord := &user.AuthMethods{
		UserId:         userInfo.Id,
		AuthType:       req.AuthType,
		AuthIdentifier: req.Identifier,
		Verified:       true,
	}
	if err := tx.WithContext(ctx).Create(authRecord).Error; err != nil {
		return nil, err
	}

	if req.InviteCode != "" {
		var referer user.User
		if err := tx.WithContext(ctx).Where("refer_code = ?", req.InviteCode).First(&referer).Error; err == nil {
			userInfo.RefererId = referer.Id
			if err = tx.WithContext(ctx).
				Model(&user.User{}).
				Where("id = ?", userInfo.Id).
				Update("referer_id", referer.Id).Error; err != nil {
				return nil, err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, err
		}
	}

	if err := promo.NewService(svcCtx).GrantSignupPromo(ctx, userInfo.Id, tx); err != nil {
		return nil, err
	}

	return userInfo, nil
}

type portalVerificationRequirement struct {
	AccountMode      string
	VerificationType string
}

func resolvePortalVerificationRequirement(state *portalPreviewState) *portalVerificationRequirement {
	if state == nil {
		return nil
	}

	switch state.AccountMode {
	case portalAccountModeNewEmail:
		return &portalVerificationRequirement{
			AccountMode:      portalAccountModeNewEmail,
			VerificationType: portalVerificationTypeRegister,
		}
	case portalAccountModeExistingUnverified:
		return &portalVerificationRequirement{
			AccountMode:      portalAccountModeExistingUnverified,
			VerificationType: portalVerificationTypeSecurity,
		}
	default:
		return nil
	}
}

func portalVerificationTypeToConstant(verificationType string) uint8 {
	switch verificationType {
	case portalVerificationTypeRegister:
		return uint8(constant.Register)
	case portalVerificationTypeSecurity:
		return uint8(constant.Security)
	default:
		return 0
	}
}
