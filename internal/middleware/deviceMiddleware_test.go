package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/perfect-panel/server/internal/config"
	"github.com/perfect-panel/server/internal/svc"
)

func TestDeviceMiddlewareAllowsNonDeviceRequestsWhenSecretIsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(DeviceMiddleware(&svc.ServiceContext{
		Config: config.Config{
			Device: config.DeviceConfig{Enable: true},
		},
	}))
	router.GET("/v1/common/site/config", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/v1/common/site/config", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}
	if recorder.Body.String() != "ok" {
		t.Fatalf("expected body ok, got %q", recorder.Body.String())
	}
}

func TestDeviceMiddlewareRejectsDeviceRequestsWhenSecretIsMissing(t *testing.T) {
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(DeviceMiddleware(&svc.ServiceContext{
		Config: config.Config{
			Device: config.DeviceConfig{Enable: true},
		},
		DeviceUnavailableReason: svc.DeviceAuthSecretMissingMsg,
	}))
	router.POST("/v1/auth/login/device", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/v1/auth/login/device", nil)
	router.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, recorder.Code)
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(recorder.Body.Bytes(), &payload); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if payload["msg"] != svc.DeviceAuthSecretMissingMsg {
		t.Fatalf("expected msg %q, got %#v", svc.DeviceAuthSecretMissingMsg, payload["msg"])
	}
}
