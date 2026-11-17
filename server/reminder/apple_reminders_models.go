package reminder

import "time"

const (
	appleReminderActionUpsert = "upsert"
	appleReminderActionDelete = "delete"

	appleReminderStatusPending = "pending"
	appleReminderStatusRetry   = "retry"
	appleReminderStatusError   = "error"

	appleReminderMaxAttempts    = 8
	appleReminderBatchSize      = 200
	appleReminderDefaultServer  = "caldav.icloud.com"
	appleReminderCalendarProdID = "-//SmallGo Reminder//Apple Reminders//EN"
	appleReminderUIDDomain      = "reminder.local"
)

// AppleReminderSync stores one user's iCloud CalDAV reminder-list binding.
// Password is encrypted at rest through encryptTarget/decryptTarget and never
// leaves the server in API responses.
type AppleReminderSync struct {
	ID             uint       `gorm:"primaryKey" json:"id"`
	UserID         uint       `gorm:"uniqueIndex;not null" json:"user_id"`
	Server         string     `gorm:"size:255;not null;default:caldav.icloud.com" json:"server"`
	AppleID        string     `gorm:"size:255;not null" json:"apple_id"`
	Password       string     `gorm:"type:text;not null" json:"-"`
	CollectionURL  string     `gorm:"size:512;not null" json:"collection_url"`
	CollectionName string     `gorm:"size:200" json:"collection_name"`
	Enabled        bool       `gorm:"not null;default:true" json:"enabled"`
	LastSyncAt     *time.Time `json:"last_sync_at"`
	LastStatus     string     `gorm:"size:16" json:"last_status"`
	LastError      string     `gorm:"size:512" json:"last_error"`
	LastPushed     int        `gorm:"not null;default:0" json:"last_pushed"`
	CreatedAt      time.Time  `json:"created_at"`
	UpdatedAt      time.Time  `json:"updated_at"`
}

// AppleReminderLink maps a local reminder to its remote VTODO object. Keeping
// the ETag lets updates use conditional requests without a read round-trip.
type AppleReminderLink struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	UserID        uint      `gorm:"index;not null" json:"user_id"`
	ReminderID    uint      `gorm:"uniqueIndex;not null" json:"reminder_id"`
	RemoteUID     string    `gorm:"size:255" json:"remote_uid"`
	RemoteURL     string    `gorm:"size:512" json:"remote_url"`
	ETag          string    `gorm:"column:e_tag;size:255" json:"etag"`
	CollectionURL string    `gorm:"size:512" json:"collection_url"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// AppleReminderOutbox coalesces all pending intents for one reminder. A newer
// local change replaces the previous action instead of growing the queue.
type AppleReminderOutbox struct {
	ID            uint       `gorm:"primaryKey" json:"id"`
	UserID        uint       `gorm:"index;not null" json:"user_id"`
	ReminderID    uint       `gorm:"uniqueIndex;not null" json:"reminder_id"`
	Action        string     `gorm:"size:16;not null;default:upsert" json:"action"`
	Status        string     `gorm:"size:16;not null;default:pending;index" json:"status"`
	AttemptCount  int        `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt *time.Time `json:"next_attempt_at"`
	LastError     string     `gorm:"size:512" json:"last_error"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// AppleReminderSyncDTO is the safe API projection of a sync configuration.
type AppleReminderSyncDTO struct {
	AppleReminderSync
	PendingCount int64 `json:"pending_count"`
	LinkedCount  int64 `json:"linked_count"`
}

// AppleReminderSyncInput is the user-editable part of the configuration.
type AppleReminderSyncInput struct {
	Server         string `json:"server"`
	AppleID        string `json:"apple_id"`
	Password       string `json:"password"`
	CollectionURL  string `json:"collection_url"`
	CollectionName string `json:"collection_name"`
	Enabled        *bool  `json:"enabled"`
}

// AppleReminderDiscoverInput identifies an iCloud account while listing lists.
// An empty Password means "reuse the stored password" for an existing config.
type AppleReminderDiscoverInput struct {
	Server   string `json:"server"`
	AppleID  string `json:"apple_id"`
	Password string `json:"password"`
}

// AppleReminderCollection is one VTODO-capable iCloud list.
type AppleReminderCollection struct {
	URL  string `json:"url"`
	Name string `json:"name"`
}

// AppleReminderSyncResult summarises one manual or scheduled run.
type AppleReminderSyncResult struct {
	Pushed  int    `json:"pushed"`
	Failed  int    `json:"failed"`
	Pending int64  `json:"pending"`
	Error   string `json:"error,omitempty"`
}
