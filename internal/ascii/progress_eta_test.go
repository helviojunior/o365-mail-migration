package ascii

import (
	"testing"
	"time"
)

// A slow in-flight folder must pull the Overall ETA up to at least its own ETA:
// the run cannot finish before its slowest active folder does.
func TestOverallETAClampedToSlowestFolder(t *testing.T) {
	p := &Progress{total: 1000, start: time.Now().Add(-100 * time.Second)}
	p.done = 500 // global average: 100s / 500 = 0.2s/msg, 500 left -> ~100s

	// A folder crawling at ~1s/msg with 900 left -> ~900s ETA.
	slow := &FolderBar{p: p, total: 1000, done: 100, start: time.Now().Add(-100 * time.Second)}
	p.active = []*FolderBar{slow}

	eta, ok := p.overallETALocked(p.done, time.Since(p.start))
	if !ok {
		t.Fatal("expected a computable ETA")
	}
	if eta < 890*time.Second {
		t.Fatalf("overall ETA %s was not clamped up to the slow folder (~900s)", eta)
	}
}

// With no folder slower than the global estimate, the Overall ETA stays the
// global estimate (no spurious inflation).
func TestOverallETAUnclampedWhenFoldersFast(t *testing.T) {
	p := &Progress{total: 1000, start: time.Now().Add(-100 * time.Second)}
	p.done = 500 // ~100s remaining

	fast := &FolderBar{p: p, total: 1000, done: 900, start: time.Now().Add(-100 * time.Second)} // ~11s left
	p.active = []*FolderBar{fast}

	eta, ok := p.overallETALocked(p.done, time.Since(p.start))
	if !ok {
		t.Fatal("expected a computable ETA")
	}
	if eta > 130*time.Second {
		t.Fatalf("overall ETA %s should stay near the global ~100s estimate", eta)
	}
}
