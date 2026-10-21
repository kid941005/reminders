package reminder

import "time"

const (
	ChannelInApp    = "inapp"
	ChannelEmail    = "email"
	ChannelSMS      = "sms"
	ChannelFeishu   = "feishu"
	ChannelQQ       = "qq"
	ChannelDingTalk = "dingtalk"
	ChannelBark     = "bark"
)

var supportedChannels = map[string]bool{
	ChannelInApp: true, ChannelEmail: true, ChannelSMS: true,
	ChannelFeishu: true, ChannelQQ: true, ChannelDingTalk: true, ChannelBark: true,
}

type List struct {
	ID        uint       `gorm:"primarykey" json:"id"`
	UserID    uint       `gorm:"not null;index:idx_reminder_lists_user_position" json:"-"`
	Name      string     `gorm:"size:40;not null" json:"name"`
	Color     string     `gorm:"size:16;not null;default:blue" json:"color"`
	Icon      string     `gorm:"size:32;not null;default:list" json:"icon"`
	Position  int        `gorm:"not null;default:0;index:idx_reminder_lists_user_position" json:"position"`
	IsDefault bool       `gorm:"not null;default:false" json:"is_default"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
	DeletedAt *time.Time `gorm:"index" json:"-"`
}

type Reminder struct {
	ID         uint       `gorm:"primarykey" json:"id"`
	UserID     uint       `gorm:"not null;index:idx_reminders_user_due;index:idx_reminders_user_completed" json:"-"`
	ListID     uint       `gorm:"not null;index" json:"list_id"`
	Title      string     `gorm:"size:200;not null" json:"title"`
	Notes      string     `gorm:"type:text" json:"notes"`
	Priority   int        `gorm:"not null;default:0" json:"priority"`
	DueAt      *time.Time `gorm:"index:idx_reminders_user_due" json:"due_at"`
	AllDay     bool       `gorm:"not null;default:false" json:"all_day"`
	RepeatRule string     `gorm:"size:16;not null;default:none" json:"repeat_rule"`
	// Calendar selects which calendar the repeat rule follows: "solar"
	// (Gregorian, default) or "lunar" (Chinese lunar calendar). Only
	// monthly/yearly observe the lunar calendar; daily and weekly are
	// calendar-independent and always stay "solar".
	Calendar string `gorm:"size:8;not null;default:solar" json:"calendar"`
	// LunarAnchor stores the lunar month/day this reminder recurs on when
	// Calendar is "lunar" and the rule is monthly/yearly, encoded as "M:D"
	// where a negative month denotes the leap month. For monthly rules only
	// the day part is used. Empty for every other combination.
	LunarAnchor string `gorm:"size:8" json:"lunar_anchor,omitempty"`
	// RepeatNotifyMinutes re-notifies an uncompleted reminder on a fixed
	// interval after each due time; 0 disables the behaviour.
	RepeatNotifyMinutes int `gorm:"not null;default:0" json:"repeat_notify_minutes"`
	// RemindBeforeMinutes fires the first notification this many minutes before
	// the due time. 0 means "exactly at the due time"; new reminders default to 15.
	// 必须带 default：SQLite 给已存在的表新增 NOT NULL 列时要求同时给出默认值，
	// 否则启动迁移直接失败（Cannot add a NOT NULL column with default value NULL）。
	// default 取 0 而不是 15，是因为 GORM 会把「零值」替换成 default——若写成 15，
	// 显式选择的「准时提醒」（0）就永远存不进去。新建时 15 的默认值由服务层决定。
	RemindBeforeMinutes int `gorm:"not null;default:0" json:"remind_before_minutes"`
	// RepeatNotifyTimes caps how many times an overdue reminder re-notifies.
	// 0 keeps the previous behaviour: repeat until the reminder is handled.
	RepeatNotifyTimes int `gorm:"not null;default:0" json:"repeat_notify_times"`
	CompletedAt         *time.Time        `gorm:"index:idx_reminders_user_completed" json:"completed_at"`
	SnoozedUntil        *time.Time        `json:"snoozed_until"`
	Position            int               `gorm:"not null;default:0" json:"position"`
	Version             uint              `gorm:"not null;default:1" json:"version"`
	CreatedAt           time.Time         `json:"created_at"`
	UpdatedAt           time.Time         `json:"updated_at"`
	DeletedAt           *time.Time        `gorm:"index" json:"-"`
	Channels            []ReminderChannel `gorm:"foreignKey:ReminderID" json:"-"`
}

type ReminderChannel struct {
	ID         uint   `gorm:"primarykey" json:"id"`
	ReminderID uint   `gorm:"not null;uniqueIndex:idx_reminder_channel" json:"reminder_id"`
	UserID     uint   `gorm:"not null;index" json:"-"`
	Channel    string `gorm:"size:16;not null;uniqueIndex:idx_reminder_channel" json:"channel"`
	Enabled    bool   `gorm:"not null;default:true" json:"enabled"`
	// Targets stores a JSON array of ChannelBinding IDs this reminder delivers
	// to. Only meaningful for the email channel; empty means every active
	// binding of the user.
	Targets   string    `gorm:"type:text" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

type Completion struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	ReminderID    uint       `gorm:"not null;index" json:"reminder_id"`
	UserID        uint       `gorm:"not null;index" json:"-"`
	ScheduledFor  *time.Time `json:"scheduled_for"`
	CompletedAt   time.Time  `gorm:"index" json:"completed_at"`
	TitleSnapshot string     `gorm:"size:200;not null" json:"title"`
}

