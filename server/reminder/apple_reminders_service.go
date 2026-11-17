package reminder

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"smallgo/server/logger"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

var appleReminderUserLocks sync.Map

func appleReminderUserLock(userID uint) *sync.Mutex {
	value, _ := appleReminderUserLocks.LoadOrStore(userID, &sync.Mutex{})
	return value.(*sync.Mutex)
}

// SaveAppleReminderSync validates credentials against iCloud, records the
// selected VTODO list, and re-queues every reminder whose remote copy might be
// stale (for example changes made while sync was disabled).
func SaveAppleReminderSync(db *gorm.DB, userID uint, in AppleReminderSyncInput) (AppleReminderSyncDTO, error) {
	var existing AppleReminderSync
	hasExisting := true
	if err := db.Where("user_id = ?", userID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			hasExisting = false
		} else {
			return AppleReminderSyncDTO{}, err
		}
	}

	serverInput := strings.TrimSpace(in.Server)
	if serverInput == "" && hasExisting {
		serverInput = existing.Server
	}
	canonicalServer, err := normalizeServerURL(appleServerOrDefault(serverInput))
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	server := strings.TrimSuffix(canonicalServer, "/")

	appleID := strings.TrimSpace(in.AppleID)
	if appleID == "" && hasExisting {
		appleID = existing.AppleID
	}
	if appleID == "" {
		return AppleReminderSyncDTO{}, errors.New("请填写 Apple ID")
	}

	plainPassword := strings.TrimSpace(in.Password)
	if plainPassword == "" {
		if !hasExisting {
			return AppleReminderSyncDTO{}, errors.New("请填写 App 专用密码")
		}
		plainPassword, err = decryptTarget(db, existing.Password)
		if err != nil {
			return AppleReminderSyncDTO{}, errors.New("已保存的 App 专用密码无法读取，请重新填写")
		}
	}

	collectionURL := strings.TrimSpace(in.CollectionURL)
	if collectionURL == "" && hasExisting {
		collectionURL = existing.CollectionURL
	}
	if collectionURL == "" {
		return AppleReminderSyncDTO{}, errors.New("请选择要同步的提醒事项列表")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	lists, err := newAppleCalDAVClient(appleID, plainPassword).DiscoverReminderCollections(ctx, server)
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	var matched *AppleReminderCollection
	for i := range lists {
		if strings.EqualFold(strings.TrimRight(lists[i].URL, "/"), strings.TrimRight(collectionURL, "/")) {
			matched = &lists[i]
			break
		}
	}
	if matched == nil {
		return AppleReminderSyncDTO{}, errors.New("选择的提醒事项列表不存在，请重新加载列表后再选择")
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	} else if hasExisting {
		enabled = existing.Enabled
	}

	encrypted, err := encryptTarget(db, plainPassword)
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	accountChanged := !hasExisting || !strings.EqualFold(existing.AppleID, appleID) ||
		!strings.EqualFold(strings.TrimRight(existing.Server, "/"), server)
	collectionChanged := !hasExisting || !strings.EqualFold(strings.TrimRight(existing.CollectionURL, "/"), strings.TrimRight(matched.URL, "/"))

	err = db.Transaction(func(tx *gorm.DB) error {
		cfg := AppleReminderSync{}
		if hasExisting {
			cfg = existing
		}
		cfg.UserID = userID
		cfg.Server = server
		cfg.AppleID = appleID
		cfg.Password = encrypted
		cfg.CollectionURL = matched.URL
		cfg.CollectionName = matched.Name
		cfg.Enabled = enabled
		cfg.LastError = ""
		if accountChanged || collectionChanged {
			cfg.LastStatus = ""
			cfg.LastSyncAt = nil
			cfg.LastPushed = 0
			if err := tx.Where("user_id = ?", userID).Delete(&AppleReminderLink{}).Error; err != nil {
				return err
			}
			if err := tx.Where("user_id = ?", userID).Delete(&AppleReminderOutbox{}).Error; err != nil {
				return err
			}
		}
		if err := tx.Save(&cfg).Error; err != nil {
			return err
		}
		// GORM applies the column default for a zero bool, so persist disabled
		// explicitly after the insert/update.
		if err := tx.Model(&cfg).Update("enabled", enabled).Error; err != nil {
			return err
		}
		if !enabled {
			return tx.Where("user_id = ?", userID).Delete(&AppleReminderOutbox{}).Error
		}
		return enqueueAppleReminderBackfill(tx, userID)
	})
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	return getAppleReminderSyncDTO(db, userID)
}

