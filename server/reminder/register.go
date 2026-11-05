package reminder

import (
	"time"

	"smallgo/server/apps"
	"smallgo/server/database"
	"smallgo/server/scheduler"
	"smallgo/server/sysconfig"

	"gorm.io/gorm"
)

var appDB *gorm.DB

func init() {
	database.Upgrades = append(database.Upgrades, database.Upgrade{
		Version: "0.1.2", Name: "rename_default_site_title",
		Upgrade: func(db *gorm.DB) error {
			// Preserve administrator customisations; only migrate the old default.
			return db.Model(&database.SystemConfig{}).
				Where("user_id = ? AND key = ? AND value = ?", 0, "site_title", "Reminder").
				Update("value", "提醒事项").Error
		},
	})

	database.Upgrades = append(database.Upgrades, database.Upgrade{
		Version: "0.3.0", Name: "allow_multiple_email_bindings",
		// The old unique index on (user_id, channel) capped every channel at
		// one binding; email now keeps one row per receiving address. The
		// replacement composite index is created by AutoMigrate, so only the
		// legacy unique index needs dropping here.
		Upgrade: func(db *gorm.DB) error {
			return db.Exec("DROP INDEX IF EXISTS idx_user_channel_binding").Error
		},
	})

	database.Upgrades = append(database.Upgrades, database.Upgrade{
		Version: "0.3.2", Name: "normalize_lunar_calendar_column",
		// The old "yearly_lunar" rule spelling becomes rule=yearly plus
		// calendar=lunar, matching the new picker model where the calendar
		// is a separate choice from the repeat interval.
		Upgrade: func(db *gorm.DB) error {
			return db.Exec(`UPDATE reminders SET calendar = 'lunar', repeat_rule = 'yearly' WHERE repeat_rule = 'yearly_lunar'`).Error
		},
	})

	database.Upgrades = append(database.Upgrades, database.Upgrade{
		Version: "0.13.0", Name: "normalize_reminder_timestamps_to_utc",
		// 旧版本按客户端传来的偏移原样入库，同一时刻在库里可能是
		// "...T07:00:00+08:00" 也可能是 "...T23:00:00Z" 两种文本。SQLite 按文本比较
		// 时间，两种格式混用会让「今天 / 已过期 / 日历区间」的边界整体偏移一个时区
		// （实测：次日凌晨的提醒被算进今天）。这里把存量行统一改写为 UTC。
		//
		// 只处理会被拿来做边界比较的三列；delivery_jobs 的时间一直由调度器以 UTC
		// 写入，且 scheduled_for 上有唯一索引，改写反而可能与历史行冲突。
		Upgrade: func(db *gorm.DB) error {
			for _, column := range []string{"due_at", "snoozed_until", "completed_at"} {
				// column 来自固定列表，不含用户输入；strftime 无法解析时返回 NULL，
				// 因此额外要求转换结果非空，避免把脏数据改成 NULL。
				stmt := "UPDATE reminders SET " + column +
					" = strftime('%Y-%m-%dT%H:%M:%SZ', " + column + ")" +
					" WHERE " + column + " IS NOT NULL" +
					" AND " + column + " NOT LIKE '%Z'" +
					" AND strftime('%Y-%m-%dT%H:%M:%SZ', " + column + ") IS NOT NULL"
				if err := db.Exec(stmt).Error; err != nil {
					return err
				}
			}
			return nil
		},
	})

	database.RegisterModels(
		&List{}, &Reminder{}, &ReminderChannel{}, &Completion{},
		&ChannelBinding{}, &ProviderConfig{}, &QQBindCode{}, &DeliveryJob{}, &DeliveryAttempt{}, &Notification{},
		&Countdown{}, &CalendarFeed{}, &CalendarSync{},
	)

	sysconfig.RegisterConfig(sysconfig.ConfigDef{
		Key: "reminder_default_channels", Scope: sysconfig.ScopeUser, Type: sysconfig.TypeString,
		Default: "inapp", Group: "notification", Label: "默认提醒渠道",
		Description: "新建提醒时默认勾选的通知方式；未绑定的渠道会自动跳过，站内消息始终保留",
	})
	sysconfig.RegisterConfig(sysconfig.ConfigDef{
		Key: "ics_include_holidays", Scope: sysconfig.ScopeUser, Type: sysconfig.TypeBool,
		Default: "false", Group: "calendar", Label: "订阅包含节日",
		Description: "开启后日历订阅会额外包含节日全天事件（不含二十四节气）",
	})
	sysconfig.RegisterConfig(sysconfig.ConfigDef{
		Key: "reminder_all_day_time", Scope: sysconfig.ScopeUser, Type: sysconfig.TypeString,
		Default: "09:00", Group: "notification", Label: "全天提醒时间",
		Description: "只有日期的提醒在当天何时通知",
	})

	// 删除账号时一并清理本应用的数据（框架不知道这些表）。
	apps.RegisterUserDataCleanup(purgeUserData)

	apps.Register(apps.App{
		Name:        "reminder",
		DisplayName: "提醒事项",
		Icon:        "check-circle",
		RoutePrefix: "/reminders",
		NavPosition: 10,
		// 日历订阅地址必须是免登录的：日历客户端无法携带 JWT。
		Setup:      setupPublicRoutes,
		SetupAuth:  setupAuthRoutes,
		SetupAdmin: setupAdminRoutes,
		Migrate: func(db *gorm.DB) error {
			appDB = db
			return nil
		},
	})

	scheduler.Register(scheduler.Job{
		Name:       "reminder-delivery",
		Interval:   time.Minute,
		RunAtStart: true,
		Run: func() {
			if appDB != nil {
				dispatchDueJobs(appDB)
			}
		},
	})
	scheduler.Register(scheduler.Job{
		// 钉钉日程单向导入。30 分钟一次：CalDAV 没有增量令牌，每次都是窗口内
		// 全量拉取，频率再高只是重复消耗对端。
		Name: "reminder-calendar-sync", Interval: 30 * time.Minute,
		Run: func() {
			if appDB != nil {
				SyncAllCalendars(appDB)
			}
		},
	})
	scheduler.Register(scheduler.Job{
		Name: "reminder-qq-gateway", Interval: 30 * time.Second,
		Run: func() {
			if appDB != nil {
				ensureQQGateway(appDB)
			}
		},
	})
}
