package runner

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/models"
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
	Folders    int64
	Copied     int64
	Skipped    int64
	Failed     int64
	DateDrift  int64 // copies whose received/sent date did not match after fix (verify mode)
	MetaFailed int64 // copies whose metadata PATCH (read/flags/dates) failed
	SaveFailed int64 // state-DB writes that failed (resume/dedup silently degraded)

	// Mailbox settings (final step).
	SettingsRun bool
	RulesCopied int64
	RulesFailed int64
	AutoReply   string // "applied", "skipped (none)", "failed" or ""

	// Calendars & events (final step).
	CalendarsRun    bool
	CalendarsCopied int64 // destination calendars matched or created
	EventsCopied    int64
	EventsSkipped   int64
	EventsFailed    int64
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
	progress   *ascii.Progress
	sem        chan struct{}     // global cap on concurrent message operations
	folderMap  map[string]string // source folder id -> dest folder id (for rule remapping)
}

// folderJob pairs a source folder with its resolved destination folder id.
type folderJob struct {
	folder graph.MailFolder
	destID string
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
//
// It works in two phases: first the destination folder tree is created
// sequentially (so parents exist before children), then messages are copied
// with several folders in flight at once — each in-flight folder gets its own
// live progress bar, while a global semaphore caps concurrent Graph operations.
func (m *Migrator) Run(ctx context.Context) (Stats, error) {
	folders, err := m.src.ListFolders(ctx)
	if err != nil {
		return m.stats, fmt.Errorf("listing source folders: %w", err)
	}

	if m.opts.Migrate.DryRun {
		for _, f := range folders {
			if m.folderSelected(f.Path) {
				log.Infof("[dry-run] %s  (%d messages)", f.Path, f.TotalItemCount)
				atomic.AddInt64(&m.stats.Folders, 1)
			}
		}
		return m.stats, nil
	}

	// Phase 1: create the destination folder tree (sequential, parents first).
	mapBySrcID := map[string]string{}
	var jobs []folderJob
	var grandTotal int64
	for _, f := range folders {
		if ctx.Err() != nil {
			return m.stats, ctx.Err()
		}
		if !m.folderSelected(f.Path) {
			log.Debugf("skipping folder (filtered): %s", f.Path)
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
		jobs = append(jobs, folderJob{folder: f, destID: destID})
		grandTotal += int64(f.TotalItemCount)
	}

	m.folderMap = mapBySrcID

	// Phase 2: copy messages, several folders at a time.
	m.progress = ascii.NewProgress(grandTotal, !m.opts.Logging.Silence)
	m.progress.Start()
	defer m.progress.Finish()

	threads := m.opts.Migrate.Threads
	if threads < 1 {
		threads = 1
	}
	m.sem = make(chan struct{}, threads)

	folderConc := m.opts.Migrate.FolderConcurrency
	if folderConc < 1 {
		folderConc = 1
	}
	folderSem := make(chan struct{}, folderConc)
	var wg sync.WaitGroup

	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		folderSem <- struct{}{}
		go func(j folderJob) {
			defer wg.Done()
			defer func() { <-folderSem }()

			bar := m.progress.StartFolder(j.folder.Path, j.folder.TotalItemCount)
			defer bar.Done()

			if !m.progress.Enabled() {
				log.Warnf("Folder: %s (%d messages)", j.folder.Path, j.folder.TotalItemCount)
			}
			if err := m.copyFolderMessages(ctx, j.folder, j.destID, bar); err != nil {
				if ctx.Err() == nil && !m.progress.SuppressLogs() {
					log.Error("error copying folder", "path", j.folder.Path, "err", err)
				}
			}
		}(j)
	}
	wg.Wait()

	if ctx.Err() != nil {
		return m.stats, ctx.Err()
	}
	return m.stats, nil
}

// MigrateSettings copies mailbox settings as a final step: inbox rules (with
// folder references remapped to the destination) and automatic replies. It is
// called after Run, when the live progress area is no longer active, so its logs
// are clean.
func (m *Migrator) MigrateSettings(ctx context.Context) Stats {
	m.stats.SettingsRun = true
	log.Warn("Migrating mailbox settings (rules, automatic replies)...")

	// 1. Inbox rules.
	if rules, err := m.src.GetInboxRules(ctx); err != nil {
		log.Error("could not read source inbox rules", "err", err)
	} else {
		for _, r := range rules {
			delete(r, "id")
			delete(r, "@odata.context")
			delete(r, "@odata.type")
			m.remapRuleFolders(r)
			if err := m.dst.CreateInboxRule(ctx, r); err != nil {
				m.stats.RulesFailed++
				log.Warnf("rule %q not created: %v", ruleName(r), err)
			} else {
				m.stats.RulesCopied++
			}
		}
		log.Warnf("Inbox rules: %d copied, %d failed", m.stats.RulesCopied, m.stats.RulesFailed)
	}

	// 2. Automatic replies (out-of-office).
	if ar, err := m.src.GetAutomaticReplies(ctx); err != nil {
		log.Warnf("could not read automatic replies: %v", err)
		m.stats.AutoReply = "failed (read)"
	} else {
		status, _ := ar["status"].(string)
		delete(ar, "@odata.context")
		if err := m.dst.SetAutomaticReplies(ctx, ar); err != nil {
			log.Warnf("automatic replies not applied: %v", err)
			m.stats.AutoReply = "failed"
		} else {
			m.stats.AutoReply = fmt.Sprintf("applied (%s)", status)
			log.Warnf("Automatic replies: applied (status=%s)", status)
		}
	}

	return m.stats
}

// remapRuleFolders rewrites folder ids in a rule's move/copy actions from source
// to destination; an action referencing a folder that was not migrated is
// dropped so the rest of the rule can still be created.
func (m *Migrator) remapRuleFolders(rule map[string]interface{}) {
	actions, ok := rule["actions"].(map[string]interface{})
	if !ok {
		return
	}
	for _, key := range []string{"moveToFolder", "copyToFolder"} {
		id, ok := actions[key].(string)
		if !ok {
			continue
		}
		if dest, ok := m.folderMap[id]; ok {
			actions[key] = dest
		} else {
			delete(actions, key)
			log.Debugf("rule action %q references an unmigrated folder; dropped", key)
		}
	}
}

func ruleName(rule map[string]interface{}) string {
	if n, ok := rule["displayName"].(string); ok {
		return n
	}
	return "(unnamed)"
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

// copyFolderMessages copies every message of a source folder into destID. Each
// message copy is gated by the migrator's global semaphore so total concurrency
// stays bounded even with several folders in flight.
func (m *Migrator) copyFolderMessages(ctx context.Context, src graph.MailFolder, destID string, bar *ascii.FolderBar) error {
	// Pre-scan the destination folder and map each internetMessageId to the
	// destination message id(s) that carry it. Two uses:
	//   - normal run: skip messages already there (dedup against the real
	//     destination, independent of the local state DB);
	//   - --force run: delete those existing copies in copyOne and re-import,
	//     so a re-run replaces (e.g. to fix messages stuck as drafts).
	// It therefore runs whenever we check the destination OR force is set.
	var existing map[string][]string
	if m.opts.Migrate.CheckDestination || m.opts.Migrate.Force {
		if ids, err := m.dst.ListInternetMessageIDs(ctx, destID); err != nil {
			log.Debugf("destination pre-scan failed for %s: %v", src.Path, err)
		} else {
			existing = ids
		}
	}
	isDraft := strings.EqualFold(src.WellKnownName, "drafts")

	var wg sync.WaitGroup
	listErr := m.src.ListMessages(ctx, src.ID, func(msg graph.Message) error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		wg.Add(1)
		select {
		case m.sem <- struct{}{}:
		case <-ctx.Done():
			wg.Done()
			return ctx.Err()
		}
		go func(msg graph.Message) {
			defer wg.Done()
			defer func() { <-m.sem }()
			m.copyOne(ctx, src.Path, destID, msg, bar, existing, isDraft)
		}(msg)
		return nil
	})

	wg.Wait()
	return listErr
}

// retry runs fn and, on a transient-looking error, retries it up to
// opts.Migrate.Retries more times, sleeping opts.Migrate.RetryDelay between
// attempts (so Retries=5 means up to 6 attempts total). Many Graph errors —
// timeouts, sporadic 500s, "failed to get the correct properties" — are
// transient and clear on retry. It does NOT retry a cancelled context or a 404
// (the target object is gone, so retrying is pointless). This is independent of
// --force. Returns the last error after the attempts are exhausted.
func (m *Migrator) retry(ctx context.Context, what string, fn func() error) error {
	var err error
	for attempt := 0; ; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		// Don't spin on cancellation or on a permanent client error (4xx such as
		// 400/403/404): those never succeed on retry and only waste the delay.
		if ctx.Err() != nil || graph.IsPermanent(err) {
			return err
		}
		if attempt >= m.opts.Migrate.Retries {
			return err
		}
		if !m.progress.SuppressLogs() {
			log.Debugf("retry %d/%d (%s): %v", attempt+1, m.opts.Migrate.Retries, what, err)
		}
		if !sleepCtx(ctx, m.opts.Migrate.RetryDelay) {
			return err
		}
	}
}