// DiscoverAppleReminderLists lists VTODO-capable iCloud lists without saving.
func DiscoverAppleReminderLists(db *gorm.DB, userID uint, in AppleReminderDiscoverInput) ([]AppleReminderCollection, error) {
	var existing AppleReminderSync
	hasExisting := true
	if err := db.Where("user_id = ?", userID).First(&existing).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			hasExisting = false
		} else {
			return nil, err
		}
	}
	server := strings.TrimSpace(in.Server)
	if server == "" && hasExisting {
		server = existing.Server
	}
	appleID := strings.TrimSpace(in.AppleID)
	if appleID == "" && hasExisting {
		appleID = existing.AppleID
	}
	if appleID == "" {
		return nil, errors.New("请填写 Apple ID")
	}
	plainPassword := strings.TrimSpace(in.Password)
	if plainPassword == "" {
		if !hasExisting {
			return nil, errors.New("请填写 App 专用密码")
		}
		var err error
		plainPassword, err = decryptTarget(db, existing.Password)
		if err != nil {
			return nil, errors.New("已保存的 App 专用密码无法读取，请重新填写")
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return newAppleCalDAVClient(appleID, plainPassword).DiscoverReminderCollections(ctx, server)
}

// DeleteAppleReminderSync removes the configuration and any queued intents but
// keeps the local↔remote links so reconnecting the same account does not
// create duplicate reminders.
func DeleteAppleReminderSync(db *gorm.DB, userID uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("user_id = ?", userID).Delete(&AppleReminderOutbox{}).Error; err != nil {
			return err
		}
		if err := tx.Where("user_id = ?", userID).Delete(&AppleReminderSync{}).Error; err != nil {
			return err
		}
		return nil
	})
}

// RunAppleReminderSync processes the queue immediately, retrying previously
// failed rows once the user explicitly asks for a sync.
func RunAppleReminderSync(db *gorm.DB, userID uint) (AppleReminderSyncResult, error) {
	var cfg AppleReminderSync
	if err := db.Where("user_id = ?", userID).First(&cfg).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return AppleReminderSyncResult{}, errors.New("尚未配置 Apple 提醒事项同步")
		}
		return AppleReminderSyncResult{}, err
	}
	if !cfg.Enabled {
		return AppleReminderSyncResult{}, errors.New("Apple 提醒事项同步已关闭")
	}
	if err := resetAppleReminderFailures(db, userID); err != nil {
		return AppleReminderSyncResult{}, err
	}
	lock := appleReminderUserLock(userID)
	lock.Lock()
	pushed, failed, processErr := processAppleReminderOutbox(db, &cfg)
	lock.Unlock()
	pending, _ := countPendingAppleReminders(db, userID)
	result := AppleReminderSyncResult{Pushed: pushed, Failed: failed, Pending: pending}
	if processErr != nil {
		result.Error = processErr.Error()
	}
	return result, processErr
}

func runDueAppleReminderSyncs(db *gorm.DB) {
	var configs []AppleReminderSync
	if err := db.Where("enabled = ?", true).Find(&configs).Error; err != nil {
		logger.Error("reminder: 读取 Apple 提醒事项配置失败: %v", err)
		return
	}
	now := time.Now().UTC()
	for i := range configs {
		cfg := &configs[i]
		lock := appleReminderUserLock(cfg.UserID)
		lock.Lock()
		due, pushed, failed, err := processDueAppleReminderQueue(db, cfg, now)
		lock.Unlock()
		if err != nil {
			logger.Error("reminder: Apple 提醒事项同步失败 user=%d: %v", cfg.UserID, err)
			continue
		}
		if due > 0 {
			logger.Info("reminder: Apple 提醒事项同步 user=%d 推送=%d 失败=%d", cfg.UserID, pushed, failed)
		}
	}
}

// processDueAppleReminderQueue drains one user's due queue while the caller
// holds that user's lock, so a manual run and the scheduler cannot process the
// same outbox rows concurrently.
func processDueAppleReminderQueue(db *gorm.DB, cfg *AppleReminderSync, now time.Time) (int64, int, int, error) {
	var due int64
	if err := db.Model(&AppleReminderOutbox{}).
		Where("user_id = ? AND status IN ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)", cfg.UserID, []string{appleReminderStatusPending, appleReminderStatusRetry}, now).
		Count(&due).Error; err != nil {
		return 0, 0, 0, err
	}
	if due == 0 {
		return 0, 0, 0, nil
	}
	pushed, failed, err := processAppleReminderOutbox(db, cfg)
	return due, pushed, failed, err
}

