package runner

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/models"
	"gorm.io/gorm/clause"
)

// errReadOnlyDestCalendar signals that the destination calendar matching a
// source calendar cannot be written to (e.g. a holidays or birthdays calendar),
// so it is skipped instead of failing every event with a 400.
var errReadOnlyDestCalendar = errors.New("destination calendar is read-only")

// MigrateCalendars copies calendars and their events. It is the first step after
// the mailbox copy. For each source calendar it finds or creates the matching
// destination calendar (default maps to default), then copies every event
// (single instances and recurring series masters; individual occurrences are
// regenerated from their series). The copy is resumable and deduped via a local
// state table, so re-runs do not duplicate; --force deletes the previous copy
// and recreates. It shows a live progress area (one bar per calendar) like the
// mailbox phase. Failures are logged and counted, never fatal.
func (m *Migrator) MigrateCalendars(ctx context.Context) Stats {
	m.stats.CalendarsRun = true
	log.Warn("Migrating calendars and events...")

	srcCals, err := m.src.ListCalendars(ctx)
	if err != nil {
		log.Error("could not list source calendars", "err", err)
		return m.stats
	}

	// Map destination calendars by name and remember the default one.
	destCals, err := m.dst.ListCalendars(ctx)
	if err != nil {
		log.Error("could not list destination calendars", "err", err)
		return m.stats
	}
	destByName := map[string]graph.Calendar{}
	var destDefault graph.Calendar
	for _, c := range destCals {
		destByName[c.Name] = c
		if c.IsDefaultCalendar {
			destDefault = c
		}
	}

	// Resolve each source calendar's destination and event count up front, so the
	// progress bars have real totals.
	type calJob struct {
		sc     graph.Calendar
		destID string
		count  int
	}
	var jobs []calJob
	var grandTotal int64
	for _, sc := range srcCals {
		if ctx.Err() != nil {
			break
		}
		destID, err := m.ensureDestCalendar(ctx, sc, destByName, destDefault)
		if err != nil {
			if errors.Is(err, errReadOnlyDestCalendar) {
				log.Warnf("Calendar %q: destination is read-only (e.g. holidays/birthdays); skipping", sc.Name)
			} else {
				log.Warnf("calendar %q: could not resolve destination: %v", sc.Name, err)
			}
			continue
		}
		atomic.AddInt64(&m.stats.CalendarsCopied, 1)
		n, err := m.src.CountEvents(ctx, sc.ID)
		if err != nil {
			log.Warnf("calendar %q: could not count events: %v", sc.Name, err)
		}
		jobs = append(jobs, calJob{sc: sc, destID: destID, count: n})
		grandTotal += int64(n)
	}

	// Live progress for the calendar phase (the mailbox phase's area is already
	// finished). One bar per calendar, mirroring the folder bars.
	m.progress = ascii.NewProgress(grandTotal, !m.opts.Logging.Silence)
	m.progress.Start()
	defer m.progress.Finish()

	for _, j := range jobs {
		if ctx.Err() != nil {
			break
		}
		bar := m.progress.StartFolder(j.sc.Name, j.count)
		bar.SetUnit("events")
		if !m.progress.Enabled() {
			log.Warnf("Calendar: %s (%d events)", j.sc.Name, j.count)
		}
		if err := m.src.ListEvents(ctx, j.sc.ID, func(ev map[string]interface{}) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.copyEvent(ctx, j.sc.Name, j.destID, ev, bar)
			return nil
		}); err != nil && ctx.Err() == nil && !m.progress.SuppressLogs() {
			log.Warnf("calendar %q: listing events failed: %v", j.sc.Name, err)
		}
		bar.Done()
	}
	return m.stats
}

// ensureDestCalendar returns the destination calendar id for a source calendar:
// the destination default calendar for the source default, otherwise a
// same-named calendar (created if missing). A matched destination calendar that
// cannot be written to (holidays/birthdays, canEdit=false) returns
// errReadOnlyDestCalendar so the caller skips it instead of failing every event.
// destByName is updated on create so a repeated name is not created twice.
func (m *Migrator) ensureDestCalendar(ctx context.Context, sc graph.Calendar, destByName map[string]graph.Calendar, destDefault graph.Calendar) (string, error) {
	if sc.IsDefaultCalendar {
		if destDefault.ID == "" {
			return "", fmt.Errorf("destination has no default calendar")
		}
		return destDefault.ID, nil
	}
	if dc, ok := destByName[sc.Name]; ok {
		if !dc.CanEdit {
			return "", errReadOnlyDestCalendar
		}
		return dc.ID, nil
	}
	created, err := m.dst.CreateCalendar(ctx, sc.Name)
	if err != nil {
		return "", err
	}
	// A freshly created calendar is writable; remember it so a repeated source
	// name is not created twice.
	created.CanEdit = true
	destByName[sc.Name] = created
	return created.ID, nil
}

