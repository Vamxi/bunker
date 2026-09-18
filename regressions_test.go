package main

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func regressionPane(cols, rows int) *Pane {
	p := &Pane{cmd: &exec.Cmd{}, w: cols + 1, h: rows, scrollbackLines: 1000, sb: sbRing{maxLines: 1000}}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollCallback(p.onScrollRow), vt10x.WithScrollbackClearCallback(p.onScrollbackClear))
	return p
}

func TestRegressionResizeRetainsModes(t *testing.T) {
	for _, size := range [][2]int{{39, 3}, {40, 4}, {40, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := regressionPane(40, 3)
			p.captureAndWrite([]byte("hello\x1b[?2004h\x1b[?1004h\x1b[?25l\x1b[5 q"))
			before := p.term.Mode()
			p.resize(0, 0, size[0]+1, size[1])
			if p.term.Mode() != before || p.term.Cursor().Shape != 5 {
				t.Fatalf("resize lost terminal state: modes %v -> %v; shape 5 -> %d", before, p.term.Mode(), p.term.Cursor().Shape)
			}
		})
	}
}

func TestRegressionClearAfterResize(t *testing.T) {
	for _, size := range [][2]int{{39, 3}, {40, 4}, {40, 2}} {
		t.Run(fmt.Sprint(size), func(t *testing.T) {
			p := regressionPane(40, 3)
			p.captureAndWrite([]byte(strings.Repeat("history\r\n", 20)))
			p.resize(0, 0, size[0]+1, size[1])
			p.captureAndWrite([]byte("fresh\r\nmore\r\nlines\r\n"))
			if p.sb.count == 0 {
				t.Fatal("setup produced no scrollback")
			}
			p.captureAndWrite([]byte("\x1b[3J"))
			if p.sb.count != 0 {
				t.Fatalf("ED3 failed after resize: %d saved rows remain", p.sb.count)
			}
		})
	}
}

func TestRegressionShortLineHistory(t *testing.T) {
	p := regressionPane(80, 3)
	for i := 0; i < 100; i++ {
		p.captureAndWrite([]byte(fmt.Sprintf("%03d\r\n", i)))
	}
	before := p.sb.count
	p.resize(0, 0, 80, 3)
	if p.sb.count != before {
		t.Fatalf("width 80 -> 79 discarded short-line history: %d -> %d rows", before, p.sb.count)
	}
}

