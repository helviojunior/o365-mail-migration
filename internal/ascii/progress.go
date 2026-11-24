package ascii

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
	"golang.org/x/term"
)

const (
	ttyInterval    = 150 * time.Millisecond
	nonTTYInterval = 60 * time.Second
	barWidth       = 24
	nameWidth      = 20
)

// Progress reports migration progress. On a TTY it keeps a live area at the
// bottom of the screen with one bar per in-flight folder; when a folder
// finishes it prints a permanent "✓ Name: N messages migrated" line above the
// area (docker-compose style) and the remaining bars move up below it. When the
// output is not a TTY it logs a plain "[status] ..." snapshot every 60s. When
// disabled (quiet or dry-run) every method is a no-op.
type Progress struct {
	// 64-bit counters first for alignment on 32-bit platforms.
	done int64

	enabled bool
	tty     bool
	total   int64
	start   time.Time
	out     io.Writer

	mu         sync.Mutex
	active     []*FolderBar
	linesDrawn int

	stopCh chan struct{}
	doneCh chan struct{}
}

// NewProgress creates a reporter for a run of total messages. enabled is
// typically "not quiet and not a dry-run"; TTY vs non-TTY is detected here.
func NewProgress(total int64, enabled bool) *Progress {
	return &Progress{
		out:     os.Stdout,
		enabled: enabled,
		tty:     term.IsTerminal(int(os.Stdout.Fd())),
		total:   total,
	}
}

// Enabled reports whether progress reporting is active at all.
func (p *Progress) Enabled() bool { return p != nil && p.enabled }

// SuppressLogs reports whether live logging must be withheld to avoid
// corrupting the live area (true only in TTY mode).
func (p *Progress) SuppressLogs() bool { return p.Enabled() && p.tty }

// Start begins rendering.
func (p *Progress) Start() {
	if !p.Enabled() {
		return
	}
	p.start = time.Now()
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})

	interval := nonTTYInterval
	if p.tty {
		interval = ttyInterval
		io.WriteString(p.out, "\033[?25l") // hide cursor
	}

	go func() {
		t := time.NewTicker(interval)
		defer t.Stop()
		defer close(p.doneCh)
		for {
			select {
			case <-p.stopCh:
				return
			case <-t.C:
				if p.tty {
					p.mu.Lock()
					p.drawLocked()
					p.mu.Unlock()
				} else {
					p.snapshot()
				}
			}
		}
	}()
}

// Finish stops rendering and leaves the terminal clean.
func (p *Progress) Finish() {
	if !p.Enabled() {
		return
	}
	close(p.stopCh)
	<-p.doneCh

	if p.tty {
		p.mu.Lock()
		p.clearAreaLocked()
		p.mu.Unlock()
		io.WriteString(p.out, "\033[?25h") // show cursor
		return
	}
	p.snapshot() // final 100% line
}

// FolderBar is a per-folder progress handle.
type FolderBar struct {
	p      *Progress
	name   string
	total  int
	done   int64
	failed int64
	start  time.Time
}

// StartFolder begins tracking a folder of total messages.
func (p *Progress) StartFolder(name string, total int) *FolderBar {
	fb := &FolderBar{p: p, name: name, total: total, start: time.Now()}
	if !p.Enabled() {
		return fb
	}
	p.mu.Lock()
	p.active = append(p.active, fb)
	p.mu.Unlock()
	return fb
}

// Inc records one processed message in this folder.
func (fb *FolderBar) Inc() {
	if !fb.p.Enabled() {
		return
	}
	atomic.AddInt64(&fb.done, 1)
	atomic.AddInt64(&fb.p.done, 1)
}

// Fail records that one message in this folder failed to migrate.
func (fb *FolderBar) Fail() {
	if !fb.p.Enabled() {
		return
	}
	atomic.AddInt64(&fb.failed, 1)
}

// Done completes the folder: prints a permanent summary line (TTY) and removes
// the bar from the live area.
func (fb *FolderBar) Done() {
	p := fb.p
	if !p.Enabled() {
		return
	}
	done := atomic.LoadInt64(&fb.done)

	failed := atomic.LoadInt64(&fb.failed)

	p.mu.Lock()
	defer p.mu.Unlock()
	p.removeActiveLocked(fb)

	if p.tty {
		p.clearAreaLocked()
		io.WriteString(p.out, completionLine(fb.name, done, failed)+"\n")
		p.drawLocked()
	}
}

