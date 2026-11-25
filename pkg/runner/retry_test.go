package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/helviojunior/o365-mail-migration/internal/ascii"
	"github.com/helviojunior/o365-mail-migration/pkg/graph"
)

// newTestMigrator builds a Migrator with just enough wired up to exercise retry:
// the options that control it and a disabled progress (so SuppressLogs is safe).
func newTestMigrator(retries int, delay time.Duration) *Migrator {
	m := &Migrator{progress: ascii.NewProgress(0, false)}
	m.opts.Migrate.Retries = retries
	m.opts.Migrate.RetryDelay = delay
	return m
}

func TestRetry(t *testing.T) {
	t.Run("succeeds on the first attempt", func(t *testing.T) {
		m := newTestMigrator(5, time.Millisecond)
		calls := 0
		err := m.retry(context.Background(), "x", func() error { calls++; return nil })
		if err != nil || calls != 1 {
			t.Fatalf("calls=%d err=%v, want 1 and nil", calls, err)
		}
	})

	t.Run("retries up to Retries+1 attempts then returns the last error", func(t *testing.T) {
		m := newTestMigrator(5, time.Millisecond)
		calls := 0
		sentinel := errors.New("boom")
		err := m.retry(context.Background(), "x", func() error { calls++; return sentinel })
		if calls != 6 { // 1 initial + 5 retries
			t.Fatalf("calls=%d, want 6", calls)
		}
		if !errors.Is(err, sentinel) {
			t.Fatalf("err=%v, want sentinel", err)
		}
	})

	t.Run("stops as soon as an attempt succeeds", func(t *testing.T) {
		m := newTestMigrator(5, time.Millisecond)
		calls := 0
		err := m.retry(context.Background(), "x", func() error {
			calls++
			if calls < 3 {
				return errors.New("transient")
			}
			return nil
		})
		if err != nil || calls != 3 {
			t.Fatalf("calls=%d err=%v, want 3 and nil", calls, err)
		}
	})

	t.Run("does not retry a 404 (object is gone)", func(t *testing.T) {
		m := newTestMigrator(5, time.Millisecond)
		calls := 0
		err := m.retry(context.Background(), "x", func() error {
			calls++
			return &graph.APIError{Status: 404}
		})
		if calls != 1 {
			t.Fatalf("calls=%d, want 1 (no retries on 404)", calls)
		}
		if !graph.IsNotFound(err) {
			t.Fatalf("err=%v, want a 404", err)
		}
	})

	t.Run("does not retry once the context is cancelled", func(t *testing.T) {
		m := newTestMigrator(5, time.Millisecond)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		calls := 0
		err := m.retry(ctx, "x", func() error { calls++; return errors.New("transient") })
		if calls != 1 {
			t.Fatalf("calls=%d, want 1 (cancelled ctx must not retry)", calls)
		}
		if err == nil {
			t.Fatal("expected an error")
		}
	})
}