// processAppleReminderOutbox drains the due queue for one configuration.
func processAppleReminderOutbox(db *gorm.DB, cfg *AppleReminderSync) (int, int, error) {
	plainPassword, err := decryptTarget(db, cfg.Password)
	if err != nil {
		markAppleReminderConfig(db, cfg, 0, 0, errors.New("Apple 应用专用密码无法读取，请重新保存配置"))
		return 0, 0, errors.New("Apple 应用专用密码无法读取，请重新保存配置")
	}
	client := newAppleCalDAVClient(cfg.AppleID, plainPassword)
	now := time.Now().UTC()
	var rows []AppleReminderOutbox
	if err := db.Where("user_id = ? AND status IN ? AND (next_attempt_at IS NULL OR next_attempt_at <= ?)",
		cfg.UserID, []string{appleReminderStatusPending, appleReminderStatusRetry}, now).
		Order("id ASC").Limit(appleReminderBatchSize).Find(&rows).Error; err != nil {
		return 0, 0, err
	}
	if len(rows) == 0 {
		return 0, 0, nil
	}

	pushed, failed := 0, 0
	var firstErr error
	for _, row := range rows {
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		pushErr := pushAppleReminderOutboxRow(ctx, db, client, *cfg, row)
		cancel()
		if pushErr == nil {
			pushed++
			continue
		}
		failed++
		if firstErr == nil {
			firstErr = pushErr
		}
		authFailure := errors.Is(pushErr, errAppleAuth)
		recordAppleReminderFailure(db, row, pushErr, authFailure)
		if authFailure {
			break
		}
	}
	markAppleReminderConfig(db, cfg, pushed, failed, firstErr)
	return pushed, failed, nil
}

func pushAppleReminderOutboxRow(ctx context.Context, db *gorm.DB, client *caldavClient, cfg AppleReminderSync, row AppleReminderOutbox) error {
	var item Reminder
	err := db.Unscoped().Where("id = ? AND user_id = ?", row.ReminderID, cfg.UserID).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if row.Action == appleReminderActionDelete {
				if err := deleteAppleReminderRemote(ctx, db, client, cfg, row); err != nil {
					return err
				}
			}
			return deleteAppleReminderOutboxIfCurrent(db, row)
		}
		return err
	}
	if row.Action == appleReminderActionDelete || item.DeletedAt != nil {
		if err := deleteAppleReminderRemote(ctx, db, client, cfg, row); err != nil {
			return err
		}
		return deleteAppleReminderOutboxIfCurrent(db, row)
	}
	if strings.TrimSpace(item.ExternalSource) != "" {
		return deleteAppleReminderOutboxIfCurrent(db, row)
	}
	if err := upsertAppleReminderRemote(ctx, db, client, cfg, item); err != nil {
		return err
	}
	return deleteAppleReminderOutboxIfCurrent(db, row)
}

func upsertAppleReminderRemote(ctx context.Context, db *gorm.DB, client *caldavClient, cfg AppleReminderSync, item Reminder) error {
	var link AppleReminderLink
	hasLink := true
	if err := db.Where("reminder_id = ? AND user_id = ?", item.ID, item.UserID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			hasLink = false
		} else {
			return err
		}
	}
	collection := cfg.CollectionURL
	if hasLink && strings.TrimSpace(link.CollectionURL) != "" {
		collection = link.CollectionURL
	}
	uid := appleReminderUID(item.UserID, item.ID)
	remoteURL := appleReminderObjectURL(collection, item.UserID, item.ID)
	if hasLink && strings.TrimSpace(link.RemoteURL) != "" {
		remoteURL = link.RemoteURL
	}
	payload := buildAppleReminderCalendar(item, uid, time.Now().UTC())

	etag := ""
	finalURL := remoteURL
	var err error
	if hasLink {
		etag, finalURL, err = client.PutReminderObject(ctx, remoteURL, payload, link.ETag, false)
		if errors.Is(err, errAppleRemotePrecondition) {
			fresh, getErr := client.GetReminderObjectETag(ctx, remoteURL)
			if getErr == nil {
				etag, finalURL, err = client.PutReminderObject(ctx, remoteURL, payload, fresh, false)
			} else if errors.Is(getErr, errAppleRemoteNotFound) {
				hasLink = false
				err = nil
			} else {
				return getErr
			}
		}
		if errors.Is(err, errAppleRemoteNotFound) {
			hasLink = false
			err = nil
		}
	}
	if !hasLink && err == nil {
		etag, finalURL, err = createAppleReminderRemote(ctx, client, remoteURL, payload)
	}
	if err != nil {
		return err
	}
	if strings.TrimSpace(finalURL) != "" {
		remoteURL = finalURL
	}
	return saveAppleReminderLink(db, item, uid, remoteURL, collection, etag)
}

