package reminder

import (
	"strings"
	"testing"
	"time"

	"gorm.io/gorm"

	"smallgo/server/database"
)

// SQLite 给**已存在的表**新增 NOT NULL 列时，必须同时给出默认值，否则
// 启动迁移直接报 "Cannot add a NOT NULL column with default value NULL"，
// 服务起不来。全新数据库建表时不受这个限制，因此普通测试发现不了——
// v0.11.0 就是这样把线上老库迁崩的。
//
// 两层保障：
//  1. 断言这两个字段的 gorm tag 必须带 default（确定性检查，任何环境都会跑）；
//  2. 删掉列后重跑迁移，复现真实的 ALTER TABLE 路径（依赖 SQLite 支持 DROP COLUMN）。
func TestNewColumnsAreSafeToAddToExistingTable(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	newColumns := []string{"remind_before_minutes", "repeat_notify_times"}

	stmt := &gorm.Statement{DB: appDB}
	if err := stmt.Parse(&Reminder{}); err != nil {
		t.Fatal(err)
	}
	for _, field := range stmt.Schema.Fields {
		if !containsString(newColumns, field.DBName) {
			continue
		}
		tag := field.Tag.Get("gorm")
		if strings.Contains(tag, "not null") && !strings.Contains(tag, "default:") {
			t.Fatalf("%s 是 NOT NULL 但没有 default，老库升级会失败（tag=%q）", field.DBName, tag)
		}
	}

	migrator := appDB.Migrator()
	dropped := 0
	for _, column := range newColumns {
		if !migrator.HasColumn(&Reminder{}, column) {
			continue
		}
		if err := migrator.DropColumn(&Reminder{}, column); err != nil {
			t.Logf("跳过端到端部分（不支持 DROP COLUMN）：%v", err)
			return
		}
		dropped++
	}
	if dropped == 0 {
		return
	}

	if err := database.AutoMigrate(appDB); err != nil {
		t.Fatalf("老库升级迁移失败：%v", err)
	}
	for _, column := range newColumns {
		if !migrator.HasColumn(&Reminder{}, column) {
			t.Fatalf("迁移后缺少列 %s", column)
		}
	}
}

// 存量行里同一个时刻可能存成两种文本（"+08:00" 与 "Z"），迁移必须把它们统一
// 改写成 UTC，且不改变时刻本身；迁移可重跑（幂等）。
func TestTimestampNormalizationUpgrade(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const legacy = "2026-11-06T07:00:00+08:00"
	if err := appDB.Exec(`INSERT INTO reminders
		(user_id, list_id, title, due_at, repeat_rule, calendar, version, priority, all_day, position, remind_before_minutes, repeat_notify_minutes, repeat_notify_times)
		VALUES (1, 1, 'legacy', ?, 'none', 'solar', 1, 0, 0, 0, 0, 0, 0)`, legacy).Error; err != nil {
		t.Fatalf("insert legacy row: %v", err)
	}

	var upgrade *database.Upgrade
	for i := range database.Upgrades {
		if database.Upgrades[i].Name == "normalize_reminder_timestamps_to_utc" {
			upgrade = &database.Upgrades[i]
			break
		}
	}
	if upgrade == nil {
		t.Fatal("未注册 normalize_reminder_timestamps_to_utc 迁移")
	}
	for round := 0; round < 2; round++ { // 第二轮验证幂等
		if err := upgrade.Upgrade(appDB); err != nil {
			t.Fatalf("run upgrade (round %d): %v", round, err)
		}
	}

	var stored string
	if err := appDB.Raw("SELECT due_at FROM reminders WHERE title = 'legacy'").Scan(&stored).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(stored, "Z") {
		t.Fatalf("due_at 未归一化为 UTC: %q", stored)
	}
	var item Reminder
	if err := appDB.Where("title = ?", "legacy").First(&item).Error; err != nil {
		t.Fatal(err)
	}
	want := time.Date(2026, 11, 5, 23, 0, 0, 0, time.UTC)
	if item.DueAt == nil || !item.DueAt.Equal(want) {
		t.Fatalf("迁移改变了时刻: got %v want %v", item.DueAt, want)
	}
}

