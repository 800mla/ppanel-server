package common

import (
	"context"

	"github.com/perfect-panel/server/internal/report"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/tool"
	"github.com/perfect-panel/server/pkg/xerr"
	"github.com/pkg/errors"
)

type GetGlobalConfigLogic struct {
	logger.Logger
	ctx    context.Context
	svcCtx *svc.ServiceContext
}

// Get global config
func NewGetGlobalConfigLogic(ctx context.Context, svcCtx *svc.ServiceContext) *GetGlobalConfigLogic {
	return &GetGlobalConfigLogic{
		Logger: logger.WithContext(ctx),
		ctx:    ctx,
		svcCtx: svcCtx,
	}
}

func (l *GetGlobalConfigLogic) GetGlobalConfig() (resp *types.GetGlobalConfigResponse, err error) {
	resp = new(types.GetGlobalConfigResponse)

	currencyCfg, err := l.svcCtx.SystemModel.GetCurrencyConfig(l.ctx)
	if err != nil {
		l.Logger.Error("[GetGlobalConfigLogic] GetCurrencyConfig error: ", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "GetCurrencyConfig error: %v", err.Error())
	}
	verifyCodeCfg, err := l.svcCtx.SystemModel.GetVerifyCodeConfig(l.ctx)
	if err != nil {
		l.Logger.Error("[GetGlobalConfigLogic] GetVerifyCodeConfig error: ", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "GetVerifyCodeConfig error: %v", err.Error())
	}
	verifyCfg, err := l.svcCtx.SystemModel.GetVerifyConfig(l.ctx)
	if err != nil {
		l.Logger.Error("[GetGlobalConfigLogic] GetVerifyConfig error: ", logger.Field("error", err.Error()))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "GetVerifyConfig error: %v", err.Error())
	}

	tool.DeepCopy(&resp.Site, l.svcCtx.Config.Site)
	tool.DeepCopy(&resp.Subscribe, l.svcCtx.Config.Subscribe)
	tool.DeepCopy(&resp.Auth.Email, l.svcCtx.Config.Email)
	tool.DeepCopy(&resp.Auth.Mobile, l.svcCtx.Config.Mobile)
	tool.DeepCopy(&resp.Auth.Device, l.svcCtx.Config.Device)
	tool.DeepCopy(&resp.Auth.Register, l.svcCtx.Config.Register)
	tool.DeepCopy(&resp.Invite, l.svcCtx.Config.Invite)
	tool.SystemConfigSliceReflectToStruct(currencyCfg, &resp.Currency)
	tool.SystemConfigSliceReflectToStruct(verifyCodeCfg, &resp.VerifyCode)
	tool.SystemConfigSliceReflectToStruct(verifyCfg, &resp.Verify)

	if report.IsGatewayMode() {
		resp.Subscribe.SubscribePath = "/sub" + l.svcCtx.Config.Subscribe.SubscribePath
	}

	var methods []string

	authMethods, err := l.svcCtx.AuthModel.FindAll(l.ctx)
	if err != nil {
		l.Logger.Error("[GetGlobalConfigLogic] FindAll error: ", logger.Field("error", err.Error()))
	}

	for _, method := range authMethods {
		if method.Enabled == nil || !*method.Enabled {
			continue
		}

		if method.Method == "device" {
			if l.svcCtx.Config.Device.Enable {
				methods = append(methods, method.Method)
			}
			continue
		}

		methods = append(methods, method.Method)
	}
	resp.OAuthMethods = methods

	webAds, err := l.svcCtx.SystemModel.FindOneByKey(l.ctx, "WebAD")
	if err != nil {
		l.Logger.Error("[GetGlobalConfigLogic] FindOneByKey error: ", logger.Field("error", err.Error()), logger.Field("key", "WebAD"))
		return nil, errors.Wrapf(xerr.NewErrCode(xerr.DatabaseQueryError), "FindOneByKey error: %v", err.Error())
	}
	resp.WebAd = webAds.Value == "true"
	return
}
