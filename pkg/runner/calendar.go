package runner

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/helviojunior/o365-mail-migration/pkg/graph"
	"github.com/helviojunior/o365-mail-migration/pkg/log"
	"github.com/helviojunior/o365-mail-migration/pkg/models"
	"gorm.io/gorm/clause"
)

// MigrateCalendars copies calendars and their events as a final step. For each
// source calendar it finds or creates the matching destination calendar, then
// copies every event (single instances and recurring series masters; individual
// occurrences are generated from their series, so they are not copied), deduped
// against the local state DB so re-runs do not duplicate. With --force an
// already-copied event's destination copy is deleted and recreated.
//
// It runs after the live progress area is gone, so its logs are clean. Failures
// are logged and counted, never fatal.
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
	destByName := map[string]string{}
	destDefaultID := ""
	for _, c := range destCals {
		destByName[c.Name] = c.ID
		if c.IsDefaultCalendar {
			destDefaultID = c.ID
		}
	}

	for _, sc := range srcCals {
		if ctx.Err() != nil {
			break
		}
		destCalID, err := m.ensureDestCalendar(ctx, sc, destByName, destDefaultID)
		if err != nil {
			log.Warnf("calendar %q: could not resolve destination: %v", sc.Name, err)
			continue
		}
		atomic.AddInt64(&m.stats.CalendarsCopied, 1)
		log.Warnf("Calendar: %s", sc.Name)

		if err := m.src.ListEvents(ctx, sc.ID, func(ev map[string]interface{}) error {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			m.copyEvent(ctx, sc.Name, destCalID, ev)
			return nil
		}); err != nil && ctx.Err() == nil {
			log.Warnf("calendar %q: listing events failed: %v", sc.Name, err)
		}
	}

	log.Warnf("Calendars: %d · events copied: %d, skipped: %d, failed: %d",
		m.stats.CalendarsCopied, m.stats.EventsCopied, m.stats.EventsSkipped, m.stats.EventsFailed)
	return m.stats
}

// ensureDestCalendar returns the destination calendar id for a source calendar:
// the destination default calendar for the source default, otherwise a
// same-named calendar (created if missing). destByName is updated on create so a
// repeated name is not created twice.
func (m *Migrator) ensureDestCalendar(ctx context.Context, sc graph.Calendar, destByName map[string]string, destDefaultID string) (string, error) {
	if sc.IsDefaultCalendar {
		if destDefaultID == "" {
			return "", fmt.Errorf("destination has no default calendar")
		}
		return destDefaultID, nil
	}
	if id, ok := destByName[sc.Name]; ok {
		return id, nil
	}
	created, err := m.dst.CreateCalendar(ctx, sc.Name)
	if err != nil {
		return "", err
	}
	destByName[sc.Name] = created.ID
	return created.ID, nil
}

// copyEvent copies one source event into the destination calendar, recording
// state for resume/dedup.
func (m *Migrator) copyEvent(ctx context.Context, calendarName, destCalID string, ev map[string]interface{}) {
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
				m.failEvent(&rec, fmt.Errorf("delete existing before re-copy: %w", err), subject)
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
		m.failEvent(&rec, err, subject)
		return
	}

	rec.DestEventID = newID
	rec.Status = "done"
	rec.Error = ""
	rec.MigratedAt = time.Now()
	atomic.AddInt64(&m.stats.EventsCopied, 1)
	m.saveEvent(&rec)
}

func (m *Migrator) failEvent(rec *models.EventMigration, err error, subject string) {
	atomic.AddInt64(&m.stats.EventsFailed, 1)
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
