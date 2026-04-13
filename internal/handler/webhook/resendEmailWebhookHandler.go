package webhook

import (
	"github.com/gin-gonic/gin"
	"github.com/perfect-panel/server/internal/logic/webhook"
	"github.com/perfect-panel/server/internal/svc"
	"github.com/perfect-panel/server/pkg/result"
)

func ResendEmailWebhookHandler(svcCtx *svc.ServiceContext) func(c *gin.Context) {
	return func(c *gin.Context) {
		l := webhook.NewResendEmailWebhookLogic(c.Request.Context(), svcCtx)
		err := l.Handle(c.Request)
		result.HttpResult(c, map[string]bool{"ok": err == nil}, err)
	}
}
