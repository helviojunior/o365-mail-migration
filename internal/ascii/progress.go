package ascii

import (
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/term"
)

// spinnerFrames are cycled on each render tick (TTY mode).
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

const (
	ttyInterval    = 150 * time.Millisecond
	nonTTYInterval = 60 * time.Second
)

// Progress reports migration progress (spinner, bar, per-folder counter,
// overall percentage, elapsed and ETA). On a TTY it redraws a single live line;
// when the output is not a TTY it instead logs a plain snapshot every 60s. When
// disabled (quiet mode or dry-run) every method is a no-op.
type Progress struct {
	// 64-bit counters first for alignment on 32-bit platforms.
	done       int64
	folderDone int64
	folderTot  int64

	out     io.Writer
	enabled bool
	tty     bool
	total   int64
	start   time.Time
	frame   int

	folderName atomic.Value // string

	mu     sync.Mutex
	stopCh chan struct{}
	doneCh chan struct{}
}

// NewProgress creates a reporter for a run of total messages. enabled is
// typically "not quiet and not a dry-run"; TTY vs non-TTY is detected here.
func NewProgress(total int64, enabled bool) *Progress {
	p := &Progress{
		out:     os.Stderr,
		enabled: enabled,
		tty:     term.IsTerminal(int(os.Stderr.Fd())),
		total:   total,
	}
	p.folderName.Store("")
	return p
}

// Enabled reports whether progress reporting is active at all.
func (p *Progress) Enabled() bool { return p != nil && p.enabled }

// Start begins the redraw/snapshot loop.
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
					p.frame++
					p.renderLive()
				} else {
					p.renderSnapshot()
				}
			}
		}
	}()
}

// SetFolder switches the current folder and resets its counter.
func (p *Progress) SetFolder(name string, total int64) {
	if !p.Enabled() {
		return
	}
	p.folderName.Store(name)
	atomic.StoreInt64(&p.folderTot, total)
	atomic.StoreInt64(&p.folderDone, 0)
}

// Inc records one processed message (copied, skipped or failed).
func (p *Progress) Inc() {
	if !p.Enabled() {
		return
	}
	atomic.AddInt64(&p.done, 1)
	atomic.AddInt64(&p.folderDone, 1)
}

// Clear erases the live status line (TTY only). Call it before writing a log
// line so output is not corrupted; the next tick redraws the line.
func (p *Progress) Clear() {
	if !p.Enabled() || !p.tty {
		return
	}
	p.mu.Lock()
	io.WriteString(p.out, "\r\033[K")
	p.mu.Unlock()
}

// Finish stops the loop and leaves the terminal clean. On a non-TTY it emits a
// final snapshot so the log ends at 100%.
func (p *Progress) Finish() {
	if !p.Enabled() {
		return
	}
	close(p.stopCh)
	<-p.doneCh
	if p.tty {
		p.Clear()
		io.WriteString(p.out, "\033[?25h") // show cursor
	} else {
		p.renderSnapshot()
	}
}

func (p *Progress) renderLive() {
	spin := spinnerFrames[p.frame%len(spinnerFrames)]
	line := fmt.Sprintf("\r\033[K%s %s %s", spin, bar(p.pct(), 18), p.status(true))
	p.mu.Lock()
	io.WriteString(p.out, line)
	p.mu.Unlock()
}

func (p *Progress) renderSnapshot() {
	line := fmt.Sprintf("[status] %s\n", p.status(false))
	p.mu.Lock()
	io.WriteString(p.out, line)
	p.mu.Unlock()
}

func (p *Progress) pct() float64 {
	done := atomic.LoadInt64(&p.done)
	if p.total <= 0 {
		return 0
	}
	v := float64(done) / float64(p.total) * 100
	if v > 100 {
		v = 100
	}
	return v
}

// status builds the shared metrics string. When pad is true (TTY) the folder
// name is padded to keep the line length stable between redraws.
func (p *Progress) status(pad bool) string {
	done := atomic.LoadInt64(&p.done)
	elapsed := time.Since(p.start)

	eta := "--"
	if done > 0 && p.total > 0 && done < p.total {
		per := elapsed / time.Duration(done)
		eta = fmtDuration(per * time.Duration(p.total-done))
	}

	name, _ := p.folderName.Load().(string)
	if pad {
		name = trimMiddle(name, 28)
	}
	fd := atomic.LoadInt64(&p.folderDone)
	ft := atomic.LoadInt64(&p.folderTot)

	return fmt.Sprintf("%5.1f%% | %d/%d msgs | %s %d/%d | %s elapsed | ETA %s",
		p.pct(), done, p.total, name, fd, ft, fmtDuration(elapsed), eta)
}

// bar renders a [####----] progress bar of the given width.
func bar(pct float64, width int) string {
	filled := int(pct / 100 * float64(width))
	if filled > width {
		filled = width
	}
	if filled < 0 {
		filled = 0
	}
	return "[" + strings.Repeat("#", filled) + strings.Repeat("-", width-filled) + "]"
}

// trimMiddle shortens s to max runes, keeping head and tail with an ellipsis,
// padding with spaces when shorter so the rendered line length is stable.
func trimMiddle(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s + strings.Repeat(" ", max-len(r))
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
