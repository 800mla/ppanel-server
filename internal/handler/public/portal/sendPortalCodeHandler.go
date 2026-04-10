package portal

import (
	"github.com/gin-gonic/gin"
	"github.com/perfect-panel/server/internal/logic/public/portal"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/internal/types"
	"github.com/perfect-panel/server/pkg/result"
)

func SendPortalCodeHandler(svcCtx *svc.ServiceContext) func(c *gin.Context) {
	return func(c *gin.Context) {
		var req types.PortalSendCodeRequest
		_ = c.ShouldBind(&req)
		req.IP = c.ClientIP()
		req.UserAgent = c.Request.UserAgent()

		validateErr := svcCtx.Validate(&req)
		if validateErr != nil {
			result.ParamErrorResult(c, validateErr)
			return
		}

		l := portal.NewSendPortalCodeLogic(c.Request.Context(), svcCtx)
		resp, err := l.SendPortalCode(&req)
		result.HttpResult(c, resp, err)
	}
}
