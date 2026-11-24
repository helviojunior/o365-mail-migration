package ascii

import (
	"fmt"
	"io"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/pterm/pterm"
	"golang.org/x/term"
)

const nonTTYInterval = 60 * time.Second

// Progress reports migration progress. On a TTY it renders one live pterm
// progress bar per in-flight folder (docker-compose style: a bar appears when a
// folder starts and leaves the screen when it completes). When the output is
// not a TTY it logs a plain "[status] ..." snapshot every 60s. When disabled
// (quiet mode or dry-run) every method is a no-op.
type Progress struct {
	// 64-bit counters first for alignment on 32-bit platforms.
	done   int64
	active int64

	enabled bool
	tty     bool
	total   int64
	start   time.Time

	out   io.Writer
	multi *pterm.MultiPrinter

	stopCh chan struct{}
	doneCh chan struct{}
	mu     sync.Mutex
}

// NewProgress creates a reporter for a run of total messages. enabled is
// typically "not quiet and not a dry-run"; TTY vs non-TTY is detected here.
func NewProgress(total int64, enabled bool) *Progress {
	return &Progress{
		out:     os.Stderr,
		enabled: enabled,
		tty:     term.IsTerminal(int(os.Stdout.Fd())),
		total:   total,
	}
}

// Enabled reports whether progress reporting is active at all.
func (p *Progress) Enabled() bool { return p != nil && p.enabled }

// SuppressLogs reports whether live logging must be withheld to avoid
// corrupting the pterm live area (true only in TTY mode).
func (p *Progress) SuppressLogs() bool { return p.Enabled() && p.tty }

// Start begins rendering.
func (p *Progress) Start() {
	if !p.Enabled() {
		return
	}
	p.start = time.Now()
	if p.tty {
		m := pterm.DefaultMultiPrinter
		p.multi = &m
		p.multi.Start()
		return
	}
	// Non-TTY: periodic textual snapshots.
	p.stopCh = make(chan struct{})
	p.doneCh = make(chan struct{})
	go func() {
		t := time.NewTicker(nonTTYInterval)
		defer t.Stop()
		defer close(p.doneCh)
		for {
			select {
			case <-p.stopCh:
				return
			case <-t.C:
				p.snapshot()
			}
		}
	}()
}

// Finish stops rendering and leaves the terminal clean.
func (p *Progress) Finish() {
	if !p.Enabled() {
		return
	}
	if p.tty {
		if p.multi != nil {
			p.multi.Stop()
		}
		return
	}
	close(p.stopCh)
	<-p.doneCh
	p.snapshot() // final 100% line
}

// FolderBar is a per-folder progress handle.
type FolderBar struct {
	p     *Progress
	pb    *pterm.ProgressbarPrinter
	mu    sync.Mutex
	total int
	done  int
}

// StartFolder begins tracking a folder of total messages. A TTY gets a live
// bar; empty folders get no bar.
func (p *Progress) StartFolder(name string, total int) *FolderBar {
	if !p.Enabled() {
		return &FolderBar{p: p, total: total}
	}
	atomic.AddInt64(&p.active, 1)

	fb := &FolderBar{p: p, total: total}
	if p.tty && total > 0 {
		// Serialize pterm writer/bar creation: MultiPrinter.NewWriter appends to
		// a shared slice and folders start concurrently.
		p.mu.Lock()
		bar, err := pterm.DefaultProgressbar.
			WithTotal(total).
			WithWriter(p.multi.NewWriter()).
			WithRemoveWhenDone(true).
			WithTitle(trimMiddle(name, 32)).
			WithShowCount(true).
			WithShowPercentage(true).
			WithShowElapsedTime(true).
			Start()
		p.mu.Unlock()
		if err == nil {
			fb.pb = bar
		}
	}
	return fb
}

// Inc records one processed message in this folder.
func (fb *FolderBar) Inc() {
	if !fb.p.Enabled() {
		return
	}
	atomic.AddInt64(&fb.p.done, 1)

	fb.mu.Lock()
	defer fb.mu.Unlock()
	fb.done++
	if fb.pb != nil && fb.done <= fb.total {
		fb.pb.Increment()
	}
}

// Done completes the folder: fills the bar and removes it from the screen.
func (fb *FolderBar) Done() {
	if !fb.p.Enabled() {
		return
	}
	atomic.AddInt64(&fb.p.active, -1)

	fb.mu.Lock()
	defer fb.mu.Unlock()
	if fb.pb != nil {
		if rem := fb.total - fb.done; rem > 0 {
			fb.pb.Add(rem)
		}
		fb.pb.Stop() //nolint:errcheck // idempotent; nothing actionable on error
		fb.pb = nil
	}
}

func (p *Progress) snapshot() {
	done := atomic.LoadInt64(&p.done)
	active := atomic.LoadInt64(&p.active)
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

	line := fmt.Sprintf("[status] %5.1f%% | %d/%d msgs | %d folders active | %s elapsed | ETA %s\n",
		pct, done, p.total, active, fmtDuration(elapsed), eta)

	p.mu.Lock()
	io.WriteString(p.out, line)
	p.mu.Unlock()
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