// completionLine formats the permanent per-folder result line.
func completionLine(name string, done, failed int64) string {
	if failed == 0 {
		return fmt.Sprintf("%s %s: %s messages migrated",
			pterm.Green("✓"), pterm.Cyan(name), pterm.Bold.Sprint(done))
	}
	return fmt.Sprintf("%s %s: %s migrated, %s failed",
		pterm.Red("✗"), pterm.Cyan(name), pterm.Bold.Sprint(done-failed), pterm.Red(pterm.Bold.Sprint(failed)))
}

// drawLocked redraws the live area in place (caller holds p.mu).
func (p *Progress) drawLocked() {
	if !p.tty {
		return
	}
	var b strings.Builder
	if p.linesDrawn > 0 {
		fmt.Fprintf(&b, "\033[%dA\r\033[J", p.linesDrawn) // up, col0, clear to end
	}
	for _, fb := range p.active {
		b.WriteString(p.barLine(fb))
		b.WriteByte('\n')
	}
	p.linesDrawn = len(p.active)
	io.WriteString(p.out, b.String())
}

// clearAreaLocked erases the live area (caller holds p.mu).
func (p *Progress) clearAreaLocked() {
	if p.linesDrawn > 0 {
		fmt.Fprintf(p.out, "\033[%dA\r\033[J", p.linesDrawn)
		p.linesDrawn = 0
	}
}

func (p *Progress) removeActiveLocked(fb *FolderBar) {
	for i, x := range p.active {
		if x == fb {
			p.active = append(p.active[:i], p.active[i+1:]...)
			return
		}
	}
}

// barLine renders one folder's progress line, including an estimated time to
// completion (ETA) derived from the folder's own throughput so far.
func (p *Progress) barLine(fb *FolderBar) string {
	done := atomic.LoadInt64(&fb.done)
	elapsed := time.Since(fb.start)

	ratio := 1.0
	if fb.total > 0 {
		ratio = float64(done) / float64(fb.total)
		if ratio > 1 {
			ratio = 1
		}
	}
	filled := int(ratio * barWidth)
	if filled > barWidth {
		filled = barWidth
	}
	bar := pterm.Cyan(strings.Repeat("█", filled)) + pterm.Gray(strings.Repeat("░", barWidth-filled))
	name := padRight(trimMiddle(fb.name, nameWidth), nameWidth)
	counts := padLeft(fmt.Sprintf("%d/%d", done, fb.total), 11)

	eta := "--"
	if done > 0 && int64(fb.total) > done {
		per := elapsed / time.Duration(done)
		eta = fmtDuration(per * time.Duration(int64(fb.total)-done))
	}

	return fmt.Sprintf("%s %s %s %3.0f%% · %s · ETA %s",
		pterm.Cyan(name), pterm.Gray(counts), bar, ratio*100,
		pterm.Gray(fmtDuration(elapsed)), pterm.Gray(eta))
}

func (p *Progress) snapshot() {
	p.mu.Lock()
	active := len(p.active)
	p.mu.Unlock()

	done := atomic.LoadInt64(&p.done)
	elapsed := time.Since(p.start)

	pct := 0.0
	if p.total > 0 {
		pct = float64(done) / float64(p.total) * 100
		if pct > 100 {
			pct = 100
		}
	}

	eta := "--"
	if done > 0 && p.total > 0 && done < p.total {
		per := elapsed / time.Duration(done)
		eta = fmtDuration(per * time.Duration(p.total-done))
	}

	fmt.Fprintf(p.out, "[status] %5.1f%% | %d/%d msgs | %d folders active | %s elapsed | ETA %s\n",
		pct, done, p.total, active, fmtDuration(elapsed), eta)
}

func padRight(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return s + strings.Repeat(" ", n-len(r))
}

func padLeft(s string, n int) string {
	r := []rune(s)
	if len(r) >= n {
		return s
	}
	return strings.Repeat(" ", n-len(r)) + s
}

// trimMiddle shortens s to max runes, keeping head and tail with an ellipsis.
func trimMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	if max <= 1 {
		return string(r[:max])
	}
	head := (max - 1) / 2
	tail := max - 1 - head
	return string(r[:head]) + "…" + string(r[len(r)-tail:])
}

// fmtDuration formats a duration compactly: 45s, 3m12s, 1h02m.
func fmtDuration(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	d = d.Round(time.Second)
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	switch {
	case h > 0:
		return fmt.Sprintf("%dh%02dm", h, m)
	case m > 0:
		return fmt.Sprintf("%dm%02ds", m, s)
	default:
		return fmt.Sprintf("%ds", s)
	}
}
