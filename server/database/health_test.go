package database

import (
	"path/filepath"
	"testing"
)

func TestInitDBOptionsAndHealthCheck(t *testing.T) {
	db, err := InitDBWithOptions(filepath.Join(t.TempDir(), "health.db"), Options{MaxOpenConns: 3, MaxIdleConns: 2})
	if err != nil {
		t.Fatal(err)
	}
	defer CloseDB(db)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	if got := sqlDB.Stats().MaxOpenConnections; got != 3 {
		t.Fatalf("MaxOpenConnections=%d want 3", got)
	}
	if h := HealthCheck(db); !h.OK {
		t.Fatalf("expected healthy database, got %+v", h)
	}
}

func TestHealthCheckWithoutDatabase(t *testing.T) {
	if h := HealthCheck(nil); h.OK {
		t.Fatalf("nil database should be unhealthy: %+v", h)
	}
}
