package middleware

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/perfect-panel/server/internal/svc"
	pkgaes "github.com/perfect-panel/server/pkg/aes"
	"github.com/perfect-panel/server/pkg/constant"
	"github.com/perfect-panel/server/pkg/logger"
	"github.com/perfect-panel/server/pkg/result"
	"github.com/perfect-panel/server/pkg/xerr"

	"github.com/gin-gonic/gin"
)

const (
	noWritten     = -1
	defaultStatus = http.StatusOK
)

func DeviceMiddleware(srvCtx *svc.ServiceContext) func(c *gin.Context) {
	return func(c *gin.Context) {
		loginType := bindLoginTypeToContext(c)
		if !isDeviceRequest(c, loginType) {
			c.Next()
			return
		}

		if !srvCtx.DeviceAuthAvailable() {
			reason := srvCtx.DeviceAuthDisabledReason()
			logger.WithContext(c.Request.Context()).Errorw("[DeviceMiddleware] Device request rejected",
				logger.Field("path", c.Request.URL.Path),
				logger.Field("reason", reason),
			)
			result.HttpResult(c, nil, xerr.NewErrMsg(reason))
			c.Abort()
			return
		}

		rw := NewResponseWriter(c, srvCtx)
		if !rw.Decrypt() {
			result.HttpResult(c, nil, xerr.NewErrCode(xerr.InvalidCiphertext))
			c.Abort()
			return
		}
		c.Writer = rw
		c.Next()
		rw.FlushAbort()
	}
}

func bindLoginTypeToContext(c *gin.Context) string {
	ctx := c.Request.Context()
	if loginType, ok := ctx.Value(constant.CtxLoginType).(string); ok && loginType != "" {
		return loginType
	}

	if header := c.GetHeader("Login-Type"); header != "" {
		ctx = context.WithValue(ctx, constant.CtxLoginType, header)
		c.Request = c.Request.WithContext(ctx)
		return header
	}

	return ""
}

func isDeviceRequest(c *gin.Context, loginType string) bool {
	if loginType == "device" {
		return true
	}

	path := c.FullPath()
	if path == "" {
		path = c.Request.URL.Path
	}
	return path == "/v1/auth/login/device"
}

func NewResponseWriter(c *gin.Context, srvCtx *svc.ServiceContext) (rw *ResponseWriter) {
	rw = &ResponseWriter{
		c:              c,
		body:           new(bytes.Buffer),
		ResponseWriter: c.Writer,
	}
	rw.encryptionKey = srvCtx.Config.Device.SecuritySecret
	rw.encryptionMethod = "AES"
	rw.encryption = true
	return rw
}

func (rw *ResponseWriter) Encrypt() {
	if !rw.encryption {
		return
	}
	buf := rw.body.Bytes()
	params := map[string]interface{}{}
	err := json.Unmarshal(buf, &params)
	if err != nil {
		return
	}
	data := params["data"]
	if data != nil {
		var jsonData []byte
		str, ok := data.(string)
		if ok {
			jsonData = []byte(str)
		} else {
			jsonData, _ = json.Marshal(data)
		}
		encrypt, iv, err := pkgaes.Encrypt(jsonData, rw.encryptionKey)
		if err != nil {
			return
		}
		params["data"] = map[string]interface{}{
			"data": encrypt,
			"time": iv,
		}

	}
	marshal, _ := json.Marshal(params)
	rw.body.Reset()
	rw.body.Write(marshal)
}

