package reminder

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func seedAppleOutboxRows(t *testing.T, userID uint, count int, firstReminderID uint) []AppleReminderOutbox {
	t.Helper()
	rows := make([]AppleReminderOutbox, 0, count)
	for i := 0; i < count; i++ {
		row := AppleReminderOutbox{
			UserID:     userID,
			ReminderID: firstReminderID + uint(i),
			Action:     appleReminderActionUpsert,
			Status:     appleReminderStatusPending,
		}
		if err := appDB.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func TestDrainAppleReminderOutboxProcessesAllBatches(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	const total = appleReminderBatchSize + 37
	seedAppleOutboxRows(t, 51, total, 1000)

	processed := 0
	pushed, failed, firstErr := drainAppleReminderOutbox(appDB, 51, time.Now().UTC(), func(ctx context.Context, row AppleReminderOutbox) error {
		processed++
		return deleteAppleReminderOutboxIfCurrent(appDB, row)
	})
	if firstErr != nil {
		t.Fatalf("drain returned error: %v", firstErr)
	}
	if pushed != total || failed != 0 || processed != total {
		t.Fatalf("pushed=%d failed=%d processed=%d, want %d/0/%d", pushed, failed, processed, total, total)
	}
	var left int64
	if err := appDB.Model(&AppleReminderOutbox{}).Where("user_id = ?", 51).Count(&left).Error; err != nil {
		t.Fatal(err)
	}
	if left != 0 {
		t.Fatalf("expected empty queue, %d rows left", left)
	}
}

func TestDrainAppleReminderOutboxRecoversRowPanic(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	rows := seedAppleOutboxRows(t, 52, 1, 2000)
	pushed, failed, firstErr := drainAppleReminderOutbox(appDB, 52, time.Now().UTC(), func(ctx context.Context, row AppleReminderOutbox) error {
		panic("boom")
	})
	if pushed != 0 || failed != 1 {
		t.Fatalf("pushed=%d failed=%d, want 0/1", pushed, failed)
	}
	if firstErr == nil || !strings.Contains(firstErr.Error(), "boom") {
		t.Fatalf("panic should become an error, got %v", firstErr)
	}
	var stored AppleReminderOutbox
	if err := appDB.First(&stored, rows[0].ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.Status != appleReminderStatusRetry || stored.AttemptCount != 1 || !strings.Contains(stored.LastError, "boom") {
		t.Fatalf("panic row should be retry/attempt=1, got %+v", stored)
	}
}

func TestDrainAppleReminderOutboxStopsOnAuthFailure(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	seedAppleOutboxRows(t, 53, 3, 3000)
	called := 0
	_, failed, firstErr := drainAppleReminderOutbox(appDB, 53, time.Now().UTC(), func(ctx context.Context, row AppleReminderOutbox) error {
		called++
		return fmt.Errorf("%w: 401", errAppleAuth)
	})
	if called != 1 || failed != 1 || !errors.Is(firstErr, errAppleAuth) {
		t.Fatalf("auth failure should stop after first row, called=%d failed=%d err=%v", called, failed, firstErr)
	}
	var pending, errored int64
	appDB.Model(&AppleReminderOutbox{}).Where("user_id = ? AND status = ?", 53, appleReminderStatusPending).Count(&pending)
	appDB.Model(&AppleReminderOutbox{}).Where("user_id = ? AND status = ?", 53, appleReminderStatusError).Count(&errored)
	if pending != 2 || errored != 1 {
		t.Fatalf("pending=%d errored=%d, want 2/1", pending, errored)
	}
}

func TestProcessAppleReminderOutboxSkipsCredentialDecryptWhenIdle(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := AppleReminderSync{
		UserID: 61, Backend: appleReminderBackendCalDAV, Server: appleReminderDefaultServer,
		AppleID: "idle@example.com", Password: "not-a-sealed-secret",
		CollectionURL: "https://caldav.icloud.com/calendars/u/reminders/", Enabled: true,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	pushed, failed, err := processAppleReminderOutbox(appDB, &cfg)
	if err != nil {
		t.Fatalf("idle queue must not decrypt credentials: %v", err)
	}
	if pushed != 0 || failed != 0 {
		t.Fatalf("pushed=%d failed=%d, want 0/0", pushed, failed)
	}
	var stored AppleReminderSync
	if err := appDB.First(&stored, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastError != "" {
		t.Fatalf("idle queue must not record a credential error, got %q", stored.LastError)
	}
}

func TestProcessAppleCloudKitOutboxSkipsSessionWhenIdle(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := AppleReminderSync{
		UserID: 62, Backend: appleReminderBackendCloudKit, Server: appleReminderBackendCloudKit,
		AppleID: "ck@example.com", SessionData: "not-a-sealed-session",
		CollectionURL: "list-id", Enabled: true,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	pushed, failed, err := processAppleCloudKitOutbox(appDB, cfg)
	if err != nil {
		t.Fatalf("idle CloudKit queue must not read the session: %v", err)
	}
	if pushed != 0 || failed != 0 {
		t.Fatalf("pushed=%d failed=%d, want 0/0", pushed, failed)
	}
}

func TestRunAppleReminderSyncRefreshesIdleStatus(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	cfg := AppleReminderSync{
		UserID: 63, Backend: appleReminderBackendCalDAV, Server: appleReminderDefaultServer,
		AppleID: "idle@example.com", Password: "not-a-sealed-secret",
		CollectionURL: "https://caldav.icloud.com/calendars/u/reminders/", Enabled: true,
	}
	if err := appDB.Create(&cfg).Error; err != nil {
		t.Fatal(err)
	}
	result, err := RunAppleReminderSync(appDB, 63)
	if err != nil {
		t.Fatalf("idle manual sync: %v", err)
	}
	if result.Pushed != 0 || result.Failed != 0 {
		t.Fatalf("pushed=%d failed=%d, want 0/0", result.Pushed, result.Failed)
	}
	var stored AppleReminderSync
	if err := appDB.First(&stored, cfg.ID).Error; err != nil {
		t.Fatal(err)
	}
	if stored.LastSyncAt == nil || stored.LastStatus != "ok" {
		t.Fatalf("idle manual sync should refresh status, got last_sync_at=%v status=%q", stored.LastSyncAt, stored.LastStatus)
	}
}

func TestRecordAppleReminderFailureBackoffAndPermanentAuth(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	row := AppleReminderOutbox{UserID: 64, ReminderID: 4000, Action: appleReminderActionUpsert, Status: appleReminderStatusPending}
	if err := appDB.Create(&row).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.First(&row, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	recordAppleReminderFailure(appDB, row, errors.New("temporary"), false)
	var first AppleReminderOutbox
	if err := appDB.First(&first, row.ID).Error; err != nil {
		t.Fatal(err)
	}
	if first.Status != appleReminderStatusRetry || first.AttemptCount != 1 || first.NextAttemptAt == nil {
		t.Fatalf("first failure should be retry with backoff, got %+v", first)
	}
	if delta := first.NextAttemptAt.Sub(time.Now()); delta < 30*time.Second || delta > 2*time.Minute {
		t.Fatalf("first backoff should be about one minute, got %v", delta)
	}

	authRow := AppleReminderOutbox{UserID: 64, ReminderID: 4001, Action: appleReminderActionUpsert, Status: appleReminderStatusPending}
	if err := appDB.Create(&authRow).Error; err != nil {
		t.Fatal(err)
	}
	if err := appDB.First(&authRow, authRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	recordAppleReminderFailure(appDB, authRow, fmt.Errorf("%w: bad password", errAppleAuth), true)
	var permanent AppleReminderOutbox
	if err := appDB.First(&permanent, authRow.ID).Error; err != nil {
		t.Fatal(err)
	}
	if permanent.Status != appleReminderStatusError || permanent.NextAttemptAt != nil {
		t.Fatalf("auth failure should be permanent without backoff, got %+v", permanent)
	}
}

func TestAppleReminderBackoffSchedule(t *testing.T) {
	cases := map[int]time.Duration{
		0: time.Minute, 1: time.Minute, 2: 5 * time.Minute,
		3: 30 * time.Minute, 4: 2 * time.Hour, 8: 2 * time.Hour,
	}
	for attempt, want := range cases {
		if got := appleReminderBackoff(attempt); got != want {
			t.Fatalf("backoff(%d)=%v, want %v", attempt, got, want)
		}
	}
}

func TestUpsertAppleReminderRemoteBuildsVTodoAndStoresLink(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()

	due := time.Now().Add(2 * time.Hour).UTC().Truncate(time.Second)
	item := mustCreateReminder(t, Reminder{UserID: 71, Title: "买牛奶", Notes: "低脂", Priority: 2, DueAt: &due})
	cfg := AppleReminderSync{UserID: 71, CollectionURL: "https://caldav.icloud.com/calendars/u/reminders/"}
	wantPath := "/calendars/u/reminders/" + appleReminderFileName(item.UserID, item.ID)

	client := newAppleCalDAVClient("user@example.com", "app-password")
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.Method != http.MethodPut {
			t.Fatalf("method = %q, want PUT", r.Method)
		}
		if r.URL.Path != wantPath {
			t.Fatalf("path = %q, want %q", r.URL.Path, wantPath)
		}
		if got := r.Header.Get("If-None-Match"); got != "*" {
			t.Fatalf("If-None-Match = %q, want *", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"BEGIN:VTODO", "SUMMARY:买牛奶", "DESCRIPTION:低脂", "DUE:"} {
			if !strings.Contains(string(body), want) {
				t.Fatalf("VTODO body missing %q:\n%s", want, string(body))
			}
		}
		return caldavStubResponse(http.StatusCreated, "", map[string]string{
			"ETag":     "\"etag-1\"",
			"Location": "/calendars/u/reminders/moved.ics",
		}), nil
	})

	if err := upsertAppleReminderRemote(context.Background(), appDB, client, cfg, item); err != nil {
		t.Fatalf("upsertAppleReminderRemote: %v", err)
	}
	var link AppleReminderLink
	if err := appDB.Where("reminder_id = ? AND user_id = ?", item.ID, item.UserID).First(&link).Error; err != nil {
		t.Fatal(err)
	}
	if link.ETag != "\"etag-1\"" {
		t.Fatalf("etag = %q", link.ETag)
	}
	if link.RemoteURL != "https://caldav.icloud.com/calendars/u/reminders/moved.ics" {
		t.Fatalf("remote url = %q", link.RemoteURL)
	}
	if link.CollectionURL != cfg.CollectionURL || link.RemoteUID != appleReminderUID(item.UserID, item.ID) {
		t.Fatalf("unexpected link: %+v", link)
	}
}
