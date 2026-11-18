package reminder

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func appleReminderBackendOf(cfg *AppleReminderSync) string {
	if cfg == nil {
		return appleReminderBackendCalDAV
	}
	switch strings.ToLower(strings.TrimSpace(cfg.Backend)) {
	case appleReminderBackendCloudKit:
		return appleReminderBackendCloudKit
	default:
		return appleReminderBackendCalDAV
	}
}

func appleCloudKitSessionFromConfig(db *gorm.DB, cfg *AppleReminderSync) (*appleCloudKitSession, error) {
	if cfg == nil || strings.TrimSpace(cfg.SessionData) == "" {
		return nil, errors.New("CloudKit 登录已失效，请重新登录 iCloud")
	}
	plain, err := decryptTarget(db, cfg.SessionData)
	if err != nil {
		return nil, errors.New("CloudKit 登录信息无法读取，请重新登录 iCloud")
	}
	var sess appleCloudKitSession
	if err := json.Unmarshal([]byte(plain), &sess); err != nil {
		return nil, errors.New("CloudKit 登录信息格式异常，请重新登录 iCloud")
	}
	return &sess, nil
}

func saveAppleCloudKitSync(db *gorm.DB, userID uint, in AppleReminderSyncInput, existing AppleReminderSync, hasExisting bool) (AppleReminderSyncDTO, error) {
	appleID := strings.TrimSpace(in.AppleID)
	if appleID == "" && hasExisting {
		appleID = existing.AppleID
	}
	if appleID == "" {
		return AppleReminderSyncDTO{}, errors.New("请填写 Apple ID")
	}

	var sess *appleCloudKitSession
	var err error
	if strings.TrimSpace(in.LoginID) != "" {
		sess, err = takeAppleCloudKitSession(in.LoginID)
		if err != nil {
			return AppleReminderSyncDTO{}, err
		}
	} else {
		if !hasExisting || appleReminderBackendOf(&existing) != appleReminderBackendCloudKit {
			return AppleReminderSyncDTO{}, errors.New("请先登录 iCloud 并选择提醒事项列表")
		}
		if !strings.EqualFold(existing.AppleID, appleID) {
			return AppleReminderSyncDTO{}, errors.New("更换 Apple ID 后需要重新登录 iCloud")
		}
		sess, err = appleCloudKitSessionFromConfig(db, &existing)
		if err != nil {
			return AppleReminderSyncDTO{}, err
		}
	}

	if strings.TrimSpace(sess.AppleID) != "" && !strings.EqualFold(sess.AppleID, appleID) {
		return AppleReminderSyncDTO{}, errors.New("登录的 Apple ID 与填写的账号不一致，请重新登录")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	client, err := newAppleCloudKitClientFromSession(sess)
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	lists, err := client.listReminderLists(ctx)
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	listID := strings.TrimSpace(in.CollectionURL)
	if listID == "" && hasExisting && appleReminderBackendOf(&existing) == appleReminderBackendCloudKit {
		listID = strings.TrimSpace(existing.CollectionURL)
	}
	var matched *AppleReminderCollection
	for i := range lists {
		if listID != "" && strings.EqualFold(lists[i].URL, listID) {
			matched = &lists[i]
			break
		}
	}
	if matched == nil {
		if listID == "" && len(lists) == 1 {
			matched = &lists[0]
		} else if listID == "" {
			return AppleReminderSyncDTO{}, errors.New("请选择要同步的提醒事项列表")
		} else {
			return AppleReminderSyncDTO{}, errors.New("选择的提醒事项列表不存在，请重新登录后再选择")
		}
	}

	enabled := true
	if in.Enabled != nil {
		enabled = *in.Enabled
	} else if hasExisting {
		enabled = existing.Enabled
	}

	sessionJSON, err := json.Marshal(sess)
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	encryptedSession, err := encryptTarget(db, string(sessionJSON))
	if err != nil {
		return AppleReminderSyncDTO{}, err
	}
	accountChanged := !hasExisting || !strings.EqualFold(existing.AppleID, appleID) ||
		appleReminderBackendOf(&existing) != appleReminderBackendCloudKit
	collectionChanged := !hasExisting || !strings.EqualFold(existing.CollectionURL, matched.URL)

	err = db.Transaction(func(tx *gorm.DB) error {
		cfg := AppleReminderSync{}
		if hasExisting {
			cfg = existing
		}
		cfg.UserID = userID
		cfg.Backend = appleReminderBackendCloudKit
		cfg.Server = appleReminderBackendCloudKit
		cfg.AppleID = appleID
		cfg.Password = ""
		cfg.SessionData = encryptedSession
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

func discoverAppleCloudKitLists(db *gorm.DB, existing *AppleReminderSync) ([]AppleReminderCollection, error) {
	sess, err := appleCloudKitSessionFromConfig(db, existing)
	if err != nil {
		return nil, err
	}
	client, err := newAppleCloudKitClientFromSession(sess)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	return client.listReminderLists(ctx)
}

func processAppleCloudKitOutbox(db *gorm.DB, cfg AppleReminderSync) (int, int, error) {
	client, err := newAppleCloudKitClientFromConfig(db, &cfg)
	if err != nil {
		markAppleReminderConfig(db, &cfg, 0, 0, errors.New("CloudKit 登录已失效，请重新登录 iCloud"))
		return 0, 0, errors.New("CloudKit 登录已失效，请重新登录 iCloud")
	}
	now := time.Now()
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
		pushErr := pushAppleCloudKitOutboxRow(ctx, db, client, cfg, row)
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
	markAppleReminderConfig(db, &cfg, pushed, failed, firstErr)
	return pushed, failed, nil
}

func pushAppleCloudKitOutboxRow(ctx context.Context, db *gorm.DB, client *appleCloudKitClient, cfg AppleReminderSync, row AppleReminderOutbox) error {
	var item Reminder
	err := db.Unscoped().Where("id = ? AND user_id = ?", row.ReminderID, cfg.UserID).First(&item).Error
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			if row.Action == appleReminderActionDelete {
				if err := deleteAppleCloudKitRemote(ctx, db, client, cfg, row); err != nil {
					return err
				}
			}
			return deleteAppleReminderOutboxIfCurrent(db, row)
		}
		return err
	}
	if row.Action == appleReminderActionDelete || item.DeletedAt != nil {
		if err := deleteAppleCloudKitRemote(ctx, db, client, cfg, row); err != nil {
			return err
		}
		return deleteAppleReminderOutboxIfCurrent(db, row)
	}
	if strings.TrimSpace(item.ExternalSource) != "" {
		return deleteAppleReminderOutboxIfCurrent(db, row)
	}
	if err := upsertAppleCloudKitRemote(ctx, db, client, cfg, item); err != nil {
		return err
	}
	return deleteAppleReminderOutboxIfCurrent(db, row)
}

func appleCloudKitReminderFields(item Reminder, listID string) (map[string]interface{}, error) {
	title, err := ckEncodeTitle(item.Title)
	if err != nil {
		return nil, fmt.Errorf("编码提醒标题失败: %w", err)
	}
	completedValue := interface{}(0)
	if item.CompletedAt != nil {
		completedValue = true
	}
	fields := map[string]interface{}{
		"TitleDocument": map[string]interface{}{"value": title},
		"Completed":     map[string]interface{}{"value": completedValue},
		"List": map[string]interface{}{"value": map[string]interface{}{
			"recordName": listID,
			"action":     "NONE",
		}},
	}
	if notes := strings.TrimSpace(item.Notes); notes != "" {
		encoded, err := ckEncodeTitle(notes)
		if err != nil {
			return nil, fmt.Errorf("编码提醒备注失败: %w", err)
		}
		fields["NotesDocument"] = map[string]interface{}{"value": encoded}
	}
	if item.DueAt != nil {
		fields["DueDate"] = map[string]interface{}{"value": item.DueAt.UnixMilli()}
	}
	if priority := appleReminderPriority(item.Priority); priority != 0 {
		fields["Priority"] = map[string]interface{}{"value": priority}
	}
	if item.CompletedAt != nil {
		fields["CompletionDate"] = map[string]interface{}{"value": item.CompletedAt.UnixMilli()}
	}
	return fields, nil
}

func upsertAppleCloudKitRemote(ctx context.Context, db *gorm.DB, client *appleCloudKitClient, cfg AppleReminderSync, item Reminder) error {
	var link AppleReminderLink
	hasLink := true
	if err := db.Where("reminder_id = ? AND user_id = ?", item.ID, item.UserID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			hasLink = false
		} else {
			return err
		}
	}
	listID := strings.TrimSpace(cfg.CollectionURL)
	if hasLink && strings.TrimSpace(link.CollectionURL) != "" {
		listID = strings.TrimSpace(link.CollectionURL)
	}
	if listID == "" {
		return errors.New("CloudKit 提醒事项列表未配置")
	}
	fields, err := appleCloudKitReminderFields(item, listID)
	if err != nil {
		return err
	}
	ownerID, err := client.owner(ctx)
	if err != nil {
		return err
	}

	recordName := ""
	if hasLink {
		recordName = strings.TrimSpace(link.RemoteUID)
	}
	changeTag := ""
	if recordName == "" {
		recordName = ckNewUUIDString()
		hasLink = false
	}
	if hasLink {
		changeTag = strings.TrimSpace(link.ETag)
		if changeTag == "" {
			record, lookupErr := client.lookupRecord(ctx, ownerID, recordName)
			if lookupErr != nil {
				if isAppleCloudKitRecordCode(lookupErr, "NOT_FOUND") {
					hasLink = false
				} else {
					return lookupErr
				}
			} else {
				changeTag = cloudKitRecordChangeTag(record)
			}
		}
	}

	if hasLink {
		op := map[string]interface{}{
			"operationType": "update",
			"record": map[string]interface{}{
				"recordType": "Reminder", "recordName": recordName,
				"recordChangeTag": changeTag, "fields": fields,
			},
		}
		result, updateErr := client.modifyRecords(ctx, ownerID, []map[string]interface{}{op})
		if updateErr != nil && (isAppleCloudKitRecordCode(updateErr, "CONFLICT") || isAppleCloudKitRecordCode(updateErr, "NOT_FOUND")) {
			if isAppleCloudKitRecordCode(updateErr, "CONFLICT") {
				record, lookupErr := client.lookupRecord(ctx, ownerID, recordName)
				if lookupErr != nil {
					return lookupErr
				}
				op["record"].(map[string]interface{})["recordChangeTag"] = cloudKitRecordChangeTag(record)
				result, updateErr = client.modifyRecords(ctx, ownerID, []map[string]interface{}{op})
			}
			if updateErr == nil {
				return saveAppleCloudKitLink(db, item, recordName, listID, cloudKitChangeTagFromResult(result))
			}
			if isAppleCloudKitRecordCode(updateErr, "NOT_FOUND") {
				hasLink = false
			} else {
				return updateErr
			}
		} else if updateErr != nil {
			return updateErr
		} else {
			return saveAppleCloudKitLink(db, item, recordName, listID, cloudKitChangeTagFromResult(result))
		}
	}

	op := map[string]interface{}{
		"operationType": "create",
		"record": map[string]interface{}{
			"recordType": "Reminder", "recordName": recordName, "fields": fields,
		},
	}
	result, err := client.modifyRecords(ctx, ownerID, []map[string]interface{}{op})
	if err != nil {
		return err
	}
	return saveAppleCloudKitLink(db, item, recordName, listID, cloudKitChangeTagFromResult(result))
}

func deleteAppleCloudKitRemote(ctx context.Context, db *gorm.DB, client *appleCloudKitClient, cfg AppleReminderSync, row AppleReminderOutbox) error {
	var link AppleReminderLink
	if err := db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).First(&link).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil
		}
		return err
	}
	recordName := strings.TrimSpace(link.RemoteUID)
	if recordName == "" {
		return db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).Delete(&AppleReminderLink{}).Error
	}
	ownerID, err := client.owner(ctx)
	if err != nil {
		return err
	}
	changeTag := strings.TrimSpace(link.ETag)
	if changeTag == "" {
		record, lookupErr := client.lookupRecord(ctx, ownerID, recordName)
		if lookupErr != nil {
			if isAppleCloudKitRecordCode(lookupErr, "NOT_FOUND") {
				return db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).Delete(&AppleReminderLink{}).Error
			}
			return lookupErr
		}
		changeTag = cloudKitRecordChangeTag(record)
	}
	op := map[string]interface{}{
		"operationType": "delete",
		"record": map[string]interface{}{
			"recordType": "Reminder", "recordName": recordName, "recordChangeTag": changeTag,
		},
	}
	if _, err := client.modifyRecords(ctx, ownerID, []map[string]interface{}{op}); err != nil {
		if isAppleCloudKitRecordCode(err, "NOT_FOUND") {
			return db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).Delete(&AppleReminderLink{}).Error
		}
		return err
	}
	return db.Where("reminder_id = ? AND user_id = ?", row.ReminderID, cfg.UserID).Delete(&AppleReminderLink{}).Error
}

func saveAppleCloudKitLink(db *gorm.DB, item Reminder, recordName, listID, changeTag string) error {
	link := AppleReminderLink{
		UserID: item.UserID, ReminderID: item.ID, RemoteUID: recordName,
		RemoteURL: recordName, ETag: changeTag, CollectionURL: listID,
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "reminder_id"}},
		DoUpdates: clause.Assignments(map[string]interface{}{
			"user_id":        item.UserID,
			"remote_uid":     recordName,
			"remote_url":     recordName,
			"e_tag":          changeTag,
			"collection_url": listID,
			"updated_at":     time.Now(),
		}),
	}).Create(&link).Error
}

func cloudKitChangeTagFromResult(result map[string]interface{}) string {
	records, _ := result["records"].([]interface{})
	if len(records) == 0 {
		return ""
	}
	record, _ := records[0].(map[string]interface{})
	return cloudKitRecordChangeTag(record)
}
