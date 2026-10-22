package reminder

import "time"

// CalendarSync describes a one-way import of an external CalDAV calendar into
// this app. One row per user: the product has a single user-facing "钉钉日程"
// switch, so a second row would only create ambiguity about which calendar the
// imported items belong to.
type CalendarSync struct {
	ID     uint `gorm:"primarykey" json:"id"`
	UserID uint `gorm:"not null;uniqueIndex" json:"-"`

	// CalendarURL is the resolved calendar collection (after CalDAV discovery),
	// not the bare server address the user types in.
	CalendarURL string `gorm:"size:512;not null" json:"calendar_url"`
	Username    string `gorm:"size:128;not null" json:"username"`
	// Password is sealed with the same AES-GCM scheme as channel bindings; it is
	// never returned by the API and never written to logs.
	Password string `gorm:"type:text;not null" json:"-"`

	ListID uint `gorm:"not null" json:"list_id"`
	// Enabled defaults to true so that saving credentials starts syncing right
	// away; users who only want a one-off import can switch it off afterwards.
	Enabled bool `gorm:"not null;default:true" json:"enabled"`

	// WindowBackDays / WindowForwardDays bound every fetch. Recurring events are
	// not expanded locally, so the window decides how far ahead a repeating
	// meeting shows up in the calendar views.
	WindowBackDays    int `gorm:"not null;default:7" json:"window_back_days"`
	WindowForwardDays int `gorm:"not null;default:180" json:"window_forward_days"`
	// RemindBeforeMinutes applies to imported items only; it mirrors the
	// per-reminder field so imports do not silently use a different lead time.
	RemindBeforeMinutes int `gorm:"not null;default:15" json:"remind_before_minutes"`

	LastSyncAt   *time.Time `json:"last_sync_at"`
	LastStatus   string     `gorm:"size:16" json:"last_status"`
	LastError    string     `gorm:"size:512" json:"last_error"`
	LastImported int        `gorm:"not null;default:0" json:"last_imported"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// ExternalSourceDingTalk marks reminders imported from the DingTalk calendar.
const ExternalSourceDingTalk = "dingtalk"

// CalendarSyncWindow returns the fetch window for a sync config, with sane
// fallbacks for rows created before a field existed.
func (c CalendarSync) window() (from, to time.Time) {
	now := time.Now().UTC()
	back := c.WindowBackDays
	if back <= 0 || back > 365 {
		back = 7
	}
	forward := c.WindowForwardDays
	if forward <= 0 || forward > 365 {
		forward = 180
	}
	return now.AddDate(0, 0, -back), now.AddDate(0, 0, forward)
}
