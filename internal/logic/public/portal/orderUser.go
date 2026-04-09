package portal

import (
	"context"

	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

const (
	portalCheckoutPasswordRequiredMsg         = "Password required for portal checkout"
	portalCheckoutEmailVerificationRequiredMsg = "Email verification required before portal checkout"
)

type portalPreviewState struct {
	User                *user.User
	CanPurchase         bool
	PurchaseBlockReason string
}

func resolvePortalPreviewState(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier, password string) (*portalPreviewState, error) {
	state := &portalPreviewState{}
	if authType == "" || identifier == "" {
		return state, nil
	}

	authMethod, err := svcCtx.UserModel.FindUserAuthMethodByOpenID(ctx, authType, identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			state.PurchaseBlockReason = portalCheckoutEmailVerificationRequiredMsg
			return state, nil
		}
		return nil, err
	}

	if authType == "email" && !authMethod.Verified {
		state.PurchaseBlockReason = portalCheckoutEmailVerificationRequiredMsg
		return state, nil
	}

	userInfo, err := svcCtx.UserModel.FindOne(ctx, authMethod.UserId)
	if err != nil {
		return nil, err
	}
	if password == "" {
		state.PurchaseBlockReason = portalCheckoutPasswordRequiredMsg
		return state, nil
	}
	if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, password, userInfo.Password) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}

	state.User = userInfo
	state.CanPurchase = true
	return state, nil
}

func newPortalCheckoutVerificationRequiredErr() error {
	return errors.Wrapf(
		xerr.NewErrCodeMsg(xerr.VerifyCodeError, portalCheckoutEmailVerificationRequiredMsg),
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

func ensurePortalOrderUser(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, req *types.PortalPurchaseRequest) (*user.User, error) {
	if req.AuthType == "" || req.Identifier == "" {
		return nil, newPortalCheckoutIdentityRequiredErr()
	}

	var authMethod user.AuthMethods
	err := tx.WithContext(ctx).
		Where("auth_type = ? AND auth_identifier = ?", req.AuthType, req.Identifier).
		First(&authMethod).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, err
	}

	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, newPortalCheckoutVerificationRequiredErr()
	}

	if req.AuthType == "email" && !authMethod.Verified {
		return nil, newPortalCheckoutVerificationRequiredErr()
	}

	var userInfo user.User
	if err = tx.WithContext(ctx).Where("id = ?", authMethod.UserId).First(&userInfo).Error; err != nil {
		return nil, err
	}

	if req.Password == "" {
		return nil, newPortalCheckoutPasswordRequiredErr()
	}

	if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, req.Password, userInfo.Password) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}

	return &userInfo, nil
}
