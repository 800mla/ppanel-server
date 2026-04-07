package portal

import (
	"context"

	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

func resolvePortalPreviewUser(ctx context.Context, svcCtx *svc.ServiceContext, authType, identifier, password string) (*user.User, bool, error) {
	if authType == "" || identifier == "" {
		return nil, false, nil
	}

	authMethod, err := svcCtx.UserModel.FindUserAuthMethodByOpenID(ctx, authType, identifier)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, true, nil
		}
		return nil, false, err
	}

	userInfo, err := svcCtx.UserModel.FindOne(ctx, authMethod.UserId)
	if err != nil {
		return nil, false, err
	}
	if password == "" {
		return nil, false, nil
	}
	if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, password, userInfo.Password) {
		return nil, false, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}
	return userInfo, false, nil
}

func ensurePortalOrderUser(ctx context.Context, svcCtx *svc.ServiceContext, tx *gorm.DB, req *types.PortalPurchaseRequest) (*user.User, bool, error) {
	var authMethod user.AuthMethods
	err := tx.WithContext(ctx).
		Where("auth_type = ? AND auth_identifier = ?", req.AuthType, req.Identifier).
		First(&authMethod).Error
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return nil, false, err
	}

	if err == nil {
		var userInfo user.User
		if err = tx.WithContext(ctx).Where("id = ?", authMethod.UserId).First(&userInfo).Error; err != nil {
			return nil, false, err
		}
		if !tool.MultiPasswordVerify(userInfo.Algo, userInfo.Salt, req.Password, userInfo.Password) {
			return nil, false, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
		}
		return &userInfo, false, nil
	}

	if req.Password == "" {
		return nil, false, errors.Wrapf(xerr.NewErrCode(xerr.UserPasswordError), "user password")
	}

	userInfo := &user.User{
		Password:          tool.EncodePassWord(req.Password),
		Algo:              "default",
		OnlyFirstPurchase: &svcCtx.Config.Invite.OnlyFirstPurchase,
	}
	if err = tx.WithContext(ctx).Create(userInfo).Error; err != nil {
		return nil, false, err
	}

	userInfo.ReferCode = uuidx.UserInviteCode(userInfo.Id)
	if err = tx.WithContext(ctx).
		Model(&user.User{}).
		Where("id = ?", userInfo.Id).
		Update("refer_code", userInfo.ReferCode).Error; err != nil {
		return nil, false, err
	}

	authRecord := &user.AuthMethods{
		UserId:         userInfo.Id,
		AuthType:       req.AuthType,
		AuthIdentifier: req.Identifier,
	}
	if err = tx.WithContext(ctx).Create(authRecord).Error; err != nil {
		return nil, false, err
	}

	if req.InviteCode != "" {
		var referer user.User
		if err = tx.WithContext(ctx).Where("refer_code = ?", req.InviteCode).First(&referer).Error; err == nil {
			userInfo.RefererId = referer.Id
			if err = tx.WithContext(ctx).
				Model(&user.User{}).
				Where("id = ?", userInfo.Id).
				Update("referer_id", referer.Id).Error; err != nil {
				return nil, false, err
			}
		} else if !errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, false, err
		}
	}

	if err = promo.NewService(svcCtx).GrantSignupPromo(ctx, userInfo.Id, tx); err != nil {
		return nil, false, err
	}

	return userInfo, true, nil
}
