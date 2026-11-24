package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helviojunior/mail_migration/pkg/graph"
	"github.com/helviojunior/mail_migration/pkg/log"
	"github.com/helviojunior/mail_migration/pkg/models"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// wellKnownMap are source wellKnownName values we map directly onto the same
// special folder in the destination (avoids display-name localization issues).
var wellKnownMap = map[string]bool{
	"inbox":               true,
	"archive":             true,
	"deleteditems":        true,
	"junkemail":           true,
	"sentitems":           true,
	"drafts":              true,
	"outbox":              true,
	"conversationhistory": true,
}

// Stats collects migration counters.
type Stats struct {
	Folders   int64
	Copied    int64
	Skipped   int64
	Failed    int64
	DateDrift int64 // copies whose received/sent date did not match after fix (verify mode)
}

// dateTolerance is how far a copied timestamp may differ from the source before
// it is reported as drift.
const dateTolerance = 90 * time.Second

// Migrator copies one mailbox from a source tenant to a destination tenant.
type Migrator struct {
	opts    Options
	db      *gorm.DB
	src     *graph.Client
	dst     *graph.Client
	srcUser *graph.User
	dstUser *graph.User

	destRootID string
	dbMu       sync.Mutex
	stats      Stats
}

// NewMigrator resolves both identities and the destination mailbox root.
func NewMigrator(ctx context.Context, opts Options, db *gorm.DB, src, dst *graph.Client) (*Migrator, error) {
	srcUser, err := src.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("source identity: %w", err)
	}
	dstUser, err := dst.Me(ctx)
	if err != nil {
		return nil, fmt.Errorf("destination identity: %w", err)
	}
	root, err := dst.MailboxRoot(ctx)
	if err != nil {
		return nil, fmt.Errorf("destination root folder: %w", err)
	}
	return &Migrator{
		opts: opts, db: db, src: src, dst: dst,
		srcUser: srcUser, dstUser: dstUser, destRootID: root,
	}, nil
}

func (m *Migrator) SourceUser() *graph.User { return m.srcUser }
func (m *Migrator) DestUser() *graph.User   { return m.dstUser }

// Run performs the migration (or a dry run). It returns the collected stats.
func (m *Migrator) Run(ctx context.Context) (Stats, error) {
	folders, err := m.src.ListFolders(ctx)
	if err != nil {
		return m.stats, fmt.Errorf("listing source folders: %w", err)
	}

	mapBySrcID := map[string]string{} // source folder id -> dest folder id

	for _, f := range folders {
		if ctx.Err() != nil {
			return m.stats, ctx.Err()
		}
		if !m.folderSelected(f.Path) {
			log.Debugf("skipping folder (filtered): %s", f.Path)
			continue
		}

		if m.opts.Migrate.DryRun {
			log.Infof("[dry-run] %s  (%d messages)", f.Path, f.TotalItemCount)
			atomic.AddInt64(&m.stats.Folders, 1)
			continue
		}

		destID, err := m.ensureDestFolder(ctx, f, mapBySrcID)
		if err != nil {
			log.Error("failed to create destination folder", "path", f.Path, "err", err)
			atomic.AddInt64(&m.stats.Failed, 1)
			continue
		}
		mapBySrcID[f.ID] = destID
		atomic.AddInt64(&m.stats.Folders, 1)

		m.saveFolderMap(f, destID)

		log.Warnf("Folder: %s (%d messages)", f.Path, f.TotalItemCount)
		if err := m.copyFolderMessages(ctx, f, destID); err != nil {
			if ctx.Err() != nil {
				return m.stats, ctx.Err()
			}
			log.Error("error copying folder", "path", f.Path, "err", err)
		}
	}

	return m.stats, nil
}

// folderSelected applies include/exclude filters (case-insensitive prefix match
// on the folder path).
func (m *Migrator) folderSelected(path string) bool {
	lp := strings.ToLower(path)
	for _, ex := range m.opts.Migrate.ExcludeFolders {
		if matchPath(lp, ex) {
			return false
		}
	}
	if len(m.opts.Migrate.IncludeFolders) == 0 {
		return true
	}
	for _, in := range m.opts.Migrate.IncludeFolders {
		if matchPath(lp, in) {
			return true
		}
	}
	return false
}

func matchPath(lowerPath, filter string) bool {
	lf := strings.ToLower(strings.Trim(filter, "/"))
	return lowerPath == lf || strings.HasPrefix(lowerPath, lf+"/")
}

// ensureDestFolder returns the destination folder id for a source folder,
// creating it (and reusing well-known specials) as needed.
func (m *Migrator) ensureDestFolder(ctx context.Context, src graph.MailFolder, mapBySrcID map[string]string) (string, error) {
	if wk := strings.ToLower(src.WellKnownName); wk != "" && wellKnownMap[wk] {
		f, err := m.dst.FindWellKnownFolder(ctx, wk)
		if err != nil {
			return "", err
		}
		return f.ID, nil
	}

	parentID := m.destRootID
	if p, ok := mapBySrcID[src.ParentFolderID]; ok {
		parentID = p
	}

	if existing, ok, err := m.dst.FindChildByName(ctx, parentID, src.DisplayName); err != nil {
		return "", err
	} else if ok {
		return existing.ID, nil
	}

	created, err := m.dst.CreateChildFolder(ctx, parentID, src.DisplayName)
	if err != nil {
		return "", err
	}
	return created.ID, nil
}

