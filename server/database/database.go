package database

import (
	"context"
	"log"
	"strings"
	"time"

	"smallgo/server/logger"

	"gorm.io/driver/sqlite"
	"gorm.io/gorm"
	gormlogger "gorm.io/gorm/logger"
)

// coreModels are the framework's own tables, always migrated on startup.
var coreModels = []interface{}{
	&User{},
	&SystemConfig{},
	&UpgradeRecord{},
	&SecurityQuestion{},
	&AuditLog{},
}

// appModels holds models contributed by registered apps via RegisterModels.
// Apps add to this slice from init(); AutoMigrate iterates both coreModels
// and appModels together so an app's schema lands on the same startup pass
// as the framework's own tables.
var appModels []interface{}

// RegisterModels adds GORM-managed models to the migration roster. Call
// from an app's init(); it is safe to call multiple times across packages.
//
//	func init() {
//	    database.RegisterModels(&Note{}, &Tag{})
//	}
func RegisterModels(models ...interface{}) {
	appModels = append(appModels, models...)
}

// Options tunes the SQLite connection pool. Zero values pick the defaults
// below, so callers that only need InitDB can ignore this type entirely.
type Options struct {
	MaxOpenConns  int
	MaxIdleConns  int
	BusyTimeout   time.Duration
	SlowThreshold time.Duration
}

func (o Options) withDefaults() Options {
	if o.MaxOpenConns <= 0 {
		o.MaxOpenConns = 4
	}
	if o.MaxIdleConns <= 0 {
		o.MaxIdleConns = o.MaxOpenConns
	}
	if o.BusyTimeout <= 0 {
		o.BusyTimeout = 10 * time.Second
	}
	if o.SlowThreshold <= 0 {
		o.SlowThreshold = 500 * time.Millisecond
	}
	return o
}

func InitDB(dbPath string) (*gorm.DB, error) {
	return InitDBWithOptions(dbPath, Options{})
}

// InitDBWithOptions opens the database with an explicit pool configuration.
//
// WAL allows concurrent readers while a writer commits. MaxOpenConns > 1 lets
// read-only API calls proceed in parallel; SQLite still serializes writers, and
// busy_timeout makes a blocked writer wait for the lock instead of failing with
// SQLITE_BUSY. The old fixed MaxOpenConns(1) turned every read into a global
// bottleneck for no reason.
func InitDBWithOptions(dbPath string, opts Options) (*gorm.DB, error) {
	opts = opts.withDefaults()
	// An in-memory database lives inside one connection; pooling would give
	// each connection its own empty database, so force a single connection.
	if dbPath == ":memory:" || strings.HasPrefix(dbPath, "file::memory:") {
		opts.MaxOpenConns = 1
		opts.MaxIdleConns = 1
	}

	// Keep GORM quiet about expected "record not found" lookups (used heavily
	// for upsert-style config init) while still surfacing slow queries/errors.
	gormLog := gormlogger.New(log.New(logger.NewWriter("WARN"), "", 0), gormlogger.Config{
		SlowThreshold:             opts.SlowThreshold,
		LogLevel:                  gormlogger.Warn,
		IgnoreRecordNotFoundError: true,
	})

	// Embed pragmas in the DSN so every pooled connection inherits them,
	// not only the connection that happened to run the PRAGMA exec below.
	dsn := dbPath +
		"?_pragma=journal_mode(WAL)" +
		"&_pragma=busy_timeout(" + itoa(int(opts.BusyTimeout.Milliseconds())) + ")" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(ON)"

	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{Logger: gormLog})
	if err != nil {
		return nil, err
	}

	sqlDB, err := db.DB()
	if err != nil {
		return nil, err
	}
	sqlDB.SetMaxOpenConns(opts.MaxOpenConns)
	sqlDB.SetMaxIdleConns(opts.MaxIdleConns)
	sqlDB.SetConnMaxLifetime(time.Hour)

	return db, nil
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// AutoMigrate runs GORM's AutoMigrate across all framework models and any
// additional models contributed by apps via RegisterModels.
func AutoMigrate(db *gorm.DB) error {
	models := make([]interface{}, 0, len(coreModels)+len(appModels))
	models = append(models, coreModels...)
	models = append(models, appModels...)
	return db.AutoMigrate(models...)
}

func CloseDB(db *gorm.DB) error {
	sqlDB, err := db.DB()
	if err != nil {
		return err
	}
	return sqlDB.Close()
}

// Health is the database probe result used by /api/health.
type Health struct {
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

// HealthCheck pings the database with a short deadline so a hung connection
// surfaces as an unhealthy report instead of blocking the health handler.
func HealthCheck(db *gorm.DB) Health {
	if db == nil {
		return Health{OK: false, Error: "数据库未初始化"}
	}
	sqlDB, err := db.DB()
	if err != nil {
		return Health{OK: false, Error: err.Error()}
	}
	start := time.Now()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := sqlDB.PingContext(ctx); err != nil {
		return Health{OK: false, LatencyMS: time.Since(start).Milliseconds(), Error: err.Error()}
	}
	return Health{OK: true, LatencyMS: time.Since(start).Milliseconds()}
}
