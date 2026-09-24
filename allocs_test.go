package main

// Allocation guards: the hot paths must not allocate in steady state. Unlike
// timings these are deterministic, so they fail reliably on a regression
// (a per-character string, a closure per escape sequence, a buffer that
// reallocates per chunk) instead of flaking. See TESTING.md.

import (
	"testing"

	"bunker/internal/benchdata"
)

func TestAllocs_PaneWriteSteadyState(t *testing.T) {
	for _, w := range []struct {
		name  string
		chunk []byte
		max   float64 // allocations per write of chunk
	}{
		{"seq", benchdata.Seq(3000), 0},
		{"prose", benchdata.Prose(300), 0},
		{"colors", benchdata.Colors(100), 0},
		{"tui", benchdata.TUI(1, 120, 40), 0},
	} {
		t.Run(w.name, func(t *testing.T) {
			p := benchPane(120, 40, 200)
			for range 20 { // fill scrollback and raw history to their caps
				p.benchWrite(w.chunk)
			}
			got := testing.AllocsPerRun(50, func() { p.benchWrite(w.chunk) })
			if got > w.max {
				t.Fatalf("writing %d bytes allocates %.1f times (max %.0f)", len(w.chunk), got, w.max)
			}
		})
	}
}

func TestAllocs_FrameCaptureSteadyState(t *testing.T) {
	p := benchPane(200, 60, 100)
	p.benchWrite(benchdata.TUI(1, 200, 60))
	var f paneFrame
	f.capture(p)
	if got := testing.AllocsPerRun(50, func() { f.capture(p) }); got != 0 {
		t.Fatalf("capturing a frame allocates %.1f times", got)
	}
}
