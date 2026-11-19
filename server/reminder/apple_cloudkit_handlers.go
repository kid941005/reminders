package reminder

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"smallgo/server/logger"
	"smallgo/server/response"
)

type appleCloudKitLoginInput struct {
	AppleID  string `json:"apple_id"`
	Password string `json:"password"`
}

type appleCloudKitVerifyInput struct {
	LoginID string `json:"login_id"`
	Code    string `json:"code"`
	PhoneID int    `json:"phone_id"`
}

type appleCloudKitResendInput struct {
	LoginID string `json:"login_id"`
	PhoneID int    `json:"phone_id"`
}

func cloudKitUserID(c *gin.Context) interface{} {
	v, _ := c.Get("userID")
	return v
}

func handleAppleCloudKitLogin(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in appleCloudKitLoginInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		started := time.Now()
		logger.Info("cloudkit: 收到登录请求 user=%v account=%s", cloudKitUserID(c), appleCloudKitMaskID(in.AppleID))
		start, err := startAppleCloudKitLogin(in.AppleID, in.Password)
		if err != nil {
			logger.Warn("cloudkit: 登录请求失败 user=%v account=%s elapsed=%s err=%v", cloudKitUserID(c), appleCloudKitMaskID(in.AppleID), time.Since(started).Round(time.Millisecond), err)
			response.ErrorBadRequest(c, err.Error())
			return
		}
		if start.Need2FA {
			logger.Info("cloudkit: 登录请求返回两步验证 user=%v elapsed=%s", cloudKitUserID(c), time.Since(started).Round(time.Millisecond))
			payload := gin.H{"need_2fa": true, "login_id": start.LoginID}
			if start.Options != nil {
				payload["options"] = start.Options.dto()
			}
			if start.OptionsError != "" {
				payload["options_error"] = start.OptionsError
			}
			response.Success(c, payload)
			return
		}
		lists, err := appleCloudKitListsForSession(start.Session)
		if err != nil {
			logger.Warn("cloudkit: 登录后读取列表失败 user=%v elapsed=%s err=%v", cloudKitUserID(c), time.Since(started).Round(time.Millisecond), err)
			response.ErrorBadRequest(c, err.Error())
			return
		}
		logger.Info("cloudkit: 登录成功 user=%v lists=%d elapsed=%s", cloudKitUserID(c), len(lists), time.Since(started).Round(time.Millisecond))
		response.Success(c, gin.H{"need_2fa": false, "login_id": start.LoginID, "lists": lists})
	}
}

func handleAppleCloudKitVerify(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in appleCloudKitVerifyInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		started := time.Now()
		logger.Info("cloudkit: 收到验证码校验 user=%v phone_id=%d", cloudKitUserID(c), in.PhoneID)
		session, err := verifyAppleCloudKitLogin(in.LoginID, in.Code, in.PhoneID)
		if err != nil {
			logger.Warn("cloudkit: 验证码校验失败 user=%v phone_id=%d elapsed=%s err=%v", cloudKitUserID(c), in.PhoneID, time.Since(started).Round(time.Millisecond), err)
			response.ErrorBadRequest(c, err.Error())
			return
		}
		lists, err := appleCloudKitListsForSession(session)
		if err != nil {
			logger.Warn("cloudkit: 验证后读取列表失败 user=%v elapsed=%s err=%v", cloudKitUserID(c), time.Since(started).Round(time.Millisecond), err)
			response.ErrorBadRequest(c, err.Error())
			return
		}
		logger.Info("cloudkit: 验证码校验成功 user=%v lists=%d elapsed=%s", cloudKitUserID(c), len(lists), time.Since(started).Round(time.Millisecond))
		response.Success(c, gin.H{"lists": lists, "login_id": in.LoginID})
	}
}

func handleAppleCloudKitResend(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in appleCloudKitResendInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		started := time.Now()
		logger.Info("cloudkit: 收到重发验证码 user=%v phone_id=%d", cloudKitUserID(c), in.PhoneID)
		phone, err := resendAppleCloudKitLogin(in.LoginID, in.PhoneID)
		if err != nil {
			logger.Warn("cloudkit: 重发验证码失败 user=%v phone_id=%d elapsed=%s err=%v", cloudKitUserID(c), in.PhoneID, time.Since(started).Round(time.Millisecond), err)
			response.ErrorBadRequest(c, err.Error())
			return
		}
		logger.Info("cloudkit: 重发验证码成功 user=%v phone_id=%d elapsed=%s", cloudKitUserID(c), phone.ID, time.Since(started).Round(time.Millisecond))
		response.Success(c, gin.H{"sent": true, "phone": phone})
	}
}

func appleCloudKitListsForSession(session *appleCloudKitSession) ([]AppleReminderCollection, error) {
	client, err := newAppleCloudKitClientFromSession(session)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return client.listReminderLists(ctx)
}