// copyEvent copies one source event into the destination calendar, recording
// state for resume/dedup. It ticks the bar exactly once, whatever the outcome.
func (m *Migrator) copyEvent(ctx context.Context, calendarName, destCalID string, ev map[string]interface{}, bar *ascii.FolderBar) {
	defer bar.Inc()

	srcID, _ := ev["id"].(string)
	ical, _ := ev["iCalUId"].(string)
	subject, _ := ev["subject"].(string)

	key := ical
	if key == "" {
		key = "src:" + srcID
	}
	dedupKey := calendarName + "|" + key

	rec := models.EventMigration{
		DedupKey:      dedupKey,
		SourceEventID: srcID,
		ICalUID:       ical,
		CalendarName:  calendarName,
		Subject:       truncate(subject, 500),
		StartAt:       eventStart(ev),
		Status:        "pending",
		MigratedAt:    time.Now(),
	}

	// Already copied in a previous run?
	if prevDestID, done := m.eventDone(dedupKey); done {
		if !m.opts.Migrate.Force {
			atomic.AddInt64(&m.stats.EventsSkipped, 1)
			return
		}
		// --force: delete the previous destination copy (if its id is known), then
		// recreate below. A 404 means it is already gone, so continue.
		if prevDestID != "" {
			if err := m.retry(ctx, "delete event: "+subject, func() error {
				return m.dst.DeleteEvent(ctx, prevDestID)
			}); err != nil && !graph.IsNotFound(err) {
				m.failEvent(&rec, fmt.Errorf("delete existing before re-copy: %w", err), subject, bar)
				return
			}
		}
	}

	body := graph.EventCreateBody(ev)
	var newID string
	if err := m.retry(ctx, "create event: "+subject, func() (e error) {
		newID, e = m.dst.CreateEvent(ctx, destCalID, body)
		return e
	}); err != nil {
		m.failEvent(&rec, err, subject, bar)
		return
	}

	rec.DestEventID = newID
	rec.Status = "done"
	rec.Error = ""
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.EventsCopied, 1)
	m.saveEvent(&rec)
}

func (m *Migrator) failEvent(rec *models.EventMigration, err error, subject string, bar *ascii.FolderBar) {
	atomic.AddInt64(&m.stats.EventsFailed, 1)
	bar.Fail()
	rec.Status = "failed"
	rec.Error = truncate(err.Error(), 1000)
	rec.MigratedAt = time.Now()
	m.saveEvent(rec)
	if !m.progress.SuppressLogs() {
		log.Warnf("event failed %q: %v", subject, err)
	}
}

// eventDone returns the recorded destination event id and whether the event has
// already been copied (status done).
func (m *Migrator) eventDone(dedupKey string) (string, bool) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	var rec models.EventMigration
	if err := m.db.Select("dest_event_id").
		Where("dedup_key = ? AND status = ?", dedupKey, "done").
		First(&rec).Error; err != nil {
		return "", false
	}
	return rec.DestEventID, true
}

func (m *Migrator) saveEvent(rec *models.EventMigration) {
	m.dbMu.Lock()
	defer m.dbMu.Unlock()
	if err := m.db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "dedup_key"}},
		DoUpdates: clause.AssignmentColumns([]string{"dest_event_id", "status", "error", "subject", "start_at", "migrated_at"}),
	}).Create(rec).Error; err != nil {
		atomic.AddInt64(&m.stats.SaveFailed, 1)
		log.Warnf("event state DB write failed for %q: %v", rec.DedupKey, err)
	}
}

// eventStart extracts an event's start dateTime (for state/reporting).
func eventStart(ev map[string]interface{}) string {
	if s, ok := ev["start"].(map[string]interface{}); ok {
		if dt, ok := s["dateTime"].(string); ok {
			return dt
		}
	}
	return ""
}
