package reminder

import "gorm.io/gorm"

// purgeUserData removes every row this app owns for one account. It is called
// by the framework when an administrator deletes a user (see
// apps.RegisterUserDataCleanup) and runs inside that deletion's transaction.
//
// 顺序即外键顺序：提醒频道与完成记录先于提醒删除，投递尝试先于投递任务删除，
// 否则 SQLite 打开 foreign_keys 时整笔删除会失败。
func purgeUserData(tx *gorm.DB, userID uint) error {
	var reminderIDs []uint
	if err := tx.Model(&Reminder{}).Where("user_id = ?", userID).Pluck("id", &reminderIDs).Error; err != nil {
		return err
	}
	if len(reminderIDs) > 0 {
		var jobIDs []uint
		if err := tx.Model(&DeliveryJob{}).Where("user_id = ?", userID).Pluck("id", &jobIDs).Error; err != nil {
			return err
		}
		if len(jobIDs) > 0 {
			if err := tx.Where("job_id IN ?", jobIDs).Delete(&DeliveryAttempt{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Where("reminder_id IN ?", reminderIDs).Delete(&ReminderChannel{}).Error; err != nil {
			return err
		}
		if err := tx.Where("reminder_id IN ?", reminderIDs).Delete(&Completion{}).Error; err != nil {
			return err
		}
	}
	for _, model := range []interface{}{
		&DeliveryJob{}, &Notification{}, &Reminder{}, &List{}, &ChannelBinding{},
		&Countdown{}, &CalendarFeed{}, &CalendarSync{}, &QQBindCode{},
	} {
		if err := tx.Where("user_id = ?", userID).Delete(model).Error; err != nil {
			return err
		}
	}
	return nil
}
