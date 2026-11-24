package database

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/helviojunior/mail_migration/pkg/models"
)

func TestConnectSqliteAndMigrate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "state.db")
	uri := "sqlite://" + dbPath // absolute temp dir -> sqlite:///abs/...

	db, err := Connect(uri, false)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}

	// AutoMigrate must have created every state table.
	for _, m := range models.All() {
		if !db.Migrator().HasTable(m) {
			t.Fatalf("expected table for %T to exist", m)
		}
	}

	// Round-trip an account through the real driver.
	want := models.Account{
		Role:      "source",
		UPN:       "user@contoso.onmicrosoft.com",
		UserID:    "abc123",
		Tenant:    "contoso.onmicrosoft.com",
		CacheFile: "/tmp/source.json",
		UpdatedAt: time.Now(),
	}
	if err := db.Create(&want).Error; err != nil {
		t.Fatalf("create account: %v", err)
	}

	var got models.Account
	if err := db.Where("role = ?", "source").First(&got).Error; err != nil {
		t.Fatalf("read account: %v", err)
	}
	if got.UPN != want.UPN || got.Tenant != want.Tenant {
		t.Fatalf("round-trip mismatch: got %+v", got)
	}
}

func TestConnectUnsupportedURI(t *testing.T) {
	if _, err := Connect("redis://localhost", false); err == nil {
		t.Fatal("expected error for unsupported URI scheme")
	}
}