func createAppleReminderRemote(ctx context.Context, client *caldavClient, remoteURL, payload string) (string, string, error) {
	etag, finalURL, err := client.PutReminderObject(ctx, remoteURL, payload, "", true)
	if errors.Is(err, errAppleRemotePrecondition) {
		fresh, getErr := client.GetReminderObjectETag(ctx, remoteURL)
		if getErr != nil {
			return "", remoteURL, getErr
		}
		return client.PutReminderObject(ctx, remoteURL, payload, fresh, false)
	}
	if err != nil {
		return "", remoteURL, err
	}
	return etag, finalURL, nil
}

func deleteAppleReminderRemote(ctx context.Context, db *gorm.DB, client *caldavClient, cfg AppleReminderSync, row AppleReminderOutbox) error {
	var link AppleReminderLink
	if err := db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	remoteURL := strings.TrimSpace(link.RemoteURL)
	if remoteURL == "" {
		collection := strings.TrimSpace(link.CollectionURL)
		if collection == "" {
			collection = cfg.CollectionURL
		}
		remoteURL = appleReminderObjectURL(collection, cfg.UserID, row.ReminderID)
	}
	if remoteURL != "" {
		if err := client.DeleteReminderObject(ctx, remoteURL); err != nil {
			return err
		}
	}
	return db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).Delete(&AppleReminderLink{}).Error
}

func saveAppleReminderLink(db *gorm.DB, item Reminder, uid, remoteURL, collection, etag string) error {
	link := AppleReminderLink{
		UserID: item.UserID, ReminderID: item.ID, RemoteUID: uid,
		RemoteURL: remoteURL, ETag: etag, CollectionURL: collection,
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "reminder_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"user_id":        item.UserID,
			"remote_uid":     uid,
			"remote_url":     remoteURL,
			"e_tag":          etag,
			"collection_url": collection,
			"updated_at":     time.Now().UTC(),
		}),
	}).Create(&link).Error
}

func deleteAppleReminderOutboxIfCurrent(db *gorm.DB, row AppleReminderOutbox) error {
	return db.Where("id = ? AND updated_at = ?", row.ID, row.UpdatedAt).Delete(&AppleReminderOutbox{}).Error
}

func recordAppleReminderFailure(db *gorm.DB, row AppleReminderOutbox, pushErr error, authFailure bool) {
	attempts := row.AttemptCount + 1
	status := appleReminderStatusRetry
	var next *time.Time
	if authFailure || attempts >= appleReminderMaxAttempts {
		status = appleReminderStatusError
	} else {
		at := time.Now().UTC().Add(appleReminderBackoff(attempts))
		next = &at
	}
	_ = db.Model(&AppleReminderOutbox{}).Where("id = ? AND updated_at = ?", row.ID, row.UpdatedAt).Updates(map[string]interface{}{
		"status":          status,
		"attempt_count":   attempts,
		"next_attempt_at": next,
		"last_error":      truncateRunes(pushErr.Error(), 512),
		"updated_at":      time.Now().UTC(),
	}).Error
}

func appleReminderBackoff(attempt int) time.Duration {
	switch {
	case attempt <= 1:
		return time.Minute
	case attempt == 2:
		return 5 * time.Minute
	case attempt == 3:
		return 30 * time.Minute
	default:
		return 2 * time.Hour
	}
}

func resetAppleReminderFailures(db *gorm.DB, userID uint) error {
	return db.Model(&AppleReminderOutbox{}).
		Where("user_id = ? AND status IN ?", userID, []string{appleReminderStatusRetry, appleReminderStatusError}).
		Updates(map[string]interface{}{
			"status": appleReminderStatusPending, "attempt_count": 0,
			"next_attempt_at": nil, "last_error": "", "updated_at": time.Now().UTC(),
		}).Error
}