type ChannelBinding struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	UserID        uint       `gorm:"not null;index:idx_channel_bindings_user_channel" json:"-"`
	Channel       string     `gorm:"size:16;not null;index:idx_channel_bindings_user_channel" json:"channel"`
	Target        string     `gorm:"type:text;not null" json:"-"`
	TargetMasked  string     `gorm:"size:120;not null" json:"target_masked"`
	Status        string     `gorm:"size:16;not null;default:active;index" json:"status"`
	VerifiedAt    *time.Time `json:"verified_at"`
	LastTestedAt  *time.Time `json:"last_tested_at"`
	LastErrorCode string     `gorm:"size:64" json:"last_error_code,omitempty"`
	LastErrorAt   *time.Time `json:"last_error_at,omitempty"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// ProviderConfig stores administrator-managed provider credentials. The secret
// is encrypted with the installation key and never returned by APIs.
type ProviderConfig struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	Provider  string    `gorm:"size:32;not null;uniqueIndex" json:"provider"`
	Secret    string    `gorm:"type:text;not null" json:"-"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// QQBindCode links a signed-in Reminder user to a QQ C2C conversation without
// ever asking that person to find a platform OpenID.
type QQBindCode struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	UserID    uint      `gorm:"not null;index" json:"-"`
	Code      string    `gorm:"size:12;not null;uniqueIndex" json:"code"`
	ExpiresAt time.Time `gorm:"not null;index" json:"expires_at"`
	CreatedAt time.Time `json:"created_at"`
}