// sleepCtx waits for d or until ctx is cancelled; returns true if the full wait
// elapsed, false if cancelled.
func sleepCtx(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return true
	}
	select {
	case <-ctx.Done():
		return false
	case <-time.After(d):
		return true
	}
}

func (m *Migrator) copyOne(ctx context.Context, folderPath, destID string, msg graph.Message, bar *ascii.FolderBar, existing map[string][]string, isDraft bool) {
	defer bar.Inc() // exactly one tick per message, whatever the outcome

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

	// Already present in the destination folder.
	if existing != nil && msg.InternetMessageID != "" && len(existing[msg.InternetMessageID]) > 0 {
		dupIDs := existing[msg.InternetMessageID]
		if m.opts.Migrate.Force {
			// --force: delete the existing copy/copies, then re-import below so
			// the message is replaced (e.g. to fix copies stuck as drafts).
			for _, id := range dupIDs {
				id := id
				if err := m.retry(ctx, "delete existing: "+msg.Subject, func() error {
					return m.dst.DeleteMessage(ctx, id)
				}); err != nil {
					// A 404 means the copy is already gone (e.g. an auto-processed
					// meeting response, or a duplicate another worker just
					// removed). The delete's goal is met, so re-import anyway.
					if graph.IsNotFound(err) {
						continue
					}
					// Any other failure would leave a duplicate; surface it and
					// skip the re-import so we don't knowingly create one.
					atomic.AddInt64(&m.stats.Failed, 1)
					m.saveMsg(&models.MessageMigration{
						DedupKey: dedupKey, SourceMsgID: msg.ID, InternetMsgID: msg.InternetMessageID,
						FolderPath: folderPath, Subject: truncate(msg.Subject, 500),
						Status: "failed", Error: truncate(fmt.Sprintf("delete existing before re-import: %v", err), 1000),
						MigratedAt: time.Now(),
					})
					if !m.progress.SuppressLogs() {
						log.Warnf("force: could not delete existing %q, skipping re-import: %v", msg.Subject, err)
					}
					return
				}
			}
			log.Debugf("force: deleted %d existing copy/copies before re-import: %s", len(dupIDs), msg.Subject)
			// fall through to import a fresh, non-draft copy
		} else {
			// Normal run: skip and backfill local state.
			atomic.AddInt64(&m.stats.Skipped, 1)
			m.saveMsg(&models.MessageMigration{
				DedupKey: dedupKey, SourceMsgID: msg.ID, InternetMsgID: msg.InternetMessageID,
				FolderPath: folderPath, Subject: truncate(msg.Subject, 500),
				Status: "done", MigratedAt: time.Now(),
			})
			log.Debugf("skip (already in destination): %s", msg.Subject)
			return
		}
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

	var mime []byte
	if err := m.retry(ctx, "get MIME: "+msg.Subject, func() (e error) {
		mime, e = m.src.GetMIME(ctx, msg.ID)
		return e
	}); err != nil {
		m.fail(bar, &rec, fmt.Errorf("get MIME: %w", err))
		return
	}
	rec.SizeBytes = int64(len(mime))

	if max := m.opts.Migrate.MaxSizeMB; max > 0 && len(mime) > max*1024*1024 {
		rec.Status = "skipped"
		rec.Error = fmt.Sprintf("message %d bytes exceeds --max-size %d MB", len(mime), max)
		atomic.AddInt64(&m.stats.Skipped, 1)
		m.saveMsg(&rec)
		if !m.progress.SuppressLogs() {
			log.Warnf("skip (too large): %s", msg.Subject)
		}
		return
	}

	var newID string
	if err := m.retry(ctx, "import MIME: "+msg.Subject, func() (e error) {
		newID, e = m.dst.ImportMIME(ctx, destID, mime, isDraft)
		return e
	}); err != nil {
		m.fail(bar, &rec, fmt.Errorf("import MIME: %w", err))
		return
	}
	rec.DestMsgID = newID

	// Best-effort metadata restore (read state, flag, categories and — when
	// enabled — the original received/sent timestamps via MAPI extended
	// properties). A failure here is logged but the message still counts as
	// migrated.
	if err := m.dst.PatchMessageMeta(ctx, newID, msg, m.opts.Migrate.FixDates); err != nil {
		atomic.AddInt64(&m.stats.MetaFailed, 1)
		if !m.progress.SuppressLogs() {
			log.Warnf("metadata patch failed for %q: %v", msg.Subject, err)
		}
	}

	if m.opts.Migrate.VerifyDates {
		m.verifyDates(ctx, newID, msg)
	}

	rec.Status = "done"
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.Copied, 1)
	m.saveMsg(&rec)
}

