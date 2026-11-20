package user

import (
	"errors"
	"path/filepath"
	"testing"

	"smallgo/server/database"

	"gorm.io/gorm"
)

func newUserTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.InitDB(filepath.Join(t.TempDir(), "user.db"))
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB(db) })
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

func TestRegisterTrimsUsernameAndRejectsInvalid(t *testing.T) {
	db := newUserTestDB(t)

	result, err := Register(db, "  alice  ", "secret1", "jwt-secret")
	if err != nil {
		t.Fatalf("register: %v", err)
	}
	user := result["user"].(map[string]interface{})
	if user["username"] != "alice" {
		t.Fatalf("username = %v, want alice", user["username"])
	}

	for _, name := range []string{"", "   "} {
		if _, err := Register(db, name, "secret1", "jwt-secret"); !errors.Is(err, ErrUsernameInvalid) {
			t.Fatalf("Register(%q) error = %v, want ErrUsernameInvalid", name, err)
		}
	}
	long := make([]rune, 51)
	for i := range long {
		long[i] = 'a'
	}
	if _, err := Register(db, string(long), "secret1", "jwt-secret"); !errors.Is(err, ErrUsernameInvalid) {
		t.Fatalf("long username error = %v, want ErrUsernameInvalid", err)
	}
}
