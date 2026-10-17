package user

import (
	"errors"
	"net/http"
	"strconv"

	"smallgo/server/audit"
	"smallgo/server/database"
	"smallgo/server/response"
	"smallgo/server/sysconfig"
	"smallgo/server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func parseUserID(c *gin.Context) (uint, bool) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 32)
	if err != nil || id == 0 {
		response.ErrorBadRequest(c, "无效的用户 ID")
		return 0, false
	}
	return uint(id), true
}

func handleSetupRequired(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var count int64
		if err := db.Model(&database.User{}).Count(&count).Error; err != nil {
			response.ErrorInternal(c, "检查初始化状态失败")
			return
		}
		response.Success(c, map[string]interface{}{
			"setup_required": count == 0,
		})
	}
}

func handleRegister(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Username string `json:"username" binding:"required"`
			Password string `json:"password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ErrorBadRequest(c, "请输入用户名和密码")
			return
		}

		secret := getJWTSecret(db)
		result, err := Register(db, req.Username, req.Password, secret)
		if err != nil {
			switch {
			case errors.Is(err, ErrRegisterDisabled):
				response.Error(c, http.StatusForbidden, response.CodeRegisterDisabled, err.Error())
			case errors.Is(err, ErrUserExists):
				response.Error(c, http.StatusConflict, response.CodeUserExists, err.Error())
			case errors.Is(err, ErrPasswordTooShort), errors.Is(err, ErrPasswordTooLong):
				response.ErrorBadRequest(c, err.Error())
			default:
				response.ErrorInternal(c, "注册失败")
			}
			return
		}

		response.Success(c, result)
	}
}

func handleLogin(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Username    string `json:"username" binding:"required"`
			Password    string `json:"password" binding:"required"`
			PasswordMd5 string `json:"password_md5"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ErrorBadRequest(c, "请输入用户名和密码")
			return
		}

		secret := getJWTSecret(db)
		result, err := Login(db, req.Username, req.Password, req.PasswordMd5, secret)
		if err != nil {
			response.Error(c, http.StatusUnauthorized, response.CodeInvalidCredentials, err.Error())
			return
		}

		// Populate context so the audit entry is attributed to the logged-in user.
		if u, ok := result["user"].(map[string]interface{}); ok {
			if uid, ok := u["id"].(uint); ok {
				c.Set("userID", uid)
				c.Set("username", req.Username)
			}
		}
		audit.Log(db, c, "login", "user", c.GetUint("userID"), "用户登录")

		response.Success(c, result)
	}
}

func setLoginAuditContext(c *gin.Context, result map[string]interface{}) {
	user, ok := result["user"].(map[string]interface{})
	if !ok {
		return
	}
	if uid, ok := user["id"].(uint); ok {
		c.Set("userID", uid)
	}
	if username, ok := user["username"].(string); ok {
		c.Set("username", username)
	}
}

func handleCheckAuth(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		requireLogin, _ := sysconfig.GetConfig(db, "require_login", 0)

		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)

		result := CheckAuth(db, uid, requireLogin)
		response.Success(c, result)
	}
}

// handleLogout 结束**应用自己的**登录态。
//
// 直连端口上会话就是应用签发的 JWT，前端清掉就结束，服务端只需把这个动作
// 记进审计；网关域上光清前端没用——服务端每个请求都能从网关注入的 NAS 身份
// 重新认出应用账号，所以必须落一条持久化抑制标记，告诉网关隐式认人「这位
// 用户主动登出了，别再自动放行」。两种环境返回同样的结果，前端不必分支。
//
// 这里刻意用 OptionalAuth：退出必须幂等，重复点击或会话已失效也应返回成功。
func handleLogout(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)
		if err := LogoutApplicationSession(db, uid); err != nil {
			response.ErrorInternal(c, "退出登录失败，请稍后重试")
			return
		}
		if uid != 0 {
			audit.Log(db, c, "logout", "user", uid, "用户退出登录")
		}
		response.Success(c, nil)
	}
}

// handleBootstrap 把 SPA 首屏必需的引导信息合并为一次往返：公开配置、
// 是否需要初始化、当前登录态。
//
// 这些数据以前分散在三个接口上串行请求，弱网下要多等两三个往返才渲染出
// 第一帧。合成一次请求后首屏只需一个 RTT。
func handleBootstrap(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		configs, err := sysconfig.GetPublicConfigs(db)
		if err != nil {
			response.ErrorInternal(c, "读取公开配置失败")
			return
		}

		var accountCount int64
		if err := db.Model(&database.User{}).Count(&accountCount).Error; err != nil {
			response.ErrorInternal(c, "检查初始化状态失败")
			return
		}

		requireLogin := configs["require_login"]
		if requireLogin == "" {
			requireLogin = "true"
		}

		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)

		response.Success(c, map[string]interface{}{
			"setup_required": accountCount == 0,
			"configs":        configs,
			"auth":           CheckAuth(db, uid, requireLogin),
		})
	}
}

func handleGetCurrentUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)

		result, err := GetCurrentUser(db, uid)
		if err != nil {
			response.ErrorInternal(c, "获取用户信息失败")
			return
		}

		response.Success(c, result)
	}
}

func handleChangePassword(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			OldPassword    string `json:"old_password" binding:"required"`
			OldPasswordMd5 string `json:"old_password_md5"`
			NewPassword    string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ErrorBadRequest(c, "请输入旧密码和新密码")
			return
		}

		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)

		if err := ChangePassword(db, uid, req.OldPassword, req.OldPasswordMd5, req.NewPassword); err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}

		response.Success(c, nil)
	}
}