func (rw *ResponseWriter) Decrypt() bool {
	if !rw.encryption {
		return true
	}

	query := rw.c.Request.URL.Query()
	dataStr := query.Get("data")
	timeStr := query.Get("time")
	if dataStr != "" && timeStr != "" {
		decrypt, err := pkgaes.Decrypt(dataStr, rw.encryptionKey, timeStr)
		if err == nil {
			params := map[string]interface{}{}
			err = json.Unmarshal([]byte(decrypt), &params)
			if err == nil {
				for k, v := range params {
					query.Set(k, fmt.Sprintf("%v", v))
				}
				query.Del("data")
				query.Del("time")
				rw.c.Request.RequestURI = fmt.Sprintf("%s?%s", rw.c.Request.RequestURI[:strings.Index(rw.c.Request.RequestURI, "?")], query.Encode())
				rw.c.Request.URL.RawQuery = query.Encode()
			}
		}
	}

	body, err := io.ReadAll(rw.c.Request.Body)
	if err != nil {
		return true
	}

	if len(body) == 0 {
		return true
	}

	params := map[string]interface{}{}
	err = json.Unmarshal(body, &params)
	data := params["data"]
	nonce := params["time"]
	if err != nil || data == nil {
		return false
	}

	str, ok := data.(string)
	if !ok {
		return false
	}
	iv, ok := nonce.(string)
	if !ok {
		return false
	}

	decrypt, err := pkgaes.Decrypt(str, rw.encryptionKey, iv)
	if err != nil {
		return false
	}
	rw.c.Request.Body = io.NopCloser(bytes.NewBuffer([]byte(decrypt)))
	return true
}

func (rw *ResponseWriter) FlushAbort() {
	defer rw.c.Abort()
	responseBody := rw.body.String()
	fmt.Println("Original Response Body:", responseBody)
	rw.flush = true
	if rw.encryption {
		rw.Encrypt()
	}
	_, err := rw.Write(rw.body.Bytes())
	if err != nil {
		return
	}
}

type ResponseWriter struct {
	http.ResponseWriter
	size             int
	status           int
	flush            bool
	body             *bytes.Buffer
	c                *gin.Context
	encryption       bool
	encryptionKey    string
	encryptionMethod string
}

func (rw *ResponseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

//nolint:unused
func (rw *ResponseWriter) reset(writer http.ResponseWriter) {
	rw.ResponseWriter = writer
	rw.size = noWritten
	rw.status = defaultStatus
}

func (rw *ResponseWriter) WriteHeader(code int) {
	if code > 0 && rw.status != code {
		if rw.Written() {
			return
		}
		rw.status = code
	}
}

func (rw *ResponseWriter) WriteHeaderNow() {
	if !rw.Written() {
		rw.size = 0
		rw.ResponseWriter.WriteHeader(rw.status)
	}
}

func (rw *ResponseWriter) Write(data []byte) (n int, err error) {
	if rw.flush {
		rw.WriteHeaderNow()
		n, err = rw.ResponseWriter.Write(data)
		rw.size += n
	} else {
		rw.body.Write(data)
	}
	return
}

func (rw *ResponseWriter) WriteString(s string) (n int, err error) {
	if rw.flush {
		rw.WriteHeaderNow()
		n, err = rw.ResponseWriter.Write([]byte(s))
		rw.size += n
	} else {
		rw.body.Write([]byte(s))
	}
	return
}

func (rw *ResponseWriter) Status() int {
	return rw.status
}

func (rw *ResponseWriter) Size() int {
	return rw.size
}

func (rw *ResponseWriter) Written() bool {
	return rw.size != noWritten
}

func (rw *ResponseWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if rw.size < 0 {
		rw.size = 0
	}
	return rw.ResponseWriter.(http.Hijacker).Hijack()
}

func (rw *ResponseWriter) CloseNotify() <-chan bool {
	done := rw.c.Request.Context().Done()
	closed := make(chan bool)

	go func() {
		<-done
		closed <- true
	}()

	return closed
}

func (rw *ResponseWriter) Flush() {
	rw.WriteHeaderNow()
	rw.ResponseWriter.(http.Flusher).Flush()
}

func (rw *ResponseWriter) Pusher() (pusher http.Pusher) {
	if pusher, ok := rw.ResponseWriter.(http.Pusher); ok {
		return pusher
	}
	return nil
}
