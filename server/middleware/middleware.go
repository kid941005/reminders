package middleware

import (
	"crypto/rand"
	"encoding/hex"
	"net/http"
	"strings"
	"time"

	"smallgo/server/auth"
	"smallgo/server/database"
	"smallgo/server/logger"
	"smallgo/server/observability"
	"smallgo/server/response"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

// LimitJSONBody caps the request body size.
//
// 不能按 Content-Type 判断：gin 的 ShouldBindJSON 不看 Content-Type，直接
// json.Decode(req.Body)，客户端只要写 text/plain 就能绕过体积上限，把整个
// 请求体读进内存。因此这里无条件生效，只对显式列出的路径豁免（上传路由
// 自己设了更大的上限）。
func LimitJSONBody(maxBytes int64, exemptPaths ...string) gin.HandlerFunc {
	return func(c *gin.Context) {
		for _, path := range exemptPaths {
			if c.Request.URL.Path == path {
				c.Next()
				return
			}
		}
		c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxBytes)
		c.Next()
	}
}

func CORS(origin string) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", origin)
		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization, X-API-Key, X-Client-Id, X-Skip-Auth-Redirect")
		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(204)
			return
		}
		c.Next()
	}
}

func RequireAuth(jwtSecret string, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user, ok := authenticate(c, jwtSecret, db); ok {
			setUserContext(c, user)
			c.Next()
			return
		}

		response.ErrorUnauthorized(c, "未授权")
		c.Abort()
	}
}

func OptionalAuth(jwtSecret string, db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if user, ok := authenticate(c, jwtSecret, db); ok {
			setUserContext(c, user)
		}
		c.Next()
	}
}

func authenticate(c *gin.Context, jwtSecret string, db *gorm.DB) (database.User, bool) {
	if apiKey := c.GetHeader("X-API-Key"); apiKey != "" {
		var user database.User
		if err := db.Where("api_key = ? AND status = ?", apiKey, 1).First(&user).Error; err == nil {
			return user, true
		}
	}

	tokenString := ""
	if authHeader := c.GetHeader("Authorization"); strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else if cookie, err := c.Cookie("token"); err == nil {
		tokenString = cookie
	}
	if tokenString != "" {
		if userID, _, _, authVersion, err := auth.ParseToken(tokenString, jwtSecret); err == nil {
			var user database.User
			if err := db.Where("id = ? AND status = ?", userID, 1).First(&user).Error; err == nil && user.AuthVersion == authVersion {
				return user, true
			}
		}
	}

	return database.User{}, false
}

func setUserContext(c *gin.Context, user database.User) {
	c.Set("userID", user.ID)
	c.Set("username", user.Username)
	c.Set("role", user.Role)
}

func RequireAdmin() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.GetString("role") != "admin" {
			response.ErrorForbidden(c, "禁止访问")
			c.Abort()
			return
		}
		c.Next()
	}
}

const requestIDKey = "request_id"

// RequestID propagates or generates a correlation id for every request. It is
// echoed in the X-Request-ID response header and stored in the gin context so
// handlers and structured logs can reference the same id.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := strings.TrimSpace(c.GetHeader("X-Request-ID"))
		if id == "" || len(id) > 128 {
			id = newRequestID()
		}
		c.Set(requestIDKey, id)
		c.Header("X-Request-ID", id)
		c.Next()
	}
}

func newRequestID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "req-" + time.Now().UTC().Format("20060102150405.000000000")
	}
	return hex.EncodeToString(b[:])
}

// GetRequestID returns the correlation id set by RequestID, or "" when the
// middleware is not installed (e.g. in unit tests that call handlers directly).
func GetRequestID(c *gin.Context) string {
	if v, ok := c.Get(requestIDKey); ok {
		if s, ok := v.(string); ok {
			return s
		}
	}
	return ""
}

func Logger() gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		path := c.Request.URL.Path
		method := c.Request.Method

		c.Next()

		latency := time.Since(start)
		status := c.Writer.Status()
		ip := c.ClientIP()

		observability.HTTPRequests.Inc()
		if status >= 400 {
			observability.HTTPErrors.Inc()
		}
		fields := map[string]interface{}{
			"request_id": GetRequestID(c),
			"method":     method,
			"path":       path,
			"status":     status,
			"latency_ms": latency.Milliseconds(),
			"ip":         ip,
			"bytes":      c.Writer.Size(),
		}
		if v, ok := c.Get("userID"); ok {
			fields["user_id"] = v
		}
		logger.Structured("INFO", "http_request", fields)
	}
}
