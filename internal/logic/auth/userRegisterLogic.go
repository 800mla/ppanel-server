package auth

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/perfect-panel/server/internal/config"
	"github.com/perfect-panel/server/internal/logic/admin/group"
	"github.com/perfect-panel/server/internal/logic/common"
	"github.com/perfect-panel/server/internal/model/log"
	"github.com/perfect-panel/server/internal/model/user"
	"github.com/perfect-panel/server/internal/promo"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/captcha"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/jwt"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/uuidx"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
	"gorm.io/gorm"
)

type UserRegisterLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// NewUserRegisterLogic User register
func NewUserRegisterLogic(ctx context.Context, svcCtx *svc.ServiceContext) *UserRegisterLogic {
	return &UserRegisterLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *UserRegisterLogic) UserRegister(req *types.UserRegisterRequest) (resp *types.LoginResponse, err error) {

	c := l.svcCtx.Config.Register
	email := l.svcCtx.Config.Email
	var referer *user.User
	var trialSubscribe *user.Subscribe
	// Check if the registration is stopped
	if c.StopRegister {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.StopRegister), "stop register")
	}

	if req.Invite == "" {
		if l.svcCtx.Config.Invite.ForcedInvite {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.InviteCodeError), "invite code is required")
		}
	} else {
		// Check if the invite code is valid
		referer, err = l.svcCtx.UserModel.FindOneByReferCode(l.ctx, req.Invite)
		if err != nil {
			l.Errorw("FindOneByReferCode Error", logger.Field("error", err))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.InviteCodeError), "invite code is invalid")
		}
	}

	// if the email verification is enabled, the verification code is required
	if email.EnableVerify {
		cacheKey := fmt.Sprintf("%s:%s:%s", config.AuthCodeCacheKey, constant.Register, req.Email)
		value, err := l.svcCtx.Redis.Get(l.ctx, cacheKey).Result()
		if err != nil {
			l.Errorw("Redis Error", logger.Field("error", err.Error()), logger.Field("cacheKey", cacheKey))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.VerifyCodeError), "code error")
		}
		var payload common.CacheKeyPayload
		err = json.Unmarshal([]byte(value), &payload)
		if err != nil {
			l.Errorw("Unmarshal Error", logger.Field("error", err.Error()), logger.Field("value", value))
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.VerifyCodeError), "code error")
		}
		if payload.Code != req.Code {
			return nil, errors.Wrapf(xerr.NewErrCode(xerr.VerifyCodeError), "code error")
		}
	}

	// Verify captcha
	if err := l.verifyCaptcha(req); err != nil {
		return nil, err
	}

	// Check if the user exists
	u, err := l.svcCtx.UserModel.FindOneByEmail(l.ctx, req.Email)
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		l.Errorw("FindOneByEmail Error", logger.Field("error", err))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "query user info failed: %v", err.Error())
	} else if err == nil && !u.DeletedAt.Valid {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserExist), "user email exist: %v", req.Email)
	} else if err == nil && u.DeletedAt.Valid {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.UserDisabled), "user email deleted: %v", req.Email)
	}

	if !registerIpLimit(l.svcCtx, l.ctx, req.IP, "email", req.Email) {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.RegisterIPLimit), "register ip limit: %v", req.IP)
	}

	// Generate password
	pwd := tool.EncodePassWord(req.Password)
	userInfo := &user.User{
		Password:          pwd,
		Algo:              "default",
		OnlyFirstPurchase: &l.svcCtx.Config.Invite.OnlyFirstPurchase,
	}
	if referer != nil {
		userInfo.RefererId = referer.Id
	}
	err = l.svcCtx.UserModel.Transaction(l.ctx, func(db *gorm.DB) error {
		// Save user information
		if err := db.Create(userInfo).Error; err != nil {
			return err
		}
		// Generate ReferCode
		userInfo.ReferCode = uuidx.UserInviteCode(userInfo.Id)
		// Update ReferCode
		if err := db.Model(&user.User{}).Where("id = ?", userInfo.Id).Update("refer_code", userInfo.ReferCode).Error; err != nil {
			return err
		}
		// create user auth info
		authInfo := &user.AuthMethods{
			UserId:         userInfo.Id,
			AuthType:       "email",
			AuthIdentifier: req.Email,
			Verified:       email.EnableVerify,
		}
		if err = db.Create(authInfo).Error; err != nil {
			return err
		}

		if err = promo.NewService(l.svcCtx).GrantSignupPromo(l.ctx, userInfo.Id, db); err != nil {
			return err
		}

		return nil
	})
	if err != nil {
		return nil, err
	}

	// Activate trial subscription after transaction success (moved outside transaction to reduce lock time)
	if l.svcCtx.Config.Register.EnableTrial {
		trialSubscribe, err = l.activeTrial(userInfo.Id)
		if err != nil {
			l.Errorw("Failed to activate trial subscription", logger.Field("error", err.Error()))
			// Don't fail registration if trial activation fails
		}
	}

	// Clear cache after transaction success
	if l.svcCtx.Config.Register.EnableTrial && trialSubscribe != nil {
		// Trigger user group recalculation (runs in background)
		go func() {
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()

			// Check if group management is enabled
			var groupEnabled string
			err := l.svcCtx.DB.Table("system").
				Where("`category` = ? AND `key` = ?", "group", "enabled").
				Select("value").
				Scan(&groupEnabled).Error
			if err != nil || groupEnabled != "true" && groupEnabled != "1" {
				l.Debugf("Group management not enabled, skipping recalculation")
				return
			}

			// Get the configured grouping mode
			var groupMode string
			err = l.svcCtx.DB.Table("system").
				Where("`category` = ? AND `key` = ?", "group", "mode").
				Select("value").
				Scan(&groupMode).Error
			if err != nil {
				l.Errorw("Failed to get group mode", logger.Field("error", err.Error()))
				return
			}

			// Validate group mode
			if groupMode != "average" && groupMode != "subscribe" && groupMode != "traffic" {
				l.Debugf("Invalid group mode (current: %s), skipping", groupMode)
				return
			}

			// Trigger group recalculation with the configured mode
			logic := group.NewRecalculateGroupLogic(ctx, l.svcCtx)
			req := &types.RecalculateGroupRequest{
				Mode: groupMode,
			}

			if err := logic.RecalculateGroup(req); err != nil {
				l.Errorw("Failed to recalculate user group",
					logger.Field("user_id", userInfo.Id),
					logger.Field("error", err.Error()),
				)
				return
			}

			l.Infow("Successfully recalculated user group after registration",
				logger.Field("user_id", userInfo.Id),
				logger.Field("mode", groupMode),
			)
		}()

		// Clear user subscription cache
		if err = l.svcCtx.UserModel.ClearSubscribeCache(l.ctx, trialSubscribe); err != nil {
			l.Errorw("ClearSubscribeCache failed", logger.Field("error", err.Error()), logger.Field("userSubscribeId", trialSubscribe.Id))
			// Don't return error, just log it
		}
		// Clear subscription cache
		if err = l.svcCtx.SubscribeModel.ClearCache(l.ctx, trialSubscribe.SubscribeId); err != nil {
			l.Errorw("ClearSubscribeCache failed", logger.Field("error", err.Error()), logger.Field("subscribeId", trialSubscribe.SubscribeId))
			// Don't return error, just log it
		}
		// Clear all server cache
		if err = l.svcCtx.NodeModel.ClearServerAllCache(l.ctx); err != nil {
			l.Errorf("ClearServerAllCache error: %v", err.Error())
			// Don't return error, just log it
		}
	}
	// Bind device to user if identifier is provided
	if req.Identifier != "" {
		bindLogic := NewBindDeviceLogic(l.ctx, l.svcCtx)
		if err := bindLogic.BindDeviceToUser(req.Identifier, req.IP, req.UserAgent, userInfo.Id); err != nil {
			l.Errorw("failed to bind device to user",
				logger.Field("user_id", userInfo.Id),
				logger.Field("identifier", req.Identifier),
				logger.Field("error", err.Error()),
			)
			// Don't fail register if device binding fails, just log the error
		}
	}
	if l.ctx.Value(constant.CtxLoginType) != nil {
		req.LoginType = l.ctx.Value(constant.CtxLoginType).(string)
	}
	// Generate session id
	sessionId := uuidx.NewUUID().String()
	// Generate token
	token, err := jwt.NewJwtToken(
		l.svcCtx.Config.JwtAuth.AccessSecret,
		time.Now().Unix(),
		l.svcCtx.Config.JwtAuth.AccessExpire,
		jwt.WithOption("UserId", userInfo.Id),
		jwt.WithOption("SessionId", sessionId),
		jwt.WithOption("LoginType", req.LoginType),
	)
	if err != nil {
		l.Logger.Error("[UserLogin] token generate error", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "token generate error: %v", err.Error())
	}
	// Set session id
	sessionIdCacheKey := fmt.Sprintf("%v:%v", config.SessionIdKey, sessionId)
	if err := l.svcCtx.Redis.Set(l.ctx, sessionIdCacheKey, userInfo.Id, time.Duration(l.svcCtx.Config.JwtAuth.AccessExpire)*time.Second).Err(); err != nil {
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.ERROR), "set session id error: %v", err.Error())
	}
	loginStatus := true
	defer func() {
		if token != "" && userInfo != nil && userInfo.Id != 0 {
			loginLog := log.Login{
				Method:    "email",
				LoginIP:   req.IP,
				UserAgent: req.UserAgent,
				Success:   loginStatus,
				Timestamp: time.Now().UnixMilli(),
			}
			content, _ := loginLog.Marshal()
			if err := l.svcCtx.LogModel.Insert(l.ctx, &log.SystemLog{
				Id:       0,
				Type:     log.TypeLogin.Uint8(),
				Date:     time.Now().Format("2006-01-02"),
				ObjectID: userInfo.Id,
				Content:  string(content),
			}); err != nil {
				l.Errorw("failed to insert login log",
					logger.Field("user_id", userInfo.Id),
					logger.Field("ip", req.IP),
					logger.Field("error", err.Error()),
				)
			}

			// Register log
			registerLog := log.Register{
				AuthMethod: "email",
				Identifier: req.Email,
				RegisterIP: req.IP,
				UserAgent:  req.UserAgent,
				Timestamp:  time.Now().UnixMilli(),
			}
			content, _ = registerLog.Marshal()
			if err = l.svcCtx.LogModel.Insert(l.ctx, &log.SystemLog{
				Type:     log.TypeRegister.Uint8(),
				ObjectID: userInfo.Id,
				Date:     time.Now().Format("2006-01-02"),
				Content:  string(content),
			}); err != nil {
				l.Errorw("failed to insert login log",
					logger.Field("user_id", userInfo.Id),
					logger.Field("ip", req.IP),
					logger.Field("error", err.Error()))
			}
		}
	}()
	return &types.LoginResponse{
		Token: token,
	}, nil
}

