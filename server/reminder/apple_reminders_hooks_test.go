package reminder

import (
	"net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
)

func enableAppleSyncForTest(t *testing.T, userID uint) {
	t.Helper()
	cfg := AppleReminderSync{
		UserID: userID, Server: appleReminderDefaultServer, AppleID: "hook@example.com",
		Password: "stored-secret", CollectionURL: "https://caldav.icloud.com/calendars/u1/reminders/",
		Enabled: true,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	// GORM writes the zero value of a bool column as its default; force it on.
	if err := appDB.Model(&AppleReminderSync{}).Where("user_id = ?", userID).Update("enabled", true).Error; err != nil {
		t.Fatal(err)
	}
}

func assertAppleOutboxAction(t *testing.T, userID, reminderID uint, want string) {
	t.Helper()
	var row AppleReminderOutbox
	if err := appDB.Where("user_id = ? AND reminder_id = ?", userID, reminderID).First(&row).Error; err != nil {
		t.Fatalf("outbox row missing for reminder %d: %v", reminderID, err)
	}
	if row.Action != want {
		t.Fatalf("outbox action = %q, want %q", row.Action, want)
	}
	if row.Status != appleReminderStatusPending {
		t.Fatalf("outbox status = %q, want pending", row.Status)
	}
}

func clearAppleOutboxForTest(t *testing.T, reminderID uint) {
	t.Helper()
	if err := appDB.Where("reminder_id = ?", reminderID).Delete(&AppleReminderOutbox{}).Error; err != nil {
		t.Fatal(err)
	}
}

func TestReminderMutationsEnqueueAppleSync(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const userID uint = 31
	enableAppleSyncForTest(t, userID)
	due := time.Now().Add(time.Hour).UTC().Truncate(time.Second)

	created, err := createReminder(appDB, userID, SaveReminderInput{Title: "创建即入队", DueAt: &due, RepeatRule: "none"})
	if err != nil {
		t.Fatal(err)
	}
	assertAppleOutboxAction(t, userID, created.ID, appleReminderActionUpsert)

	clearAppleOutboxForTest(t, created.ID)
	if _, err := updateReminder(appDB, userID, created.ID, SaveReminderInput{
		Title: "更新后入队", DueAt: &due, RepeatRule: "none", Version: created.Version,
	}); err != nil {
		t.Fatal(err)
	}
	assertAppleOutboxAction(t, userID, created.ID, appleReminderActionUpsert)

	clearAppleOutboxForTest(t, created.ID)
	if err := deleteReminder(appDB, userID, created.ID); err != nil {
		t.Fatal(err)
	}
	assertAppleOutboxAction(t, userID, created.ID, appleReminderActionDelete)

	completed, err := createReminder(appDB, userID, SaveReminderInput{Title: "完成后入队", DueAt: &due, RepeatRule: "none"})
	if err != nil {
		t.Fatal(err)
	}
	clearAppleOutboxForTest(t, completed.ID)
	done, err := completeReminder(appDB, userID, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if done.CompletedAt == nil {
		t.Fatal("non-repeating reminder should become completed")
	}
	assertAppleOutboxAction(t, userID, completed.ID, appleReminderActionUpsert)

	clearAppleOutboxForTest(t, completed.ID)
	restored, err := restoreReminder(appDB, userID, completed.ID)
	if err != nil {
		t.Fatal(err)
	}
	if restored.CompletedAt != nil {
		t.Fatal("restore should clear completed_at")
	}
	assertAppleOutboxAction(t, userID, completed.ID, appleReminderActionUpsert)

	snoozed, err := createReminder(appDB, userID, SaveReminderInput{Title: "稍后提醒入队", RepeatRule: "none"})
	if err != nil {
		t.Fatal(err)
	}
	clearAppleOutboxForTest(t, snoozed.ID)
	until := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	afterSnooze, err := snoozeReminder(appDB, userID, snoozed.ID, until)
	if err != nil {
		t.Fatal(err)
	}
	if afterSnooze.SnoozedUntil == nil {
		t.Fatal("snooze should set snoozed_until")
	}
	assertAppleOutboxAction(t, userID, snoozed.ID, appleReminderActionUpsert)
}

func TestAppleReminderRoutesRegistered(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	gin.SetMode(gin.TestMode)
	engine := gin.New()
	setupAuthRoutes(engine.Group("/api"), appDB)

	registered := map[string]bool{}
	for _, route := range engine.Routes() {
		registered[route.Method+" "+route.Path] = true
	}
	for _, want := range includeAppleReminderRoutes() {
		if !registered[want] {
			t.Fatalf("missing route %s", want)
		}
	}
}

func includeAppleReminderRoutes() []string {
	return []string{
		http.MethodGet + " /api/reminder/apple-reminders",
		http.MethodPut + " /api/reminder/apple-reminders",
		http.MethodDelete + " /api/reminder/apple-reminders",
		http.MethodPost + " /api/reminder/apple-reminders/discover",
		http.MethodPost + " /api/reminder/apple-reminders/run",
		http.MethodPost + " /api/reminder/apple-reminders/cloudkit/login",
		http.MethodPost + " /api/reminder/apple-reminders/cloudkit/verify",
		http.MethodPost + " /api/reminder/apple-reminders/cloudkit/resend",
	}
}
