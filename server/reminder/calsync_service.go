package reminder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
)

// CalendarSyncInput is the payload for configuring the DingTalk CalDAV import.
// Every field except the credentials is optional; omitted values keep whatever
// was stored before (or the model defaults on first save).
type CalendarSyncInput struct {
	Server    string `json:"server"`
	Username  string `json:"username"`
	Password  string `json:"password"`
	Enabled   *bool  `json:"enabled"`
	WindowBackDays    *int `json:"window_back_days"`
	WindowForwardDays *int `json:"window_forward_days"`
	RemindBeforeMinutes *int `json:"remind_before_minutes"`
}

// syncListName is the dedicated list imported events land in. Keeping them
// separate matters: a sync that silently merges into the user's own list makes
// "why did this appear" impossible to answer, and pruning would then have to
// distinguish imported rows from user-created ones in the same list.
const syncListName = "钉钉日程"

func getCalendarSync(db *gorm.DB, userID uint) (CalendarSync, bool) {
	var cfg CalendarSync
	err := db.Where("user_id = ?", userID).First(&cfg).Error
	if err != nil {
		return CalendarSync{}, false
	}
	return cfg, true
}

// SaveCalendarSync validates the credentials against the server, then stores
// them. The password is kept from the previous row when the caller submits an
// empty one, so re-saving the window settings does not require retyping it.
func SaveCalendarSync(db *gorm.DB, userID uint, in CalendarSyncInput) (CalendarSync, error) {
	in.Server = strings.TrimSpace(in.Server)
	in.Username = strings.TrimSpace(in.Username)
	if in.Server == "" || in.Username == "" {
		return CalendarSync{}, errors.New("请填写服务器地址与账号")
	}

	existing, hasExisting := getCalendarSync(db, userID)
	password := in.Password
	if password == "" {
		if !hasExisting {
			return CalendarSync{}, errors.New("请填写密码")
		}
		decrypted, err := decryptTarget(db, existing.Password)
		if err != nil {
			return CalendarSync{}, errors.New("原密码无法解密，请重新输入")
		}
		password = decrypted
	}

	// 先连一次再落库：凭据错误在保存时暴露，比等到半小时后的定时任务更早。
	client := newCalDAVClient(in.Username, password)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	calendarURL, err := client.Discover(ctx, in.Server)
	if err != nil {
		return CalendarSync{}, err
	}

	sealed, err := encryptTarget(db, password)
	if err != nil {
		return CalendarSync{}, errors.New("密码加密失败")
	}

	cfg := CalendarSync{}
	if hasExisting {
		cfg = existing
	} else {
		cfg.UserID = userID
		cfg.Enabled = true
		cfg.WindowBackDays = 7
		cfg.WindowForwardDays = 180
		cfg.RemindBeforeMinutes = 15
	}
	cfg.CalendarURL = calendarURL
	cfg.Username = in.Username
	cfg.Password = sealed
	if in.Enabled != nil {
		cfg.Enabled = *in.Enabled
	}
	if in.WindowBackDays != nil && *in.WindowBackDays >= 0 && *in.WindowBackDays <= 365 {
		cfg.WindowBackDays = *in.WindowBackDays
	}
	if in.WindowForwardDays != nil && *in.WindowForwardDays > 0 && *in.WindowForwardDays <= 365 {
		cfg.WindowForwardDays = *in.WindowForwardDays
	}
	if in.RemindBeforeMinutes != nil && *in.RemindBeforeMinutes >= 0 && *in.RemindBeforeMinutes <= 60*24 {
		cfg.RemindBeforeMinutes = *in.RemindBeforeMinutes
	}

	if err := db.Transaction(func(tx *gorm.DB) error {
		list, err := ensureSyncList(tx, userID)
		if err != nil {
			return err
		}
		cfg.ListID = list.ID
		return tx.Save(&cfg).Error
	}); err != nil {
		return CalendarSync{}, err
	}
	return cfg, nil
}

func DeleteCalendarSync(db *gorm.DB, userID uint) error {
	return db.Where("user_id = ?", userID).Delete(&CalendarSync{}).Error
}