func TestRegressionWideCharacterWrap(t *testing.T) {
	p := regressionPane(5, 3)
	p.captureAndWrite([]byte("界界AB"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(6, 3)
	renderPane(scr, p, testTheme())
	got, _, _, _ := scr.GetContent(0, 1) //nolint:staticcheck // inspect the rendered rune
	if got != 'B' {
		t.Fatalf("B should wrap after five display columns; row 2 starts with %q", got)
	}
}

func TestRegressionUnterminatedOSCBounded(t *testing.T) {
	data := append([]byte("\x1b]0;"), bytes.Repeat([]byte{'x'}, 2*oscMaxBuf)...)
	var stream ptyStream
	complete := stream.scan(data)
	if len(stream.pending) > oscMaxBuf {
		t.Fatalf("unterminated OSC bypasses scanner cap: complete=%d carry=%d", len(complete), len(stream.pending))
	}
	if got := string(stream.scan([]byte("\x1b\\recovered"))); got != "recovered" {
		t.Fatalf("oversized OSC recovery = %q", got)
	}
}

func TestRegressionKittySurvivesFirstPoll(t *testing.T) {
	done := make(chan struct{})
	p, err := NewPane(0, 0, 0, 40, 3, 100, "", []string{"/bin/sh", "-c", "printf '\033[>1u'; sleep 5"}, hostOSCColors{}, make(chan struct{}, 1), make(chan *Pane, 1), done, newOSCBuffer())
	if err != nil {
		t.Fatal(err)
	}
	defer close(done)
	defer p.close()
	deadline := time.Now().Add(500 * time.Millisecond)
	negotiated := false
	for time.Now().Before(deadline) {
		p.mu.Lock()
		negotiated = len(p.kittyStack) > 0
		p.mu.Unlock()
		if negotiated {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if !negotiated {
		t.Fatal("child failed to negotiate kitty mode")
	}
	time.Sleep(1200 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.kittyStack) == 0 {
		t.Fatal("first foreground poll erased currently running child's kitty negotiation")
	}
}

func TestRegressionKittyOwnership(t *testing.T) {
	for _, tc := range []struct {
		name              string
		owner, foreground int
		wantEnabled       bool
	}{
		{"current app", 12, 12, true},
		{"exited app", 12, 11, false},
		{"new app negotiated before poll", 13, 13, true},
		{"foreground unavailable", 12, 0, true},
		{"owner unavailable", 0, 12, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := &Pane{kittyStack: []int{1}, kittyOwnerPGID: tc.owner}
			p.clearStaleKittyMode(tc.foreground)
			if got := len(p.kittyStack) > 0; got != tc.wantEnabled {
				t.Fatalf("enabled = %v, want %v", got, tc.wantEnabled)
			}
		})
	}
}

func TestRegressionRepeatedTransientClear(t *testing.T) {
	p := regressionPane(10, 3)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.ptmx = r
	readDone := make(chan struct{})
	go func() { p.readPTY(make(chan struct{}, 1), newOSCBuffer()); close(readDone) }()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-readDone
	}()
	for n := 0; n < 2; n++ {
		if _, err := w.Write([]byte("\r   \r")); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		p.mu.Lock()
		held := p.transientLineClear
		p.mu.Unlock()
		if held {
			t.Fatalf("clear %d remains render-suppressed after timer expiry", n+1)
		}
	}
}

func TestRegressionSyncDoesNotFreezeOtherPane(t *testing.T) {
	p, other := regressionPane(10, 3), regressionPane(10, 3)
	other.x = 12
	p.captureAndWrite([]byte("\x1b[?2026h"))
	other.captureAndWrite([]byte("updated"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(23, 3)
	app := &App{screen: scr, root: &Node{left: &Node{pane: p}, right: &Node{pane: other}}, active: p, oscBuf: newOSCBuffer(), theme: testTheme()}
	app.render()
	got, _, _, _ := scr.GetContent(12, 0) //nolint:staticcheck // inspect the rendered rune
	if got != 'u' {
		t.Fatalf("other pane's output suppressed by active pane's sync mode: got %q", got)
	}
}

func TestRegressionMouseReleaseForwarding(t *testing.T) {
	for _, mode := range []int{1000, 1002, 1003} {
		t.Run(fmt.Sprint(mode), func(t *testing.T) {
			p := regressionPane(10, 3)
			f, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}()
			p.ptmx = f
			p.captureAndWrite([]byte(fmt.Sprintf("\x1b[?%dh\x1b[?1006h", mode)))
			app := &App{root: newLeaf(p, 0, 0, 11, 3), active: p}
			app.handleMouse(tcell.NewEventMouse(1, 1, tcell.Button1, tcell.ModNone))
			app.handleMouse(tcell.NewEventMouse(1, 1, tcell.ButtonNone, tcell.ModNone))
			if _, err := f.Seek(0, 0); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(f)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.HasSuffix(got, []byte("\x1b[<0;2;2m")) {
				t.Fatalf("mouse release not forwarded; child received %q", got)
			}
		})
	}
}

func TestRegressionSyncTimeout(t *testing.T) {
	p := regressionPane(10, 3)
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	p.ptmx = r
	redraw := make(chan struct{}, 1)
	readDone := make(chan struct{})
	go func() { p.readPTY(redraw, newOSCBuffer()); close(readDone) }()
	defer func() {
		if err := w.Close(); err != nil {
			t.Error(err)
		}
		<-readDone
	}()
	if _, err := w.Write([]byte("\x1b[?2026hheld")); err != nil {
		t.Fatal(err)
	}
	select {
	case <-redraw:
		p.mu.Lock()
		defer p.mu.Unlock()
		if p.term.Mode()&vt10x.ModeSync != 0 || !p.syncDeadline.IsZero() {
			t.Fatal("sync timeout did not release rendering")
		}
	case <-time.After(3 * syncUpdateTimeout):
		t.Fatal("missing redraw after abandoned synchronized update")
	}
}

func TestRegressionWideSelectionAndSearch(t *testing.T) {
	for _, tc := range []struct {
		text string
		cols int
	}{
		{"界界AB", 5}, {"abcd界B", 5}, {"界X", 10},
	} {
		t.Run(tc.text, func(t *testing.T) {
			p := regressionPane(tc.cols, 3)
			p.captureAndWrite([]byte(tc.text))
			p.selActive = true
			p.selAnchor = selPos{row: 0, col: 0}
			cur := p.term.Cursor()
			p.selCursor = selPos{row: cur.Y, col: cur.X - 1}
			if got := p.selText(); got != tc.text {
				t.Fatalf("copied %q, want %q", got, tc.text)
			}
		})
	}
	t.Run("search display columns", func(t *testing.T) {
		p := regressionPane(10, 3)
		p.captureAndWrite([]byte("a界界b"))
		app := &App{searchPane: p, searchQuery: "界界b"}
		app.runSearchScan()
		if len(app.searchMatches) != 1 || app.searchMatches[0].col != 1 || app.searchMatches[0].length != 5 {
			t.Fatalf("matches = %+v", app.searchMatches)
		}
		selectWord(p, selPos{row: 0, col: 2})
		if got := p.selText(); got != "a界界b" {
			t.Fatalf("word on wide continuation = %q", got)
		}
	})
}

func TestRegressionResizePreservesLinkIdentities(t *testing.T) {
	p := regressionPane(20, 3)
	p.captureAndWrite([]byte("\x1b]8;;https://old.example\x07old\x1b]8;;\x07\r\n"))
	p.rawBuf = nil // simulate the rolling raw buffer having evicted the first URL
	p.captureAndWrite([]byte("\x1b]8;;https://new.example\x07new\x1b]8;;\x07\r\none\r\ntwo\r\nthree\r\n"))
	p.resize(0, 0, 20, 3)
	if p.sb.count == 0 {
		t.Fatal("missing rebuilt scrollback")
	}
	if got := p.term.Link(p.sb.get(0)[0].Link); got != "https://new.example" {
		t.Fatalf("scrollback link = %q", got)
	}
}