func markAppleReminderConfig(db *gorm.DB, cfg *AppleReminderSync, pushed, failed int, firstErr error) {
	status := "ok"
	message := ""
	if failed > 0 && firstErr != nil {
		status = "error"
		message = truncateRunes(firstErr.Error(), 512)
	}
	now := time.Now().UTC()
	_ = db.Model(&AppleReminderSync{}).Where("id = ?", cfg.ID).Updates(map[string]interface{}{
		"last_sync_at": now, "last_status": status, "last_error": message,
		"last_pushed": pushed, "updated_at": now,
	}).Error
	cfg.LastSyncAt = &now
	cfg.LastStatus = status
	cfg.LastError = message
	cfg.LastPushed = pushed
}

func countPendingAppleReminders(db *gorm.DB, userID uint) (int64, error) {
	var count int64
	err := db.Model(&AppleReminderOutbox{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func getAppleReminderSyncDTO(db *gorm.DB, userID uint) (AppleReminderSyncDTO, error) {
	var cfg AppleReminderSync
	if err := db.Where("user_id = ?", userID).First(&cfg).Error; err != nil {
		return AppleReminderSyncDTO{}, err
	}
	dto := AppleReminderSyncDTO{AppleReminderSync: cfg}
	dto.PendingCount, _ = countPendingAppleReminders(db, userID)
	_ = db.Model(&AppleReminderLink{}).Where("user_id = ?", userID).Count(&dto.LinkedCount).Error
	return dto, nil
}

// enqueueAppleSync records the latest intent for one local reminder. A newer
// change replaces the previous queued action (including delete over upsert).
func enqueueAppleSync(tx *gorm.DB, userID, reminderID uint, action string) error {
	var count int64
	if err := tx.Model(&AppleReminderSync{}).Where("user_id = ? AND enabled = ?", userID, true).Count(&count).Error; err != nil {
		return err
	}
	if count == 0 {
		return nil
	}
	var item Reminder
	if err := tx.Unscoped().Select("id", "user_id", "external_source").
		Where("id = ? AND user_id = ?", reminderID, userID).First(&item).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	if strings.TrimSpace(item.ExternalSource) != "" {
		return nil
	}
	return enqueueAppleSyncRow(tx, userID, reminderID, action)
}

func enqueueAppleSyncRow(tx *gorm.DB, userID, reminderID uint, action string) error {
	now := time.Now().UTC()
	row := AppleReminderOutbox{
		UserID: userID, ReminderID: reminderID, Action: action,
		Status: appleReminderStatusPending,
	}
	return tx.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "reminder_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"user_id": userID, "action": action, "status": appleReminderStatusPending,
			"attempt_count": 0, "next_attempt_at": nil, "last_error": "",
			"updated_at": now,
		}),
	}).Create(&row).Error
}