// ensureSyncList finds or creates the dedicated import list.
func ensureSyncList(tx *gorm.DB, userID uint) (List, error) {
	var list List
	err := tx.Where("user_id = ? AND name = ? AND deleted_at IS NULL", userID, syncListName).First(&list).Error
	if err == nil {
		return list, nil
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return list, err
	}
	list = List{UserID: userID, Name: syncListName, Color: "blue", Icon: "list"}
	if err := tx.Create(&list).Error; err != nil {
		// 并发保存时可能已被另一个请求创建。
		if findErr := tx.Where("user_id = ? AND name = ? AND deleted_at IS NULL", userID, syncListName).First(&list).Error; findErr == nil {
			return list, nil
		}
		return list, err
	}
	return list, nil
}

// RunCalendarSync performs one import pass. Manual runs ignore Enabled so the
// user can verify credentials before turning the schedule on.
func RunCalendarSync(db *gorm.DB, userID uint) (int, error) {
	cfg, ok := getCalendarSync(db, userID)
	if !ok {
		return 0, errors.New("尚未配置日历同步")
	}
	imported, err := syncCalendar(db, cfg)
	markSyncResult(db, cfg.ID, imported, err)
	return imported, err
}

// SyncAllCalendars is the scheduler entry point: one failure must not stop the
// remaining users.
func SyncAllCalendars(db *gorm.DB) {
	var configs []CalendarSync
	if err := db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
		return
	}
	for _, cfg := range configs {
		imported, err := syncCalendar(db, cfg)
		markSyncResult(db, cfg.ID, imported, err)
	}
}

func markSyncResult(db *gorm.DB, id uint, imported int, syncErr error) {
	updates := map[string]interface{}{
		"last_sync_at":   time.Now().UTC(),
		"last_imported":  imported,
		"last_status":    "ok",
		"last_error":     "",
	}
	if syncErr != nil {
		updates["last_status"] = "error"
		updates["last_error"] = truncateRunes(syncErr.Error(), 400)
	}
	_ = db.Model(&CalendarSync{}).Where("id = ?", id).Updates(updates).Error
}

