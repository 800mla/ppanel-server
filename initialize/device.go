package initialize

import (
	"context"
	"fmt"
	"strings"

	"github.com/perfect-panel/server/pkg/logger"

	"github.com/perfect-panel/server/internal/config"
	"github.com/perfect-panel/server/internal/model/auth"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/tool"
)

func Device(ctx *svc.ServiceContext) {
	logger.Debug("device config initialization")
	ctx.DeviceUnavailableReason = ""

	method, err := ctx.AuthModel.FindOneByMethod(context.Background(), "device")
	if err != nil {
		panic(err)
	}

	var cfg config.DeviceConfig
	if method.Enabled != nil {
		cfg.Enable = *method.Enabled
	}

	var deviceConfig auth.DeviceConfig
	if err := deviceConfig.Unmarshal(method.Config); err != nil {
		if cfg.Enable {
			ctx.DeviceUnavailableReason = fmt.Sprintf("%s: %v", svc.DeviceAuthInvalidConfigMsg, err)
			cfg.Enable = false
			logger.Errorf("[Init Device Config] %s", ctx.DeviceUnavailableReason)
		} else {
			logger.Errorf("[Init Device Config] Unmarshal device auth config error: %s", err.Error())
		}
		ctx.Config.Device = cfg
		return
	}

	tool.DeepCopy(&cfg, deviceConfig)
	if method.Enabled != nil {
		cfg.Enable = *method.Enabled
	}

	if cfg.Enable && strings.TrimSpace(cfg.SecuritySecret) == "" {
		ctx.DeviceUnavailableReason = svc.DeviceAuthSecretMissingMsg
		cfg.Enable = false
		logger.Errorf("[Init Device Config] %s", ctx.DeviceUnavailableReason)
	}

	ctx.Config.Device = cfg
}