// enqueueAppleReminderBackfill queues all open local reminders plus linked ones
// (so a completion made while sync was disabled still reaches iCloud).
func enqueueAppleReminderBackfill(tx *gorm.DB, userID uint) error {
	linked := tx.Model(&AppleReminderLink{}).Select("reminder_id").Where("user_id = ?", userID)
	var ids []uint
	if err := tx.Model(&Reminder{}).
		Where("user_id = ? AND deleted_at IS NULL", userID).
		Where("COALESCE(external_source, '') = ''").
		Where("completed_at IS NULL OR id IN (?)", linked).
		Pluck("id", &ids).Error; err != nil {
		return err
	}
	for _, id := range ids {
		if err := enqueueAppleSyncRow(tx, userID, id, appleReminderActionUpsert); err != nil {
			return err
		}
	}
	// A reminder deleted while sync was disabled still has a remote copy. Queue
	// deletes for linked rows whose local reminder is gone or no longer eligible.
	var links []AppleReminderLink
	if err := tx.Where("user_id = ?", userID).Find(&links).Error; err != nil {
		return err
	}
	for _, link := range links {
		var item Reminder
		err := tx.Unscoped().Select("id", "deleted_at", "external_source").
			Where("id = ? AND user_id = ?", link.ReminderID, userID).First(&item).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if err := enqueueAppleSyncRow(tx, userID, link.ReminderID, appleReminderActionDelete); err != nil {
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if item.DeletedAt != nil || strings.TrimSpace(item.ExternalSource) != "" {
			if err := enqueueAppleSyncRow(tx, userID, link.ReminderID, appleReminderActionDelete); err != nil {
				return err
			}
		}
	}
	return nil
}

func appleReminderUID(userID, reminderID uint) string {
	return fmt.Sprintf("smallgo-reminder-%d-%d@%s", userID, reminderID, appleReminderUIDDomain)
}

func appleReminderFileName(userID, reminderID uint) string {
	return fmt.Sprintf("smallgo-reminder-%d-%d.ics", userID, reminderID)
}

func appleReminderObjectURL(collection string, userID, reminderID uint) string {
	collection = strings.TrimRight(strings.TrimSpace(collection), "/")
	if collection == "" {
		return ""
	}
	return collection + "/" + appleReminderFileName(userID, reminderID)
}

// buildAppleReminderCalendar renders a VTODO. Lunar recurrence deliberately
// omits RRULE: VTODO RRULE is Gregorian, so emitting it for a lunar rule would
// create wrong occurrences in Apple Reminders.
func buildAppleReminderCalendar(item Reminder, uid string, now time.Time) string {
	now = now.UTC()
	lines := []string{
		"BEGIN:VCALENDAR",
		"VERSION:2.0",
		"PRODID:" + appleReminderCalendarProdID,
		"CALSCALE:GREGORIAN",
		"BEGIN:VTODO",
		"UID:" + uid,
		"DTSTAMP:" + now.Format("20060102T150405Z"),
	}
	lines = append(lines, "SUMMARY:"+escapeICalText(item.Title))
	if strings.TrimSpace(item.Notes) != "" {
		lines = append(lines, "DESCRIPTION:"+escapeICalText(item.Notes))
	}
	if item.DueAt != nil {
		if item.AllDay {
			lines = append(lines, "DUE;VALUE=DATE:"+item.DueAt.In(shanghai()).Format("20060102"))
		} else {
			lines = append(lines, "DUE:"+item.DueAt.UTC().Format("20060102T150405Z"))
		}
	}
	lines = append(lines, fmt.Sprintf("PRIORITY:%d", appleReminderPriority(item.Priority)))
	if item.CompletedAt != nil {
		lines = append(lines, "STATUS:COMPLETED")
		lines = append(lines, "COMPLETED:"+item.CompletedAt.UTC().Format("20060102T150405Z"))
		lines = append(lines, "PERCENT-COMPLETE:100")
	} else {
		lines = append(lines, "STATUS:NEEDS-ACTION")
	}
	if rule := appleReminderRRule(item); rule != "" {
		lines = append(lines, "RRULE:"+rule)
	}
	sequence := item.Version - 1
	if sequence < 0 {
		sequence = 0
	}
	lines = append(lines,
		fmt.Sprintf("SEQUENCE:%d", sequence),
		"LAST-MODIFIED:"+item.UpdatedAt.UTC().Format("20060102T150405Z"),
		"CREATED:"+item.CreatedAt.UTC().Format("20060102T150405Z"),
		"END:VTODO",
		"END:VCALENDAR",
	)
	return foldICalLines(strings.Join(lines, "\n"))
}

func appleReminderPriority(priority int) int {
	switch priority {
	case 1:
		return 9
	case 2:
		return 5
	case 3:
		return 1
	default:
		return 0
	}
}

func appleReminderRRule(item Reminder) string {
	if item.RepeatRule == "" || item.RepeatRule == "none" || strings.EqualFold(item.Calendar, "lunar") {
		return ""
	}
	switch item.RepeatRule {
	case "daily":
		return "FREQ=DAILY"
	case "weekly":
		return "FREQ=WEEKLY"
	case "monthly":
		return "FREQ=MONTHLY"
	case "yearly":
		return "FREQ=YEARLY"
	default:
		return ""
	}
}

func escapeICalText(value string) string {
	replacer := strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
	)
	return replacer.Replace(value)
}

// foldICalLines emits CRLF endings and folds at 75 octets without splitting a
// UTF-8 sequence, as required by RFC 5545.
func foldICalLines(content string) string {
	lines := strings.Split(strings.ReplaceAll(content, "\r\n", "\n"), "\n")
	var out strings.Builder
	for _, line := range lines {
		out.WriteString(foldICalLine(line))
		out.WriteString("\r\n")
	}
	return out.String()
}

func foldICalLine(line string) string {
	if len(line) <= 75 {
		return line
	}
	var out strings.Builder
	remaining := line
	first := true
	for len(remaining) > 0 {
		limit := 75
		if !first {
			out.WriteString("\r\n ")
			limit = 74 // continuation lines also carry the leading space
		}
		if len(remaining) <= limit {
			out.WriteString(remaining)
			break
		}
		cut := limit
		for cut > 0 && !utf8.RuneStart(remaining[cut]) {
			cut--
		}
		if cut == 0 {
			cut = limit
		}
		out.WriteString(remaining[:cut])
		remaining = remaining[cut:]
		first = false
	}
	return out.String()
}