func (m *Migrator) fail(bar *ascii.FolderBar, rec *models.MessageMigration, err error) {
	rec.Status = "failed"
	rec.Error = err.Error()
	atomic.AddInt64(&m.stats.Failed, 1)
	bar.Fail()
	m.saveMsg(rec)
	if !m.progress.SuppressLogs() {
		log.Error("message failed", "subject", rec.Subject, "err", err)
	}
}

// RetryFailed re-attempts only the messages currently recorded as "failed" in
// the state DB (independent of a normal run). It reuses the folder mapping
// already recorded, so it creates no folders and scans no source folders. Each
// message still gets the per-message --retries. Any message that fails AGAIN is
// listed verbosely (subject, timestamp, source id, error) and left as failed.
func (m *Migrator) RetryFailed(ctx context.Context) (Stats, error) {
	var failed []models.MessageMigration
	if err := m.db.Where("status = ?", "failed").Order("folder_path").Find(&failed).Error; err != nil {
		return m.stats, fmt.Errorf("loading failed messages: %w", err)
	}
	if len(failed) == 0 {
		log.Info("retry-failed: no messages recorded as failed; nothing to do")
		return m.stats, nil
	}

	// Resolve folder path -> (destination folder id, isDraft) from folder_maps.
	var fmaps []models.FolderMap
	if err := m.db.Find(&fmaps).Error; err != nil {
		return m.stats, fmt.Errorf("loading folder maps: %w", err)
	}
	type destFolder struct {
		id      string
		isDraft bool
	}
	byPath := map[string]destFolder{}
	for _, f := range fmaps {
		byPath[f.Path] = destFolder{id: f.DestFolderID, isDraft: strings.EqualFold(f.WellKnownName, "drafts")}
	}

	log.Warnf("retry-failed: re-attempting %d failed message(s)...", len(failed))

	var still []failedReport
	for i := range failed {
		if ctx.Err() != nil {
			break
		}
		rec := failed[i]
		df, ok := byPath[rec.FolderPath]
		if !ok || df.id == "" {
			// The source message was never fetched, so there is no Outlook date.
			m.markFailed(&rec, fmt.Errorf("no destination folder mapping for %q (re-run a normal migrate first)", rec.FolderPath))
			still = append(still, failedReport{rec: rec, date: "N/A"})
			continue
		}
		if date, err := m.retryFailedOne(ctx, &rec, df.id, df.isDraft); err != nil {
			still = append(still, failedReport{rec: rec, date: date})
		}
	}

	m.reportStillFailed(still)
	return m.stats, ctx.Err()
}