func handleRegenerateAPIKey(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, _ := c.Get("userID")
		uid, _ := userID.(uint)

		newKey, err := RegenerateAPIKey(db, uid)
		if err != nil {
			response.ErrorInternal(c, "重新生成 API Key 失败")
			return
		}

		response.Success(c, map[string]string{"api_key": newKey})
	}
}

func handleGetAllUsers(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		page := utils.Atoi(c.Query("page"), 1)
		pageSize := utils.Atoi(c.Query("pageSize"), utils.DefaultPageSize)
		search := c.Query("search")

		users, total, err := GetAllUsers(db, page, pageSize, search)
		if err != nil {
			response.ErrorInternal(c, "获取用户列表失败")
			return
		}

		page, pageSize = utils.NormalizePage(page, pageSize)
		response.SuccessPage(c, users, total, page, pageSize)
	}
}

func handleUpdateUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			Role   *string `json:"role"`
			Status *int    `json:"status"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ErrorBadRequest(c, "请求无效")
			return
		}

		userID, ok := parseUserID(c)
		if !ok {
			return
		}

		if err := UpdateUser(db, userID, req.Role, req.Status); err != nil {
			if errors.Is(err, ErrUserNotFound) {
				response.Error(c, http.StatusNotFound, response.CodeNotFound, err.Error())
			} else {
				response.ErrorBadRequest(c, err.Error())
			}
			return
		}

		audit.Log(db, c, "user_update", "user", userID, "管理员修改用户信息")
		response.Success(c, nil)
	}
}

func handleDeleteUser(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := parseUserID(c)
		if !ok {
			return
		}
		if userID == c.GetUint("userID") {
			response.ErrorBadRequest(c, "不能删除自己的账号")
			return
		}
		if err := DeleteUser(db, userID); err != nil {
			switch {
			case errors.Is(err, ErrUserNotFound):
				response.Error(c, http.StatusNotFound, response.CodeNotFound, err.Error())
			case errors.Is(err, ErrLastAdmin):
				response.ErrorBadRequest(c, err.Error())
			default:
				response.ErrorInternal(c, "删除用户失败")
			}
			return
		}

		audit.Log(db, c, "user_delete", "user", userID, "管理员删除用户")
		response.Success(c, nil)
	}
}

func handleToggleStatus(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID, ok := parseUserID(c)
		if !ok {
			return
		}

		if err := ToggleUserStatus(db, userID); err != nil {
			switch {
			case errors.Is(err, ErrUserNotFound):
				response.Error(c, http.StatusNotFound, response.CodeNotFound, err.Error())
			case errors.Is(err, ErrLastAdmin):
				response.ErrorBadRequest(c, err.Error())
			default:
				response.ErrorInternal(c, "切换用户状态失败")
			}
			return
		}

		audit.Log(db, c, "user_status", "user", userID, "管理员切换用户状态")
		response.Success(c, nil)
	}
}

func handleResetPassword(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req struct {
			NewPassword string `json:"new_password" binding:"required"`
		}
		if err := c.ShouldBindJSON(&req); err != nil {
			response.ErrorBadRequest(c, "请输入新密码")
			return
		}

		userID, ok := parseUserID(c)
		if !ok {
			return
		}
		if err := ResetPassword(db, userID, req.NewPassword); err != nil {
			if errors.Is(err, ErrPasswordTooShort) || errors.Is(err, ErrPasswordTooLong) {
				response.ErrorBadRequest(c, err.Error())
			} else if errors.Is(err, ErrUserNotFound) {
				response.Error(c, http.StatusNotFound, response.CodeNotFound, err.Error())
			} else {
				response.ErrorInternal(c, "重置密码失败")
			}
			return
		}

		audit.Log(db, c, "password_reset", "user", userID, "管理员重置用户密码")
		response.Success(c, nil)
	}
}

// RegisterRoutes wires user and authentication routes.
func RegisterRoutes(publicGroup *gin.RouterGroup, optionalAuthGroup *gin.RouterGroup, authGroup *gin.RouterGroup, adminGroup *gin.RouterGroup, db *gorm.DB) {
	publicGroup.GET("/auth/setup-required", handleSetupRequired(db))
	publicGroup.POST("/auth/register", handleRegister(db))
	publicGroup.POST("/auth/login", handleLogin(db))

	optionalAuthGroup.GET("/auth/check", handleCheckAuth(db))
	// 首屏引导：一次请求拿到渲染第一帧所需的全部信息（见 handleBootstrap）。
	// 走 optionalAuth，携带令牌时顺带完成鉴权，前端因此省掉一次 check 往返。
	optionalAuthGroup.GET("/bootstrap", handleBootstrap(db))

	authGroup.GET("/auth/me", handleGetCurrentUser(db))
	authGroup.PUT("/auth/password", handleChangePassword(db))
	authGroup.POST("/auth/apikey", handleRegenerateAPIKey(db))

	// 退出登录走 optionalAuth：会话已失效时重复调用也必须成功（幂等）。
	optionalAuthGroup.POST("/auth/logout", handleLogout(db))

	adminGroup.GET("/users", handleGetAllUsers(db))
	adminGroup.PUT("/users/:id", handleUpdateUser(db))
	adminGroup.DELETE("/users/:id", handleDeleteUser(db))
	adminGroup.PUT("/users/:id/status", handleToggleStatus(db))
	adminGroup.PUT("/users/:id/password", handleResetPassword(db))
}
