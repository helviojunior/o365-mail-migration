package graph

import (
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