// failedReport pairs a still-failed message with its Outlook received date (or
// "N/A" when the message itself could not be fetched) for the verbose report.
type failedReport struct {
	rec  models.MessageMigration
	date string
}

// retryFailedOne re-drives a single failed message's copy from its stored source
// id. On success the row flips to "done"; on failure it stays "failed" (updated
// error) and the error is returned. The first return value is the message's
// Outlook received date formatted for the report, or "N/A" if the message could
// not be fetched.
func (m *Migrator) retryFailedOne(ctx context.Context, rec *models.MessageMigration, destID string, isDraft bool) (string, error) {
	var msg graph.Message
	if err := m.retry(ctx, "get message: "+rec.Subject, func() (e error) {
		msg, e = m.src.GetMessage(ctx, rec.SourceMsgID)
		return e
	}); err != nil {
		// Could not fetch the message -> no Outlook date to show.
		return "N/A", m.markFailed(rec, fmt.Errorf("get source message: %w", err))
	}
	date := outlookDate(msg.ReceivedDateTime)

	var mime []byte
	if err := m.retry(ctx, "get MIME: "+rec.Subject, func() (e error) {
		mime, e = m.src.GetMIME(ctx, rec.SourceMsgID)
		return e
	}); err != nil {
		return date, m.markFailed(rec, fmt.Errorf("get MIME: %w", err))
	}

	var newID string
	if err := m.retry(ctx, "import MIME: "+rec.Subject, func() (e error) {
		newID, e = m.dst.ImportMIME(ctx, destID, mime, isDraft)
		return e
	}); err != nil {
		return date, m.markFailed(rec, fmt.Errorf("import MIME: %w", err))
	}

	if err := m.dst.PatchMessageMeta(ctx, newID, msg, m.opts.Migrate.FixDates); err != nil {
		atomic.AddInt64(&m.stats.MetaFailed, 1)
		log.Warnf("retry-failed: metadata patch failed for %q: %v", rec.Subject, err)
	}

	rec.DestMsgID = newID
	rec.SizeBytes = int64(len(mime))
	rec.Status = "done"
	rec.Error = ""
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.Copied, 1)
	m.saveMsg(rec)
	log.Infof("retry-failed: recovered %q", rec.Subject)
	return date, nil
}