func syncCalendar(db *gorm.DB, cfg CalendarSync) (int, error) {
	password, err := decryptTarget(db, cfg.Password)
	if err != nil {
		return 0, errors.New("密码解密失败，请重新保存配置")
	}
	from, to := cfg.window()

	client := newCalDAVClient(cfg.Username, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	events, err := client.FetchEvents(ctx, cfg.CalendarURL, from, to)
	if err != nil {
		return 0, err
	}

	seen := make(map[string]bool, len(events))
	imported := 0

	err = db.Transaction(func(tx *gorm.DB) error {
		for _, event := range events {
			uid := strings.TrimSpace(event.UID)
			// 没有 UID 就没有去重依据：重复导入会不断产生副本。
			if uid == "" || event.Status == "CANCELLED" {
				continue
			}
			seen[uid] = true

			title := strings.TrimSpace(event.Summary)
			if title == "" {
				title = "(无标题日程)"
			}
			title = truncateRunes(title, 200)
			due := event.Start.In(shanghai())
			if event.AllDay {
				due = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, shanghai())
			}
			notes := composeSyncNotes(event)

			var existing Reminder
			findErr := tx.Where("user_id = ? AND external_source = ? AND external_uid = ?",
				cfg.UserID, ExternalSourceDingTalk, uid).First(&existing).Error

			switch {
			case errors.Is(findErr, gorm.ErrRecordNotFound):
				created := Reminder{
					UserID: cfg.UserID, ListID: cfg.ListID, Title: title, Notes: notes,
					DueAt: &due, AllDay: event.AllDay, RepeatRule: event.RepeatRule,
					Calendar: "solar", RemindBeforeMinutes: cfg.RemindBeforeMinutes,
					ExternalSource: ExternalSourceDingTalk, ExternalUID: uid, Version: 1,
				}
				if err := tx.Create(&created).Error; err != nil {
					return err
				}
				channels := []string{ChannelInApp}
				if err := replaceChannels(tx, created, SaveReminderInput{Channels: channels}); err != nil {
					return err
				}
				if err := rebuildJobs(tx, created, channels); err != nil {
					return err
				}
				imported++
			case findErr != nil:
				return findErr
			default:
				// 已完成条目不覆盖：用户在本地处理过的日程不应该被远端重新激活。
				if existing.CompletedAt != nil {
					continue
				}
				existing.Title = title
				existing.Notes = notes
				existing.DueAt = &due
				existing.AllDay = event.AllDay
				existing.RepeatRule = event.RepeatRule
				existing.SnoozedUntil = nil
				existing.Version++
				if err := tx.Save(&existing).Error; err != nil {
					return err
				}
				channels := enabledChannelsOf(tx, existing)
				if err := rebuildJobs(tx, existing, channels); err != nil {
					return err
				}
				imported++
			}
		}

		// 远端删掉的日程：仅在同一时间窗内清理，避免把窗口外的东西误删。
		// seen 为空时直接跳过——一次空响应（例如凭据失效返回空集合）不应该清空本地。
		if len(seen) == 0 {
			return nil
		}
		uids := make([]string, 0, len(seen))
		for uid := range seen {
			uids = append(uids, uid)
		}
		// 不加时间窗条件：导入的来源只有这个窗口，窗口外的条目本来就不该由这里
		// 管理。若加上 due_at 范围判断，反而会踩到「due_at 以本地偏移存储、谓词用
		// 另一种时区」的字符串比较问题（见 CHANGELOG 的已知问题）。
		now := time.Now()
		return tx.Model(&Reminder{}).
			Where("user_id = ? AND external_source = ? AND completed_at IS NULL AND deleted_at IS NULL",
				cfg.UserID, ExternalSourceDingTalk).
			Where("external_uid NOT IN ?", uids).
			Update("deleted_at", now).Error
	})
	if err != nil {
		return imported, err
	}
	return imported, nil
}

func composeSyncNotes(event ICalEvent) string {
	notes := strings.TrimSpace(event.Description)
	if location := strings.TrimSpace(event.Location); location != "" {
		if notes != "" {
			notes += "\n"
		}
		notes += "地点：" + location
	}
	return notes
}

// enabledChannelsOf reads the channels currently linked to a reminder. Imported
// rows always have in-app enabled, but a user may have added more.
func enabledChannelsOf(tx *gorm.DB, item Reminder) []string {
	var rows []ReminderChannel
	if err := tx.Where("reminder_id = ? AND user_id = ? AND enabled = ?", item.ID, item.UserID, true).Find(&rows).Error; err != nil {
		return []string{ChannelInApp}
	}
	channels := make([]string, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, row.Channel)
	}
	if len(channels) == 0 {
		return []string{ChannelInApp}
	}
	return channels
}

// CalendarSyncDTO is what the settings page reads: the sealed password never
// leaves the server.
type CalendarSyncDTO struct {
	CalendarSync
	Server string `json:"server"`
	ListName string `json:"list_name"`
}

func calendarSyncDTO(db *gorm.DB, cfg CalendarSync) CalendarSyncDTO {
	dto := CalendarSyncDTO{CalendarSync: cfg}
	if parsed, err := parseServerHost(cfg.CalendarURL); err == nil {
		dto.Server = parsed
	}
	var list List
	if err := db.Select("name").First(&list, cfg.ListID).Error; err == nil {
		dto.ListName = list.Name
	}
	return dto
}

// parseServerHost extracts scheme://host from a stored calendar URL so the
// settings form can show something the user recognises.
func parseServerHost(raw string) (string, error) {
	idx := strings.Index(raw, "://")
	if idx < 0 {
		return "", fmt.Errorf("invalid url")
	}
	rest := raw[idx+3:]
	if end := strings.IndexByte(rest, '/'); end >= 0 {
		return raw[:idx+3+end], nil
	}
	return raw, nil
}
