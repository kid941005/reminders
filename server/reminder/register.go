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

	database.Upgrades = append(database.Upgrades, database.Upgrade{
		Version: "0.14.2", Name: "disable_inapp_for_imported_reminders",
		// 导入的日程以前默认带站内通知，和日程来源自身的提醒重复，实际没有
		// 作用。这里只清理导入条目；手动创建的提醒保留用户当时的选择。
		Upgrade: func(db *gorm.DB) error {
			imported := `SELECT id FROM reminders WHERE external_source IS NOT NULL AND external_source <> ''`
			if err := db.Exec(`DELETE FROM reminder_channels
				WHERE channel = ? AND reminder_id IN (`+imported+`)`, ChannelInApp).Error; err != nil {
				return err
			}
			// 已经排队的站内投递任务也要取消，否则升级后仍会收到旧日程的
			// 一次性站内推送。已完成的投递记录保留。
			return db.Exec(`UPDATE delivery_jobs
				SET status = 'cancelled', last_error_code = 'CHANNEL_DISABLED',
					last_error_message = '站内通知已改为按需开启'
				WHERE channel = ? AND status IN ('pending', 'retry', 'processing')
				AND reminder_id IN (`+imported+`)`, ChannelInApp).Error
		},
	})

	database.RegisterModels(
		&List{}, &Reminder{}, &ReminderChannel{}, &Completion{},
		&ChannelBinding{}, &ProviderConfig{}, &QQBindCode{}, &DeliveryJob{}, &DeliveryAttempt{}, &Notification{},
		&Countdown{}, &CalendarFeed{}, &CalendarSync{}, &WeatherConfig{},
	)

	sysconfig.RegisterConfig(sysconfig.ConfigDef{
		Key: "reminder_default_channels", Scope: sysconfig.ScopeUser, Type: sysconfig.TypeString,
		Default: "", Group: "notification", Label: "默认提醒渠道",
		Description: "新建提醒时默认勾选的通知方式；留空表示不默认开启任何渠道，站内消息也需要手动选择",
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
		// 天气摘要每天上午推送一次。这里每分钟检查一次，用 LastPushDate 保证
		// 重启后当天不会重复推送。
		Name: "reminder-weather", Interval: time.Minute,
		Run: func() {
			if appDB != nil {
				runDueWeatherPushes(appDB)
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
