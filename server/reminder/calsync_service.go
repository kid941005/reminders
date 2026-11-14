package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"smallgo/server/logger"

	"gorm.io/gorm"
)

// CalendarSyncInput is the payload for configuring the DingTalk CalDAV import.
// Every field except the credentials is optional; omitted values keep whatever
// was stored before (or the model defaults on first save).
type CalendarSyncInput struct {
	Server              string `json:"server"`
	Username            string `json:"username"`
	Password            string `json:"password"`
	Enabled             *bool  `json:"enabled"`
	WindowBackDays      *int   `json:"window_back_days"`
	WindowForwardDays   *int   `json:"window_forward_days"`
	RemindBeforeMinutes *int   `json:"remind_before_minutes"`
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
	collections, err := client.Discover(ctx, in.Server)
	if err != nil {
		return CalendarSync{}, err
	}
	if len(collections) == 0 {
		return CalendarSync{}, errors.New("没有找到可读取的日历")
	}
	rawCollections, err := json.Marshal(collections)
	if err != nil {
		return CalendarSync{}, errors.New("日历列表处理失败")
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
	cfg.CalendarURL = collections[0]
	cfg.Collections = string(rawCollections)
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
		"last_sync_at":  time.Now().UTC(),
		"last_imported": imported,
		"last_status":   "ok",
		"last_error":    "",
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

	collections := cfg.collectionURLs()
	if len(collections) == 0 {
		return 0, errors.New("尚未记录日历地址，请重新保存 CalDAV 配置")
	}

	client := newCalDAVClient(cfg.Username, password)
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 逐个日历集合拉取。某个集合失败（订阅日历、只读日历常常不支持 calendar-query）
	// 时继续读其余的，但本轮**跳过清理**：少读一个集合就把它的日程当成「远端已
	// 删除」删掉，是最容易造成「日程缺漏」的路径。
	var events []ICalEvent
	var fetchErrs []string
	for _, collection := range collections {
		fetched, err := client.FetchEvents(ctx, collection, from, to)
		if err != nil {
			fetchErrs = append(fetchErrs, fmt.Sprintf("%s: %v", collection, err))
			continue
		}
		events = append(events, fetched...)
	}
	if len(fetchErrs) == len(collections) {
		return 0, fmt.Errorf("读取日历失败: %s", strings.Join(fetchErrs, "; "))
	}
	allRead := len(fetchErrs) == 0

	var imported, pruned int
	err = db.Transaction(func(tx *gorm.DB) error {
		var applyErr error
		imported, pruned, applyErr = applySyncEvents(tx, cfg, events, from, to, allRead)
		return applyErr
	})
	if err != nil {
		return imported, err
	}
	if len(fetchErrs) > 0 {
		return imported, fmt.Errorf("部分日历读取失败（本轮未清理）: %s", strings.Join(fetchErrs, "; "))
	}
	// 排障用：把「拉了多少、导入了多少、清理了多少」写进运行日志，
	// 出现缺漏时可以直接判断是没拉到、还是被清理了。
	logger.Info("reminder: 钉钉日程同步 user=%d 集合=%d 拉取=%d 导入=%d 清理=%d",
		cfg.UserID, len(collections), len(events), imported, pruned)
	return imported, nil
}

// applySyncEvents 把一轮拉取的 VEVENT 落到本地提醒表，返回导入条数与清理条数。
//
// 单独抽出来是为了能在不联网的情况下覆盖这些容易出错的分支：多日历合并、
// 循环日程的起始时间推进、例外实例的独立身份、以及「之前被清理过的条目复活」。
// prune=false 表示本轮存在读取失败的集合，不能据此判断「远端已删除」。
func applySyncEvents(tx *gorm.DB, cfg CalendarSync, events []ICalEvent, from, to time.Time, prune bool) (int, int, error) {
	now := time.Now().UTC()
	loc := shanghai()
	// 全天日程按「本地今天零点」判断是否过期；定时日程按现在判断。
	localNow := now.In(loc)
	allDayFloor := time.Date(localNow.Year(), localNow.Month(), localNow.Day(), 0, 0, 0, 0, loc).UTC()

	// CalDAV 会把循环日程拆成「主事件 + 每个被改过的实例」。实例与主事件共享
	// UID，但带 RECURRENCE-ID；如果实例只是改了地点/描述、时间没变，再存一条
	// 独立提醒就会让同一个时间点出现两条待办、发两次通知。这里先把这种「同时间
	// 例外」识别出来，合并到主事件里，而不是各自成行。
	masters := make(map[string]ICalEvent, len(events))
	for _, event := range events {
		uid := strings.TrimSpace(event.UID)
		if uid == "" || event.Status == "CANCELLED" || strings.TrimSpace(event.RecurrenceID) != "" {
			continue
		}
		masters[uid] = event
	}
	skipExceptions := make(map[string]bool)
	mergedNotes := make(map[string]string)
	for _, event := range events {
		uid := strings.TrimSpace(event.UID)
		if uid == "" || strings.TrimSpace(event.RecurrenceID) == "" || event.Status == "CANCELLED" {
			continue
		}
		master, ok := masters[uid]
		if !ok {
			continue
		}
		masterDue := rollForward(syncEventDue(master, loc), master.RepeatRule, syncEventFloor(master, now, allDayFloor))
		if !syncOccurrenceContains(masterDue, master.RepeatRule, syncEventDue(event, loc), to) {
			continue
		}
		skipExceptions[eventKey(event, uid)] = true
		if notes := composeSyncNotes(event); notes != "" {
			mergedNotes[uid] = notes
		}
	}

	seen := make(map[string]bool, len(events))
	imported := 0

	for _, event := range events {
		uid := strings.TrimSpace(event.UID)
		// 没有 UID 就没有去重依据：重复导入会不断产生副本。
		if uid == "" || event.Status == "CANCELLED" {
			continue
		}
		key := eventKey(event, uid)
		if skipExceptions[key] {
			// 旧版本可能已经把这条同时间例外存成了独立提醒；这里顺手清掉，
			// 但保留用户已经完成的记录，避免删除历史。
			if err := tx.Model(&Reminder{}).
				Where("user_id = ? AND external_source = ? AND external_uid = ? AND completed_at IS NULL",
					cfg.UserID, ExternalSourceDingTalk, key).
				Update("deleted_at", now).Error; err != nil {
				return imported, 0, err
			}
			continue
		}
		seen[key] = true

		title := strings.TrimSpace(event.Summary)
		if title == "" {
			title = "(无标题日程)"
		}
		title = truncateRunes(title, 200)
		// 与手工创建的提醒一样按 UTC 入库（见 service.go 的入库归一化）。
		due := syncEventDue(event, loc)

		rule := event.RepeatRule
		if strings.TrimSpace(event.RecurrenceID) != "" {
			// 例外实例是一次性的，不能继承主事件的重复规则。
			rule = "none"
		}
		// CalDAV 只返回主事件、不展开实例，主事件的 DTSTART 可能停在很久以前。
		// 直接落库会让这条提醒卡在过去，而「接下来的会议」在列表里完全看不到。
		due = rollForward(due, rule, syncEventFloor(event, now, allDayFloor))

		notes := composeSyncNotes(event)
		if merged := mergedNotes[uid]; merged != "" && strings.TrimSpace(notes) == "" {
			notes = merged
		}

		var existing Reminder
		findErr := tx.Where("user_id = ? AND external_source = ? AND external_uid = ?",
			cfg.UserID, ExternalSourceDingTalk, key).First(&existing).Error

		switch {
		case errors.Is(findErr, gorm.ErrRecordNotFound):
			created := Reminder{
				UserID: cfg.UserID, ListID: cfg.ListID, Title: title, Notes: notes,
				DueAt: &due, AllDay: event.AllDay, RepeatRule: rule,
				Calendar: "solar", RemindBeforeMinutes: cfg.RemindBeforeMinutes,
				ExternalSource: ExternalSourceDingTalk, ExternalUID: key, Version: 1,
			}
			if err := tx.Create(&created).Error; err != nil {
				return imported, 0, err
			}
			// 导入日程默认不推送：用户通常已经在日程来源里收到过提醒，
			// 站内消息需要用户在该条提醒里主动开启。
			channels := []string{}
			if err := replaceChannels(tx, created, SaveReminderInput{Channels: channels}); err != nil {
				return imported, 0, err
			}
			if err := rebuildJobs(tx, created, channels); err != nil {
				return imported, 0, err
			}
			imported++
		case findErr != nil:
			return imported, 0, findErr
		default:
			// 已完成条目不覆盖：用户在本地处理过的日程不应该被远端重新激活。
			if existing.CompletedAt != nil {
				continue
			}
			// 之前被清理过的条目必须复活：只更新字段而不清 deleted_at，这条记录
			// 就永远回不来了（某次同步只拿到部分数据时确实发生过）。
			existing.DeletedAt = nil
			existing.Title = title
			existing.Notes = notes
			existing.DueAt = &due
			existing.AllDay = event.AllDay
			existing.RepeatRule = rule
			existing.SnoozedUntil = nil
			existing.Version++
			if err := tx.Save(&existing).Error; err != nil {
				return imported, 0, err
			}
			channels := enabledChannelsOf(tx, existing)
			if err := rebuildJobs(tx, existing, channels); err != nil {
				return imported, 0, err
			}
			imported++
		}
	}

	// 清理：只处理「本次窗口内、未完成、本轮没看到」的条目。加窗口条件后，
	// 窗口外或已被推进到窗口之后的日程不会被误删；本轮完全空响应时整体跳过
	// （例如凭据失效返回空集合，绝不能据此清空本地）。
	if len(seen) == 0 || !prune {
		return imported, 0, nil
	}
	keys := make([]string, 0, len(seen))
	for key := range seen {
		keys = append(keys, key)
	}
	res := tx.Model(&Reminder{}).
		Where("user_id = ? AND external_source = ? AND completed_at IS NULL AND deleted_at IS NULL",
			cfg.UserID, ExternalSourceDingTalk).
		Where("due_at IS NOT NULL AND due_at >= ? AND due_at < ?", from.UTC(), to.UTC()).
		Where("external_uid NOT IN ?", keys).
		Update("deleted_at", now)
	if res.Error != nil {
		return imported, 0, res.Error
	}
	return imported, int(res.RowsAffected), nil
}

// eventKey 生成去重键：普通事件用 UID；被单独改期的循环实例（RECURRENCE-ID）
// 用「UID#实例时间」，否则同一 UID 的多个实例会互相覆盖、只剩一条。
func eventKey(event ICalEvent, uid string) string {
	if rid := strings.TrimSpace(event.RecurrenceID); rid != "" {
		return uid + "#" + rid
	}
	return uid
}

// syncEventDue 把 VEVENT 的 DTSTART 归一成入库用的 UTC 时刻；全天日程统一
// 落到本地零点，和手工创建的全天提醒保持一致。
func syncEventDue(event ICalEvent, loc *time.Location) time.Time {
	due := event.Start.In(loc)
	if event.AllDay {
		due = time.Date(due.Year(), due.Month(), due.Day(), 0, 0, 0, 0, loc)
	}
	return due.UTC()
}

// syncEventFloor 返回循环推进的下界：定时日程按当前时刻，全天日程按本地今天
// 零点，这样「今天的全天日程」不会因为已经过了 00:00 就被推到下周。
func syncEventFloor(event ICalEvent, now, allDayFloor time.Time) time.Time {
	if event.AllDay {
		return allDayFloor
	}
	return now
}

// syncOccurrenceContains 判断 target 是否是「从 due 开始、limit 之前」的某个
// 循环实例。只比较下一次实例不够：DingTalk 可能为未来某一周的实例单独返回
// RECURRENCE-ID，如果那时才去重，用户会在那一天重新收到两条提醒。
func syncOccurrenceContains(due time.Time, rule string, target, limit time.Time) bool {
	if rule == "" || rule == "none" {
		return due.Equal(target)
	}
	if target.Before(due) {
		return false
	}
	for i := 0; i < 500; i++ {
		if due.Equal(target) {
			return true
		}
		if due.After(target) || due.After(limit) {
			return false
		}
		next := nextOccurrence(due, rule)
		if !next.After(due) {
			return false
		}
		due = next
	}
	return false
}

// rollForward 把循环日程的起始时间推进到 floor 之后。CalDAV 返回的是主事件，
// 不展开实例；主事件的 DTSTART 可能是几年前，直接落库会让提醒卡在过去。
func rollForward(due time.Time, rule string, floor time.Time) time.Time {
	if rule == "" || rule == "none" {
		return due
	}
	for i := 0; i < 500 && due.Before(floor); i++ {
		next := nextOccurrence(due, rule)
		if !next.After(due) {
			break
		}
		due = next
	}
	return due
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

// enabledChannelsOf reads the channels currently linked to a reminder.
// Imported rows start with none: the source calendar already notifies the user,
// so in-app delivery is opt-in per reminder. A read failure also returns no
// channels rather than silently resurrecting the old in-app default.
func enabledChannelsOf(tx *gorm.DB, item Reminder) []string {
	var rows []ReminderChannel
	if err := tx.Where("reminder_id = ? AND user_id = ? AND enabled = ?", item.ID, item.UserID, true).Find(&rows).Error; err != nil {
		return nil
	}
	channels := make([]string, 0, len(rows))
	for _, row := range rows {
		channels = append(channels, row.Channel)
	}
	return channels
}

// CalendarSyncDTO is what the settings page reads: the sealed password never
// leaves the server.
type CalendarSyncDTO struct {
	CalendarSync
	Server   string `json:"server"`
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
