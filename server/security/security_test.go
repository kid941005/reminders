package security

import (
	"errors"
	"path/filepath"
	"testing"

	"smallgo/server/database"

	"gorm.io/gorm"
)

func newSecurityTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := database.InitDB(filepath.Join(t.TempDir(), "security.db"))
	if err != nil {
		t.Fatalf("init db: %v", err)
	}
	t.Cleanup(func() { _ = database.CloseDB(db) })
	if err := database.AutoMigrate(db); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	return db
}

// TestVerifyAnswersDoesNotRevealAccountState ensures an unknown username and an
// account without security questions produce the same error as a wrong answer,
// so the public forgot-password flow cannot be used to enumerate users.
func TestVerifyAnswersDoesNotRevealAccountState(t *testing.T) {
	db := newSecurityTestDB(t)
	if err := db.Create(&database.User{Username: "noquestions", Password: "x", Status: 1}).Error; err != nil {
		t.Fatal(err)
	}

	for _, username := range []string{"ghost", "noquestions"} {
		err := VerifyAnswers(db, username, "a", "b", "c", "", "", "")
		if !errors.Is(err, ErrSecurityAnswerMismatch) {
			t.Fatalf("VerifyAnswers(%q) = %v, want ErrSecurityAnswerMismatch", username, err)
		}
		if err.Error() != ErrSecurityAnswerMismatch.Error() {
			t.Fatalf("VerifyAnswers(%q) leaked a distinct message: %v", username, err)
		}
	}
}