type DeliveryJob struct {
	ID                uint       `gorm:"primarykey" json:"id"`
	UserID            uint       `gorm:"not null;index" json:"-"`
	ReminderID        uint       `gorm:"not null;uniqueIndex:idx_delivery_occurrence" json:"reminder_id"`
	Channel           string     `gorm:"size:16;not null;uniqueIndex:idx_delivery_occurrence;index" json:"channel"`
	ScheduledFor      time.Time  `gorm:"not null;uniqueIndex:idx_delivery_occurrence;index" json:"scheduled_for"`
	RunAt             time.Time  `gorm:"not null;index:idx_delivery_due" json:"run_at"`
	Status            string     `gorm:"size:16;not null;default:pending;index:idx_delivery_due" json:"status"`
	AttemptCount      int        `gorm:"not null;default:0" json:"attempt_count"`
	NextAttemptAt     *time.Time `gorm:"index" json:"next_attempt_at,omitempty"`
	LockedAt          *time.Time `json:"locked_at,omitempty"`
	WorkerID          string     `gorm:"size:64" json:"worker_id,omitempty"`
	IdempotencyKey    string     `gorm:"size:64;not null;uniqueIndex" json:"idempotency_key"`
	LastErrorCode     string     `gorm:"size:64" json:"last_error_code,omitempty"`
	LastErrorMessage  string     `gorm:"size:300" json:"last_error_message,omitempty"`
	ExternalMessageID string     `gorm:"size:200" json:"external_message_id,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
}

type DeliveryAttempt struct {
	ID              uint      `gorm:"primarykey" json:"id"`
	JobID           uint      `gorm:"not null;index" json:"job_id"`
	AttemptNo       int       `gorm:"not null" json:"attempt_no"`
	StartedAt       time.Time `json:"started_at"`
	FinishedAt      time.Time `json:"finished_at"`
	Result          string    `gorm:"size:16;not null" json:"result"`
	ProviderCode    string    `gorm:"size:100" json:"provider_code,omitempty"`
	LatencyMS       int64     `json:"latency_ms"`
	ResponseSummary string    `gorm:"size:300" json:"response_summary,omitempty"`
}

type Notification struct {
	ID            uint       `gorm:"primarykey" json:"id"`
	UserID        uint       `gorm:"not null;index:idx_notifications_user_read" json:"-"`
	ReminderID    *uint      `gorm:"index" json:"reminder_id,omitempty"`
	DeliveryJobID *uint      `gorm:"uniqueIndex" json:"delivery_job_id,omitempty"`
	Type          string     `gorm:"size:32;not null" json:"type"`
	Title         string     `gorm:"size:200;not null" json:"title"`
	Body          string     `gorm:"size:500" json:"body"`
	ReadAt        *time.Time `gorm:"index:idx_notifications_user_read" json:"read_at,omitempty"`
	CreatedAt     time.Time  `gorm:"index" json:"created_at"`
}

type ReminderDTO struct {
	Reminder
	Channels       []string          `json:"channels"`
	ChannelTargets map[string][]uint `json:"channel_targets,omitempty"`
	ListName       string            `json:"list_name"`
}

type ListDTO struct {
	List
	OpenCount int64 `json:"open_count"`
}

type ChannelStatus struct {
	Channel      string `json:"channel"`
	Label        string `json:"label"`
	Configured   bool   `json:"configured"`
	Bound        bool   `json:"bound"`
	Status       string `json:"status"`
	TargetMasked string `json:"target_masked,omitempty"`
	BotLink      string `json:"bot_link,omitempty"`
	Description  string `json:"description"`
	// Bindings lists every bound target for channels that allow more than
	// one (currently email). Each item exposes only the masked address.
	Bindings []ChannelBindingItem `json:"bindings,omitempty"`
}

type ChannelBindingItem struct {
	ID           uint   `json:"id"`
	TargetMasked string `json:"target_masked"`
	Status       string `json:"status"`
}

// Countdown 表示一个「倒数 / 纪念」条目：生日、结婚纪念日、考试倒计时等。
// 不设「正数/倒数」模式字段——目标日期晚于今天就是倒数，早于今天就是已经
// 过去的天数，同一条记录随日期自然切换语义。
type Countdown struct {
	ID     uint   `gorm:"primarykey" json:"id"`
	UserID uint   `gorm:"not null;index" json:"-"`
	Title  string `gorm:"size:60;not null" json:"title"`
	// TargetDate 只承载「哪一天」这个信息，统一按 UTC 零点存储，
	// 这样跨时区比较日期时不会因为本地偏移而差一天。
	TargetDate time.Time `gorm:"not null;index" json:"target_date"`
	// RepeatYearly 为真表示每年重复（生日、纪念日），此时 Calendar 决定
	// 按公历还是农历推算下一次。
	RepeatYearly bool   `gorm:"not null;default:false" json:"repeat_yearly"`
	Calendar     string `gorm:"size:8;not null;default:solar" json:"calendar"`
	// LunarAnchor 存农历锚点 "M:D"（负月份表示闰月），仅农历每年重复时使用。
	LunarAnchor string `gorm:"size:8" json:"lunar_anchor,omitempty"`
	Notes       string `gorm:"type:text" json:"notes"`
	Color       string `gorm:"size:16;not null;default:rose" json:"color"`
	Position    int    `gorm:"not null;default:0" json:"position"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// CalendarFeed 保存一个用户的 ICS 订阅令牌。
//
// 日历客户端（系统日历、Google Calendar 等）无法携带 JWT，鉴权只能靠 URL
// 里的高熵令牌，所以令牌必须可轮换：重置后旧地址立即失效。
type CalendarFeed struct {
	ID        uint      `gorm:"primarykey" json:"id"`
	UserID    uint      `gorm:"not null;uniqueIndex" json:"-"`
	Token     string    `gorm:"size:64;not null;uniqueIndex" json:"token"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}
