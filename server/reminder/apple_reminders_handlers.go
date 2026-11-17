package reminder

import (
	"errors"

	"gorm.io/gorm"

	"github.com/gin-gonic/gin"

	"smallgo/server/response"
)

func handleGetAppleReminders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		dto, err := getAppleReminderSyncDTO(db, currentUserID(c))
		if err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.Success(c, nil)
				return
			}
			response.ErrorInternal(c, "读取 Apple 提醒事项配置失败")
			return
		}
		response.Success(c, dto)
	}
}

func handleDiscoverAppleReminders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in AppleReminderDiscoverInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		lists, err := DiscoverAppleReminderLists(db, currentUserID(c), in)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, gin.H{"lists": lists})
	}
}

func handleSaveAppleReminders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in AppleReminderSyncInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		dto, err := SaveAppleReminderSync(db, currentUserID(c), in)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, dto)
	}
}

func handleDeleteAppleReminders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := DeleteAppleReminderSync(db, currentUserID(c)); err != nil {
			response.ErrorInternal(c, "关闭 Apple 提醒事项同步失败")
			return
		}
		response.Success(c, gin.H{"deleted": true})
	}
}

func handleRunAppleReminders(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := currentUserID(c)
		result, err := RunAppleReminderSync(db, userID)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		dto, _ := getAppleReminderSyncDTO(db, userID)
		response.Success(c, gin.H{"result": result, "config": dto})
	}
}
