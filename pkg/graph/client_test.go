package graph

import (
	"context"
	"net/http"
	"testing"
	"time"
)

func TestParseRetryAfter(t *testing.T) {
	t.Run("seconds", func(t *testing.T) {
		if got := parseRetryAfter("120"); got != 120*time.Second {
			t.Fatalf("got %s, want 120s", got)
		}
	})

	t.Run("empty", func(t *testing.T) {
		if got := parseRetryAfter(""); got != 0 {
			t.Fatalf("got %s, want 0", got)
		}
	})

	t.Run("http-date in the future", func(t *testing.T) {
		future := time.Now().Add(30 * time.Second).UTC().Format(http.TimeFormat)
		got := parseRetryAfter(future)
		if got <= 0 || got > 31*time.Second {
			t.Fatalf("got %s, want ~30s", got)
		}
	})

	t.Run("http-date in the past is ignored", func(t *testing.T) {
		past := time.Now().Add(-time.Hour).UTC().Format(http.TimeFormat)
		if got := parseRetryAfter(past); got != 0 {
			t.Fatalf("got %s, want 0", got)
		}
	})
}

func TestThrottleGate(t *testing.T) {
	t.Run("waitGate blocks until the pause elapses", func(t *testing.T) {
		c := &Client{}
		c.setGate(80 * time.Millisecond)
		start := time.Now()
		if err := c.waitGate(context.Background()); err != nil {
			t.Fatalf("waitGate: %v", err)
		}
		if waited := time.Since(start); waited < 60*time.Millisecond {
			t.Fatalf("returned too early after %s; gate not honoured", waited)
		}
	})

	t.Run("no pause returns immediately", func(t *testing.T) {
		c := &Client{}
		start := time.Now()
		if err := c.waitGate(context.Background()); err != nil {
			t.Fatalf("waitGate: %v", err)
		}
		if waited := time.Since(start); waited > 20*time.Millisecond {
			t.Fatalf("blocked %s with no gate set", waited)
		}
	})

	t.Run("setGate only ever extends the window", func(t *testing.T) {
		c := &Client{}
		c.setGate(time.Hour)
		far := c.pausedUntil
		c.setGate(time.Second) // shorter: must not pull the window in
		if !c.pausedUntil.Equal(far) {
			t.Fatalf("a shorter setGate shortened the pause")
		}
	})

	t.Run("cancelled context unblocks waitGate", func(t *testing.T) {
		c := &Client{}
		c.setGate(time.Hour)
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		if err := c.waitGate(ctx); err == nil {
			t.Fatalf("expected context error, got nil")
		}
	})
}

func TestBackoffDuration(t *testing.T) {
	t.Run("honours Retry-After exactly", func(t *testing.T) {
		if got := backoffDuration(0, 17*time.Second); got != 17*time.Second {
			t.Fatalf("got %s, want 17s", got)
		}
	})

	t.Run("exponential fallback stays within [d/2, d] and is capped", func(t *testing.T) {
		for attempt := 0; attempt < 10; attempt++ {
			base := time.Duration(1<<attempt) * time.Second
			if base > 60*time.Second {
				base = 60 * time.Second
			}
			for i := 0; i < 50; i++ {
				got := backoffDuration(attempt, 0)
				if got < base/2 || got > base {
					t.Fatalf("attempt %d: got %s, want within [%s, %s]", attempt, got, base/2, base)
				}
			}
		}
	})
}

func TestAbs(t *testing.T) {
	c := &Client{}
	if got := c.abs("/me/messages"); got != BaseURL+"/me/messages" {
		t.Fatalf("relative path not prefixed: %q", got)
	}
	absolute := "https://graph.microsoft.com/v1.0/me/mailFolders?$skiptoken=x"
	if got := c.abs(absolute); got != absolute {
		t.Fatalf("absolute URL rewritten: %q", got)
	}
}