// outlookDate formats a Graph receivedDateTime (RFC3339, UTC) the way Outlook
// shows it — in local time. It returns "N/A" when the value is missing.
func outlookDate(iso string) string {
	if iso == "" {
		return "N/A"
	}
	if t, err := time.Parse(time.RFC3339, iso); err == nil {
		return t.Local().Format("2006-01-02 15:04:05")
	}
	return iso
}

// markFailed records rec as failed with err and counts it.
func (m *Migrator) markFailed(rec *models.MessageMigration, err error) error {
	rec.Status = "failed"
	rec.Error = truncate(err.Error(), 1000)
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.Failed, 1)
	m.saveMsg(rec)
	return err
}

// reportStillFailed prints a verbose, per-message block for messages that failed
// again during retry-failed: subject, folder path, Outlook date, source id and
// error.
func (m *Migrator) reportStillFailed(still []failedReport) {
	if len(still) == 0 {
		log.Warn("retry-failed: all retried messages were recovered")
		return
	}
	var b strings.Builder
	fmt.Fprintf(&b, "## ⚠️ %d message(s) still failed after retry\n\n", len(still))
	for _, f := range still {
		r := f.rec
		subject := r.Subject
		if subject == "" {
			subject = "(no subject)"
		}
		path := r.FolderPath
		if path == "" {
			path = "(unknown folder)"
		}
		errMsg := r.Error
		if errMsg == "" {
			errMsg = "(no error recorded)"
		}
		fmt.Fprintf(&b, "- **%s**\n  - Folder: `%s`\n  - Date/time: %s\n  - ID: `%s`\n  - Error: %s\n",
			subject, path, f.date, r.SourceMsgID, errMsg)
	}
	fmt.Println(ascii.Markdown(b.String()))
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
	// NOTE: these are DB column names (GORM derives them from the Go field
	// names, not the json tags). DestMsgID -> dest_msg_id, NOT dest_message_id:
	// the latter does not exist, and referencing it made the upsert's
	// `excluded.<col>` clause fail at prepare time, silently dropping every
	// message row (folder_maps uses only valid columns, which is why it saved).
	if err := m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "dedup_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"dest_msg_id", "status", "error", "size_bytes", "migrated_at"}),
	}).Create(rec).Error; err != nil {
		// A failed state write is not cosmetic: it silently disables resume and
		// local dedup, so the next run re-scans and re-copies everything. Make
		// it visible (and counted) instead of hiding it at debug level.
		atomic.AddInt64(&m.stats.SaveFailed, 1)
		log.Warnf("state DB write failed for %q (resume/dedup degraded): %v", rec.DedupKey, err)
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
		if m.progress.SuppressLogs() {
			return
		}
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
