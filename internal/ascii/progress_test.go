package ascii

import (
	"bytes"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// TestProgressTTYRendering drives the live-area logic with TTY mode forced and
// a buffer as output, so the ANSI area management is exercised without a real
// terminal.
func TestProgressTTYRendering(t *testing.T) {
	var buf bytes.Buffer
	p := &Progress{out: &buf, enabled: true, tty: true, total: 4}

	a := p.StartFolder("Inbox", 2)
	p.StartFolder("Sent Items", 2)

	// One render tick draws the overall bar plus both folder bars.
	p.mu.Lock()
	p.drawLocked()
	p.mu.Unlock()
	if p.linesDrawn != 4 {
		t.Fatalf("linesDrawn after draw = %d, want 4 (spacer + overall + 2 folders)", p.linesDrawn)
	}
	if !strings.Contains(buf.String(), "Overall") {
		t.Fatalf("overall mailbox bar missing:\n%q", buf.String())
	}

	a.Inc()
	a.Inc()
	a.Done() // prints the permanent line and redraws remaining bars

	out := buf.String()
	// pterm colorizes, so the words are interleaved with SGR codes; check the
	// stable parts of the permanent line and the area-management escapes.
	for _, want := range []string{"✓", "messages migrated", "\033["} {
		if !strings.Contains(out, want) {
			t.Fatalf("output missing %q:\n%q", want, out)
		}
	}
	// Spacer + overall bar + Sent Items remain (Inbox done and removed).
	if p.linesDrawn != 3 {
		t.Fatalf("linesDrawn after one Done = %d, want 3 (spacer + overall + 1 folder)", p.linesDrawn)
	}
}

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
	p.mu.Lock()
	n := len(p.active)
	p.mu.Unlock()
	if n != 1 {
		t.Fatalf("active = %d, want 1", n)
	}

	bar.Inc()
	bar.Inc()
	bar.Inc()
	if got := atomic.LoadInt64(&p.done); got != 3 {
		t.Fatalf("done = %d, want 3", got)
	}

	bar.Done()
	p.mu.Lock()
	n = len(p.active)
	p.mu.Unlock()
	if n != 0 {
		t.Fatalf("active after Done = %d, want 0", n)
	}
}

func TestCompletionLine(t *testing.T) {
	ok := completionLine("Inbox", 10, 0)
	if !strings.Contains(ok, "✓") || !strings.Contains(ok, "messages migrated") {
		t.Fatalf("success line wrong: %q", ok)
	}
	if strings.Contains(ok, "failed") {
		t.Fatalf("success line should not mention failures: %q", ok)
	}

	bad := completionLine("Inbox", 10, 3)
	for _, want := range []string{"✗", "migrated", "failed"} {
		if !strings.Contains(bad, want) {
			t.Fatalf("failure line missing %q: %q", want, bad)
		}
	}
	// 10 processed, 3 failed -> 7 migrated reported.
	if !strings.Contains(bad, "7") || !strings.Contains(bad, "3") {
		t.Fatalf("failure line counts wrong (want 7 migrated / 3 failed): %q", bad)
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