// 站内消息改为按需开启后，存量导入日程上遗留的 inapp 渠道必须清掉，
// 否则用户升级后仍会继续收到没有意义的站内推送；手动创建的提醒不受影响。
func TestDisableInAppForImportedRemindersUpgrade(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	manual := Reminder{UserID: 61, ListID: 1, Title: "手动提醒", RepeatRule: "none", Calendar: "solar", Version: 1}
	imported := Reminder{UserID: 61, ListID: 1, Title: "导入日程", RepeatRule: "none", Calendar: "solar", Version: 1,
		ExternalSource: ExternalSourceDingTalk, ExternalUID: "imported-1"}
	for _, item := range []*Reminder{&manual, &imported} {
		if err := appDB.Create(item).Error; err != nil {
			t.Fatal(err)
		}
	}
	channels := []ReminderChannel{
		{ReminderID: manual.ID, UserID: 61, Channel: ChannelInApp},
		{ReminderID: imported.ID, UserID: 61, Channel: ChannelInApp},
		{ReminderID: imported.ID, UserID: 61, Channel: ChannelEmail},
	}
	if err := appDB.Create(&channels).Error; err != nil {
		t.Fatal(err)
	}
	// 升级前已经排队的站内投递任务：导入条目上的应被取消，手动条目上的保留。
	runAt := time.Now().Add(time.Hour)
	jobs := []DeliveryJob{
		{UserID: 61, ReminderID: manual.ID, Channel: ChannelInApp, ScheduledFor: runAt, RunAt: runAt,
			Status: "pending", IdempotencyKey: "manual-inapp"},
		{UserID: 61, ReminderID: imported.ID, Channel: ChannelInApp, ScheduledFor: runAt, RunAt: runAt,
			Status: "pending", IdempotencyKey: "imported-inapp"},
	}
	if err := appDB.Create(&jobs).Error; err != nil {
		t.Fatal(err)
	}

	var upgrade *database.Upgrade
	for i := range database.Upgrades {
		if database.Upgrades[i].Name == "disable_inapp_for_imported_reminders" {
			upgrade = &database.Upgrades[i]
			break
		}
	}
	if upgrade == nil {
		t.Fatal("未注册 disable_inapp_for_imported_reminders 迁移")
	}
	for round := 0; round < 2; round++ { // 第二轮验证幂等
		if err := upgrade.Upgrade(appDB); err != nil {
			t.Fatalf("run upgrade (round %d): %v", round, err)
		}
	}

	var manualInApp, importedInApp, importedEmail int64
	if err := appDB.Model(&ReminderChannel{}).Where("reminder_id = ? AND channel = ?", manual.ID, ChannelInApp).Count(&manualInApp).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&ReminderChannel{}).Where("reminder_id = ? AND channel = ?", imported.ID, ChannelInApp).Count(&importedInApp).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Model(&ReminderChannel{}).Where("reminder_id = ? AND channel = ?", imported.ID, ChannelEmail).Count(&importedEmail).Error; err != nil {
		t.Fatal(err)
	}
	if manualInApp != 1 {
		t.Fatalf("手动提醒的站内渠道被误删: %d", manualInApp)
	}
	if importedInApp != 0 {
		t.Fatalf("导入日程的站内渠道未清理: %d", importedInApp)
	}
	if importedEmail != 1 {
		t.Fatalf("导入日程的其它渠道被误删: %d", importedEmail)
	}

	var manualJob, importedJob DeliveryJob
	if err := appDB.Where("idempotency_key = ?", "manual-inapp").First(&manualJob).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.Where("idempotency_key = ?", "imported-inapp").First(&importedJob).Error; err != nil {
		t.Fatal(err)
	}
	if manualJob.Status != "pending" {
		t.Fatalf("手动提醒的投递任务被误取消: %s", manualJob.Status)
	}
	if importedJob.Status != "cancelled" {
		t.Fatalf("导入日程的站内投递任务未取消: %s", importedJob.Status)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
