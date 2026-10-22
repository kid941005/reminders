package reminder

import (
	"gorm.io/gorm"

	"github.com/gin-gonic/gin"

	"smallgo/server/response"
)

// handleGetCalendarSync returns the current configuration. An unconfigured user
// gets 200 with a null payload rather than 404: the settings page renders an
// empty form from it, and a 404 would surface as an error toast.
func handleGetCalendarSync(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		cfg, ok := getCalendarSync(db, currentUserID(c))
		if !ok {
			response.Success(c, nil)
			return
		}
		response.Success(c, calendarSyncDTO(db, cfg))
	}
}

func handleSaveCalendarSync(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var in CalendarSyncInput
		if err := c.ShouldBindJSON(&in); err != nil {
			response.ErrorBadRequest(c, "请求参数无效")
			return
		}
		cfg, err := SaveCalendarSync(db, currentUserID(c), in)
		if err != nil {
			// 保存阶段的错误基本都是凭据或地址问题，属于用户可修正的输入。
			response.ErrorBadRequest(c, err.Error())
			return
		}
		response.Success(c, calendarSyncDTO(db, cfg))
	}
}

func handleDeleteCalendarSync(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		if err := DeleteCalendarSync(db, currentUserID(c)); err != nil {
			response.ErrorInternal(c, "关闭同步失败")
			return
		}
		response.Success(c, gin.H{"deleted": true})
	}
}

func handleRunCalendarSync(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := currentUserID(c)
		imported, err := RunCalendarSync(db, userID)
		if err != nil {
			response.ErrorBadRequest(c, err.Error())
			return
		}
		cfg, _ := getCalendarSync(db, userID)
		response.Success(c, gin.H{"imported": imported, "config": calendarSyncDTO(db, cfg)})
	}
}
