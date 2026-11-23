package reminder

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"smallgo/server/reminder/core"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
)

type countingNotifier struct {
	channel string
	count   *atomic.Int64
	delay   time.Duration
}

func (n countingNotifier) Channel() string { return n.channel }
func (n countingNotifier) Send(ctx context.Context, msg core.OutboundMessage) (core.SendResult, error) {
	n.count.Add(1)
	if n.delay > 0 {
		select {
		case <-time.After(n.delay):
		case <-ctx.Done():
			return core.SendResult{}, ctx.Err()
		}
	}
	return core.SendResult{ExternalID: "ok"}, nil
}

// dispatchDueJobs must deliver each due job exactly once even though it now
// fans out to a bounded worker pool; the CAS claim is what prevents duplicates.
func TestDispatchDueJobsDeliversEachJobOnce(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	registerBuiltinIntegrations()

	const channel = "testc"
	var sends atomic.Int64
	notifierRegistry.Register(channel, func(_ *gorm.DB) core.Notifier {
		return countingNotifier{channel: channel, count: &sends, delay: 20 * time.Millisecond}
	})

	due := time.Now().UTC().Add(-time.Minute).Truncate(time.Second)
	const jobs = 8
	for i := 0; i < jobs; i++ {
		item := Reminder{UserID: 990, ListID: 1, Title: fmt.Sprintf("并发 %d", i), DueAt: &due, RepeatRule: "none"}
		if err := appDB.Create(&item).Error; err != nil {
			t.Fatal(err)
		}
		if err := appDB.Create(&ReminderChannel{ReminderID: item.ID, UserID: 990, Channel: channel, Enabled: true}).Error; err != nil {
			t.Fatal(err)
		}
		job := DeliveryJob{
			UserID: 990, ReminderID: item.ID, Channel: channel,
			ScheduledFor: due, RunAt: due, Status: "pending",
			IdempotencyKey: fmt.Sprintf("dedupe-%d", i),
		}
		if err := appDB.Create(&job).Error; err != nil {
			t.Fatal(err)
		}
	}

	dispatchDueJobs(appDB)

	if got := sends.Load(); got != jobs {
		t.Fatalf("expected %d sends, got %d", jobs, got)
	}
	var succeeded int64
	if err := appDB.Model(&DeliveryJob{}).Where("status = ?", "succeeded").Count(&succeeded).Error; err != nil {
		t.Fatal(err)
	}
	if succeeded != jobs {
		t.Fatalf("expected %d succeeded jobs, got %d", jobs, succeeded)
	}
	var attempts int64
	if err := appDB.Model(&DeliveryAttempt{}).Count(&attempts).Error; err != nil {
		t.Fatal(err)
	}
	if attempts != jobs {
		t.Fatalf("each job should record exactly one attempt, got %d", attempts)
	}
}

func TestHandleNotificationsPaginates(t *testing.T) {
	cleanup := testDB(t)
	defer cleanup()
	gin.SetMode(gin.TestMode)

	for i := 0; i < 25; i++ {
		if err := appDB.Create(&Notification{UserID: 993, Type: "system", Title: fmt.Sprintf("通知 %d", i)}).Error; err != nil {
			t.Fatal(err)
		}
	}
	r := gin.New()
	r.Use(func(c *gin.Context) { c.Set("userID", uint(993)); c.Next() })
	r.GET("/notifications", handleNotifications(appDB))

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/notifications?page=2&page_size=10", nil)
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Items    []Notification `json:"items"`
			Total    int64          `json:"total"`
			Page     int            `json:"page"`
			PageSize int            `json:"pageSize"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if resp.Data.Total != 25 || resp.Data.Page != 2 || resp.Data.PageSize != 10 || len(resp.Data.Items) != 10 {
		t.Fatalf("unexpected page payload: %+v", resp.Data)
	}

	// Legacy callers (no page param) still receive a plain array.
	w = httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/notifications", nil))
	var legacy struct {
		Data []Notification `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &legacy); err != nil {
		t.Fatal(err)
	}
	if len(legacy.Data) != 25 {
		t.Fatalf("legacy list should return all rows, got %d", len(legacy.Data))
	}
}
