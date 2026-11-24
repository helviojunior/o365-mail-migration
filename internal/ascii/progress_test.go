package ascii

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestFmtDuration(t *testing.T) {
	cases := map[time.Duration]string{
		0:                                "0s",
		45 * time.Second:                 "45s",
		(3*time.Minute + 12*time.Second): "3m12s",
		(1*time.Hour + 2*time.Minute):    "1h02m",
		-5 * time.Second:                 "0s",
	}
	for d, want := range cases {
		if got := fmtDuration(d); got != want {
			t.Errorf("fmtDuration(%s) = %q, want %q", d, got, want)
		}
	}
}

func TestTrimMiddle(t *testing.T) {
	if got := trimMiddle("Inbox", 10); got != "Inbox" {
		t.Errorf("short string changed: %q", got)
	}
	got := trimMiddle("Inbox/Projects/2024/Q4/Reports", 12)
	if len([]rune(got)) != 12 {
		t.Errorf("trimMiddle length = %d, want 12 (%q)", len([]rune(got)), got)
	}
	if got[0] != 'I' {
		t.Errorf("head not preserved: %q", got)
	}
}

func TestProgressCounters(t *testing.T) {
	// In the test process stdout is not a TTY, so no pterm bars are created;
	// only the counters are exercised.
	p := NewProgress(10, true)
	if !p.Enabled() {
		t.Fatal("expected enabled progress")
	}

	bar := p.StartFolder("Inbox", 5)
	if got := atomic.LoadInt64(&p.active); got != 1 {
		t.Fatalf("active = %d, want 1", got)
	}

	bar.Inc()
	bar.Inc()
	bar.Inc()
	if got := atomic.LoadInt64(&p.done); got != 3 {
		t.Fatalf("done = %d, want 3", got)
	}

	bar.Done()
	if got := atomic.LoadInt64(&p.active); got != 0 {
		t.Fatalf("active after Done = %d, want 0", got)
	}
}

func TestProgressDisabledIsNoop(t *testing.T) {
	p := NewProgress(10, false)
	bar := p.StartFolder("Inbox", 5)
	bar.Inc()
	bar.Done()
	if got := atomic.LoadInt64(&p.done); got != 0 {
		t.Fatalf("disabled progress counted: done = %d, want 0", got)
	}
}
