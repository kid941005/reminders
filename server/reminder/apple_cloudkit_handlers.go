package reminder

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"smallgo/server/response"
)

type appleCloudKitLoginInput struct {
	AppleID  string `json:"apple_id"`
	Password string `json:"password"`
}

type appleCloudKitVerifyInput struct {
	LoginID string `json:"login_id"`
	Code    string `json:"code"`
}

func handleAppleCloudKitLogin(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in appleCloudKitLoginInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		need2FA, loginID, session, err := startAppleCloudKitLogin(in.AppleID, in.Password)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		if need2FA {
			response.Success(c, gin.H{"need_2fa": true, "login_id": loginID})
			return
		}
		lists, err := appleCloudKitListsForSession(session)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, gin.H{"need_2fa": false, "login_id": loginID, "lists": lists})
	}
}

func handleAppleCloudKitVerify(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in appleCloudKitVerifyInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		session, err := verifyAppleCloudKitLogin(in.LoginID, in.Code)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		lists, err := appleCloudKitListsForSession(session)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, gin.H{"lists": lists, "login_id": in.LoginID})
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
