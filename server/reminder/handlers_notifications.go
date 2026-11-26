package reminder

import (
	"net/http"
	"time"

	"smallgo/server/response"
	"smallgo/server/utils"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

func handleNotifications(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		userID := currentUserID(c)
		page, pageSize, paged := pageParams(c)
		base := db.Model(&Notification{}).Where("user_id = ?", userID)
		var total int64
		if paged {
			if err := base.Count(&total).Error; err != nil {
				response.ErrorInternal(c, "统计通知失败")
				return
			}
		}
		var rows []Notification
		q := db.Where("user_id = ?", userID).Order("id DESC")
		if paged {
			q = q.Offset(utils.Offset(page, pageSize)).Limit(pageSize)
		} else {
			q = q.Limit(100)
		}
		if err := q.Find(&rows).Error; err != nil {
			response.ErrorInternal(c, "读取通知失败")
			return
		}
		if paged {
			response.SuccessPage(c, rows, total, page, pageSize)
			return
		}
		response.Success(c, rows)
	}
}

func handleUnreadCount(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		var count int64
		if err := db.Model(&Notification{}).Where("user_id = ? AND read_at IS NULL", currentUserID(c)).Count(&count).Error; err != nil {
			response.ErrorInternal(c, "读取未读数量失败")
			return
		}
		response.Success(c, gin.H{"count": count})
	}
}

func handleReadNotification(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		id, ok := parseID(c)
		if !ok {
			return
		}
		now := time.Now()
		result := db.Model(&Notification{}).Where("id = ? AND user_id = ?", id, currentUserID(c)).Update("read_at", &now)
		if result.Error != nil {
			response.ErrorInternal(c, "更新通知失败")
			return
		}
		if result.RowsAffected == 0 {
			response.Error(c, http.StatusNotFound, response.CodeNotFound, "通知不存在")
			return
		}
		publishDataChanged(currentUserID(c), "notification", "read", id, originClientID(c))
		response.Success(c, gin.H{"read": true})
	}
}

func handleReadAllNotifications(db *gorm.DB) gin.HandlerFunc {
	return func(c *gin.Context) {
		now := time.Now()
		if err := db.Model(&Notification{}).Where("user_id = ? AND read_at IS NULL", currentUserID(c)).Update("read_at", &now).Error; err != nil {
			response.ErrorInternal(c, "更新通知失败")
			return
		}
		publishDataChanged(currentUserID(c), "notification", "read-all", 0, originClientID(c))
		response.Success(c, gin.H{"read": true})
	}
}
