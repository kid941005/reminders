package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// 请求体上限不能按 Content-Type 判断：gin 的 ShouldBindJSON 不看 Content-Type，
// 直接 json.Decode(req.Body)。旧实现只在 application/json 时才套 MaxBytesReader，
// 客户端写 text/plain 就能提交任意大的 JSON，把整包读进内存。
func TestLimitJSONBodyIgnoresContentType(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LimitJSONBody(64, "/api/upload"))
	r.POST("/echo", func(c *gin.Context) {
		var payload []int
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})

	oversized := "[" + strings.Repeat("1,", 200) + "1]"
	req := httptest.NewRequest(http.MethodPost, "/echo", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "text/plain")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("text/plain 的超大请求体未被限制，返回 %d", recorder.Code)
	}
}

// 上传路由自己设了更大的上限，必须被豁免，否则 1MB 上限会把正常上传截断。
func TestLimitJSONBodyExemptsUploadPath(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(LimitJSONBody(64, "/api/upload"))
	r.POST("/api/upload", func(c *gin.Context) {
		var payload []int
		if err := c.ShouldBindJSON(&payload); err != nil {
			c.Status(http.StatusRequestEntityTooLarge)
			return
		}
		c.Status(http.StatusOK)
	})

	oversized := "[" + strings.Repeat("1,", 200) + "1]"
	req := httptest.NewRequest(http.MethodPost, "/api/upload", strings.NewReader(oversized))
	req.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	r.ServeHTTP(recorder, req)

	if recorder.Code != http.StatusOK {
		t.Fatalf("上传路径应被豁免，实际返回 %d", recorder.Code)
	}
}