func (l *UserRegisterLogic) activeTrial(uid int64) (*user.Subscribe, error) {
	sub, err := l.svcCtx.SubscribeModel.FindOne(l.ctx, l.svcCtx.Config.Register.TrialSubscribe)
	if err != nil {
		return nil, err
	}
	userSub := &user.Subscribe{
		UserId:      uid,
		OrderId:     0,
		SubscribeId: sub.Id,
		StartTime:   time.Now(),
		ExpireTime:  tool.AddTime(l.svcCtx.Config.Register.TrialTimeUnit, l.svcCtx.Config.Register.TrialTime, time.Now()),
		Traffic:     sub.Traffic,
		Download:    0,
		Upload:      0,
		Token:       uuidx.SubscribeToken(fmt.Sprintf("Trial-%v", uid)),
		UUID:        uuidx.NewUUID().String(),
		Status:      1,
	}
	if err = l.svcCtx.UserModel.InsertSubscribe(l.ctx, userSub); err != nil {
		return nil, err
	}
	return userSub, nil
}

func (l *UserRegisterLogic) verifyCaptcha(req *types.UserRegisterRequest) error {
	verifyCfg, err := l.svcCtx.SystemModel.GetVerifyConfig(l.ctx)
	if err != nil {
		l.Logger.Error("[UserRegisterLogic] GetVerifyConfig error: ", logger.Field("error", err.Error()))
		return errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "GetVerifyConfig error: %v", err.Error())
	}

	var cfg struct {
		CaptchaType               string `json:"captcha_type"`
		EnableUserRegisterCaptcha bool   `json:"enable_user_register_captcha"`
		TurnstileSecret           string `json:"turnstile_secret"`
	}
	tool.SystemConfigSliceReflectToStruct(verifyCfg, &cfg)

	if !cfg.EnableUserRegisterCaptcha {
		return nil
	}

	return captcha.VerifyCaptcha(l.ctx, l.svcCtx.Redis, cfg.CaptchaType, cfg.TurnstileSecret, captcha.VerifyInput{
		CaptchaId:   req.CaptchaId,
		CaptchaCode: req.CaptchaCode,
		CfToken:     req.CfToken,
		SliderToken: req.SliderToken,
		IP:          req.IP,
	})
}
