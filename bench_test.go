package main

// Benchmarks for the paths users feel, end to end. See TESTING.md for how to
// run them and compare against bench/baseline.txt.
//
//	BenchmarkPaneWrite    PTY bytes → emulator → scrollback, per workload
//	BenchmarkReflow       rewrapping 5k lines of history on a width change
//	BenchmarkSearch       Ctrl+F scan over 10k lines of scrollback
//	BenchmarkFrameCapture copying a 200×60 pane for one GUI frame
//	BenchmarkTOMLEdit     one Preferences change to the documented config
//	BenchmarkGUIFrame     building a whole frame (capture + GSK nodes), GUI only
//
// The emulator alone is measured by internal/vt10x BenchmarkWrite.

import (
	"os/exec"
	"testing"
	"time"

	"bunker/internal/benchdata"
	"bunker/internal/vt10x"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// benchPane is a Pane without a PTY, wired as NewPane wires it.
func benchPane(cols, rows, scrollback int) *Pane {
	p := &Pane{
		cmd:             &exec.Cmd{},
		x:               0,
		y:               0,
		w:               cols + 1,
		h:               rows,
		scrollbackLines: scrollback,
		sb:              sbRing{maxLines: scrollback},
	}
	p.term = vt10x.New(vt10x.WithSize(cols, rows),
		vt10x.WithScrollSwapCallback(p.onScrollSwap),
		vt10x.WithScrollbackClearCallback(p.onScrollbackClear))
	return p
}

// write feeds data in PTY-sized chunks, as readPTY does.
func (p *Pane) benchWrite(data []byte) {
	const chunk = 32 << 10
	for len(data) > 0 {
		n := min(chunk, len(data))
		p.mu.Lock()
		p.captureAndWrite(data[:n])
		p.mu.Unlock()
		data = data[n:]
	}
}

var workloads = []struct {
	name string
	data func() []byte
}{
	{"seq", func() []byte { return benchdata.Seq(200_000) }},
	{"prose", func() []byte { return benchdata.Prose(20_000) }},
	{"colors", func() []byte { return benchdata.Colors(5_000) }},
	{"unicode", func() []byte { return benchdata.Unicode(10_000) }},
	{"tui", func() []byte { return benchdata.TUI(60, 120, 40) }},
}

func BenchmarkPaneWrite(b *testing.B) {
	for _, w := range workloads {
		data := w.data()
		b.Run(w.name, func(b *testing.B) {
			p := benchPane(120, 40, defaultScrollbackLines)
			b.SetBytes(int64(len(data)))
			b.ReportAllocs()
			for b.Loop() {
				p.benchWrite(data)
			}
		})
	}
}

func BenchmarkReflow(b *testing.B) {
	p := benchPane(120, 40, defaultScrollbackLines)
	p.benchWrite(benchdata.Prose(5_000))
	b.ReportAllocs()
	wide := true
	for b.Loop() {
		cols := 80
		if !wide {
			cols = 120
		}
		wide = !wide
		p.mu.Lock()
		p.resizeAndReflow(cols, 40)
		p.mu.Unlock()
	}
}

func BenchmarkSearch(b *testing.B) {
	p := benchPane(120, 40, defaultScrollbackLines)
	p.benchWrite(benchdata.Prose(10_000))
	app := &App{redraw: make(chan struct{}, 1), searchPane: p, searchQuery: "liquor", searchMode: true}
	b.ReportAllocs()
	for b.Loop() {
		app.runSearchScan()
		drainRedraw(app.redraw)
	}
	if len(app.searchMatches) == 0 {
		b.Fatal("search found nothing")
	}
}

func BenchmarkFrameCapture(b *testing.B) {
	p := benchPane(200, 60, 1000)
	p.benchWrite(benchdata.TUI(1, 200, 60))
	var f paneFrame
	b.ReportAllocs()
	for b.Loop() {
		f.capture(p)
	}
}

func BenchmarkTOMLEdit(b *testing.B) {
	base := DefaultConfigTOML()
	b.ReportAllocs()
	for b.Loop() {
		setTOMLKey(base, "tabs", "position", `"right"`)
	}
}

// BenchmarkGUIFrame builds complete frames of a busy 3-pane tab: capture of
// every pane plus all colour and text nodes. A 60 Hz display leaves 16ms.
func BenchmarkGUIFrame(b *testing.B) {
	w := newTestWin(b, "exec sleep 60", nil)
	w.key(gdk.KEY_F1, 0)
	w.key(gdk.KEY_F1, 0)
	w.waitPanes(3)
	for _, p := range w.panes() {
		p.mu.Lock()
		p.captureAndWrite(benchdata.TUI(1, 120, 40))
		p.mu.Unlock()
	}
	settle(200 * time.Millisecond)
	b.ReportAllocs()
	for b.Loop() {
		onMain(func() {
			snap := gtk.NewSnapshot()
			w.active.view.snapshot(snap)
		})
	}
}