// copyFolderMessages copies every message of a source folder into destID using
// a bounded worker pool.
func (m *Migrator) copyFolderMessages(ctx context.Context, src graph.MailFolder, destID string) error {
	threads := m.opts.Migrate.Threads
	if threads < 1 {
		threads = 1
	}
	sem := make(chan struct{}, threads)
	var wg sync.WaitGroup

	listErr := m.src.ListMessages(ctx, src.ID, func(msg graph.Message) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(msg graph.Message) {
			defer wg.Done()
			defer func() { <-sem }()
			m.copyOne(ctx, src.Path, destID, msg)
		}(msg)
		return nil
	})

	wg.Wait()
	return listErr
}

func (m *Migrator) copyOne(ctx context.Context, folderPath, destID string, msg graph.Message) {
	key := msg.InternetMessageID
	if key == "" {
		key = "src:" + msg.ID
	}
	dedupKey := folderPath + "|" + key

	if !m.opts.Migrate.Force && m.alreadyDone(dedupKey) {
		atomic.AddInt64(&m.stats.Skipped, 1)
		log.Debugf("skip (already migrated): %s", msg.Subject)
		return
	}

	rec := models.MessageMigration{
		DedupKey:      dedupKey,
		SourceMsgID:   msg.ID,
		InternetMsgID: msg.InternetMessageID,
		FolderPath:    folderPath,
		Subject:       truncate(msg.Subject, 500),
		Status:        "pending",
		MigratedAt:    time.Now(),
	}

	mime, err := m.src.GetMIME(ctx, msg.ID)
	if err != nil {
		m.fail(&rec, fmt.Errorf("get MIME: %w", err))
		return
	}
	rec.SizeBytes = int64(len(mime))

	if max := m.opts.Migrate.MaxSizeMB; max > 0 && len(mime) > max*1024*1024 {
		rec.Status = "skipped"
		rec.Error = fmt.Sprintf("message %d bytes exceeds --max-size %d MB", len(mime), max)
		atomic.AddInt64(&m.stats.Skipped, 1)
		m.saveMsg(&rec)
		log.Warnf("skip (too large): %s", msg.Subject)
		return
	}

	newID, err := m.dst.ImportMIME(ctx, destID, mime)
	if err != nil {
		m.fail(&rec, fmt.Errorf("import MIME: %w", err))
		return
	}
	rec.DestMsgID = newID

	// Best-effort metadata restore (read state, flag, categories and — when
	// enabled — the original received/sent timestamps via MAPI extended
	// properties). A failure here is logged but the message still counts as
	// migrated.
	if err := m.dst.PatchMessageMeta(ctx, newID, msg, m.opts.Migrate.FixDates); err != nil {
		log.Debugf("metadata patch failed for %q: %v", msg.Subject, err)
	}

	if m.opts.Migrate.VerifyDates {
		m.verifyDates(ctx, newID, msg)
	}

	rec.Status = "done"
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.Copied, 1)
	m.saveMsg(&rec)
}

func (m *Migrator) fail(rec *models.MessageMigration, err error) {
	rec.Status = "failed"
	rec.Error = err.Error()
	atomic.AddInt64(&m.stats.Failed, 1)
	m.saveMsg(rec)
	log.Error("message failed", "subject", rec.Subject, "err", err)
}

func (m *Migrator) alreadyDone(dedupKey string) bool {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	var count int64
	m.db.Model(&models.MessageMigration{}).
		Where("dedup_key = ? AND status = ?", dedupKey, "done").
		Count(&count)
	return count > 0
}

func (m *Migrator) saveMsg(rec *models.MessageMigration) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	if err := m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "dedup_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"dest_message_id", "status", "error", "size_bytes", "migrated_at"}),
	}).Create(rec).Error; err != nil {
		log.Debugf("db save message: %v", err)
	}
}

func (m *Migrator) saveFolderMap(f graph.MailFolder, destID string) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	rec := models.FolderMap{
		SourceFolderID: f.ID,
		Path:           f.Path,
		DestFolderID:   destID,
		WellKnownName:  f.WellKnownName,
		CreatedAt:      time.Now(),
	}
	if err := m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "source_folder_id"}},
		DoUpdates: clause.AssignmentColumns([]string{"path", "dest_folder_id", "well_known_name"}),
	}).Create(&rec).Error; err != nil {
		log.Debugf("db save folder map: %v", err)
	}
}

// verifyDates re-reads the copied message and reports when its received/sent
// timestamp drifted from the source beyond dateTolerance.
func (m *Migrator) verifyDates(ctx context.Context, destMsgID string, src graph.Message) {
	gotRecv, gotSent, err := m.dst.GetMessageDates(ctx, destMsgID)
	if err != nil {
		log.Debugf("verify-dates read failed for %q: %v", src.Subject, err)
		return
	}
	recvOK := withinTolerance(src.ReceivedDateTime, gotRecv)
	sentOK := withinTolerance(src.SentDateTime, gotSent)
	if !recvOK || !sentOK {
		atomic.AddInt64(&m.stats.DateDrift, 1)
		log.Warnf("date drift on %q: received src=%s dst=%s | sent src=%s dst=%s",
			src.Subject, src.ReceivedDateTime, gotRecv, src.SentDateTime, gotSent)
	}
}

// withinTolerance reports whether two RFC3339 timestamps are equal within
// dateTolerance. Empty source values are treated as a match (nothing to check).
func withinTolerance(src, dst string) bool {
	if src == "" {
		return true
	}
	ts, err1 := time.Parse(time.RFC3339, src)
	td, err2 := time.Parse(time.RFC3339, dst)
	if err1 != nil || err2 != nil {
		return false
	}
	diff := ts.Sub(td)
	if diff < 0 {
		diff = -diff
	}
	return diff <= dateTolerance
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
