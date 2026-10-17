package middleware

import (
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

type visitor struct {
	count   int
	expires time.Time
}

type RateLimiter struct {
	mu          sync.Mutex
	visitors    map[string]*visitor
	rate        int
	window      time.Duration
	nextCleanup time.Time
}

func NewRateLimiter(rate int, window time.Duration) *RateLimiter {
	rl := &RateLimiter{
		visitors:    make(map[string]*visitor),
		rate:        rate,
		window:      window,
		nextCleanup: time.Now().Add(window),
	}
	return rl
}

func (rl *RateLimiter) Allow(ip string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	if !now.Before(rl.nextCleanup) {
		for key, item := range rl.visitors {
			if !now.Before(item.expires) {
				delete(rl.visitors, key)
			}
		}
		rl.nextCleanup = now.Add(rl.window)
	}

	v, exists := rl.visitors[ip]
	if !exists || !now.Before(v.expires) {
		rl.visitors[ip] = &visitor{count: 1, expires: now.Add(rl.window)}
		return true
	}

	if v.count >= rl.rate {
		return false
	}

	v.count++
	return true
}

// bootstrapReads 是 SPA 每次进入都必须读的只读引导接口，不参与限流：
// 它们既不是可爆破的凭证接口，又被前端当作「应用是否可用」的依据。一旦被
// 429，前端只能拿到默认值继续跑——初始化状态读不到就会把首次安装的用户带到
// 登录页而不是管理员创建页，且完全没有可见错误。限流的目的是抵挡凭证爆破，
// 不该牺牲这些幂等读。
var bootstrapReads = map[string]struct{}{
	"/configs/public":      {},
	"/auth/setup-required": {},
}

func isBootstrapRead(c *gin.Context) bool {
	if c.Request.Method != http.MethodGet {
		return false
	}
	path := c.Request.URL.Path
	// 按后缀匹配，避免部署在子路径下时匹配不到。
	for suffix := range bootstrapReads {
		if strings.HasSuffix(path, suffix) {
			return true
		}
	}
	return false
}

// rateLimitKey decides which bucket a request counts against.
//
// 单机 Docker 部署下按客户端 IP 分桶即可。反向代理场景要拿到真实客户端 IP，
// 需由前置代理设置 X-Forwarded-For，Gin 的 ClientIP() 会读取它。
func rateLimitKey(c *gin.Context) string {
	if ip := strings.TrimSpace(c.ClientIP()); ip != "" {
		return "ip:" + ip
	}
	return "unknown"
}

func LimitRequests(rl *RateLimiter) gin.HandlerFunc {
	if rl == nil {
		return func(c *gin.Context) { c.Next() }
	}
	return func(c *gin.Context) {
		if isBootstrapRead(c) {
			c.Next()
			return
		}
		if !rl.Allow(rateLimitKey(c)) {
			c.AbortWithStatusJSON(http.StatusTooManyRequests, gin.H{
				"code":    429,
				"message": "too many requests, please try again later",
				"data":    nil,
			})
			return
		}
		c.Next()
	}
}
