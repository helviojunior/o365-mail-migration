// Package models holds the gorm-backed state used to make migrations
// resumable and idempotent.
package models

import "time"

// Account records an authenticated mailbox (source or destination). The MSAL
// token cache itself lives in a separate file; this only keeps identity.
type Account struct {
	ID        uint      `gorm:"primaryKey" json:"id"`
	Role      string    `gorm:"uniqueIndex;size:16" json:"role"` // "source" | "destination"
	HomeID    string    `gorm:"size:255" json:"home_account_id"` // MSAL account identifier
	UPN       string    `gorm:"size:320" json:"upn"`
	UserID    string    `gorm:"size:64" json:"user_id"`
	Tenant    string    `gorm:"size:64" json:"tenant"`
	CacheFile string    `gorm:"size:1024" json:"cache_file"`
	UpdatedAt time.Time `json:"updated_at"`
}

// FolderMap records the source→destination folder relationship so a resumed
// run reuses the already-created destination folders.
type FolderMap struct {
	ID             uint      `gorm:"primaryKey" json:"id"`
	SourceFolderID string    `gorm:"uniqueIndex;size:512" json:"source_folder_id"`
	Path           string    `gorm:"size:1024;index" json:"path"`
	DestFolderID   string    `gorm:"size:512" json:"dest_folder_id"`
	WellKnownName  string    `gorm:"size:64" json:"well_known_name"`
	CreatedAt      time.Time `json:"created_at"`
}

// MessageMigration tracks the state of a single message copy. Dedup is keyed by
// the RFC822 Internet-Message-ID when present, falling back to the source id.
type MessageMigration struct {
	ID            uint      `gorm:"primaryKey" json:"id"`
	DedupKey      string    `gorm:"uniqueIndex;size:512" json:"dedup_key"`
	SourceMsgID   string    `gorm:"size:512" json:"source_message_id"`
	InternetMsgID string    `gorm:"size:512;index" json:"internet_message_id"`
	FolderPath    string    `gorm:"size:1024;index" json:"folder_path"`
	DestMsgID     string    `gorm:"size:512" json:"dest_message_id"`
	Subject       string    `gorm:"size:512" json:"subject"`
	SizeBytes     int64     `json:"size_bytes"`
	Status        string    `gorm:"size:16;index" json:"status"` // pending | done | failed | skipped
	Error         string    `gorm:"type:text" json:"error"`
	MigratedAt    time.Time `json:"migrated_at"`
}

// All returns every model for AutoMigrate.
func All() []interface{} {
	return []interface{}{&Account{}, &FolderMap{}, &MessageMigration{}}
}
