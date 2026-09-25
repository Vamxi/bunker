package main

import (
	"os"
	"strings"
	"testing"

	"bunker/internal/vt10x"
)

func TestSbRing_SwapInEvictsOldest(t *testing.T) {
	r := sbRing{maxLines: 2}
	a, b, c := makeGlyphRowN(3, 'A'), makeGlyphRowN(3, 'B'), makeGlyphRowN(3, 'C')
	if got := r.swapIn(a); len(got) != 3 || &got[0] == &a[0] {
		t.Fatalf("filling ring must return a fresh buffer")
	}
	r.swapIn(b)
	evicted := r.swapIn(c)
	if &evicted[0] != &a[0] {
		t.Fatalf("full ring must hand back the evicted oldest slot")
	}
	if r.count != 2 || r.get(0)[0].Char != 'B' || r.get(1)[0].Char != 'C' {
		t.Fatalf("ring = %c %c, want B C", r.get(0)[0].Char, r.get(1)[0].Char)
	}
}

func TestSbRing_SwapInDisabled(t *testing.T) {
	r := sbRing{maxLines: 0}
	if r.swapIn(makeGlyphRowN(3, 'A')) != nil || r.count != 0 {
		t.Fatal("scrollback off must not take the row")
	}
}

// TestCaptureAndWrite_ScrollSwap runs the NewPane wiring through enough
// output to wrap the ring several times, then checks scrollback and screen.
func TestCaptureAndWrite_ScrollSwap(t *testing.T) {
	const cols, rows, keep = 4, 3, 5
	p := &Pane{scrollbackLines: keep, sb: sbRing{maxLines: keep}}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollSwapCallback(p.onScrollSwap))

	var out strings.Builder
	for i := range 20 {
		if i > 0 {
			out.WriteString("\r\n")
		}
		out.WriteString(strings.Repeat(string(rune('a'+i)), cols))
	}
	p.mu.Lock()
	p.captureAndWrite([]byte(out.String()))
	p.mu.Unlock()

	// 20 lines on a 3-row screen: 17 scrolled off, ring keeps the last 5 (m..q).
	if p.sb.count != keep {
		t.Fatalf("sb.count = %d, want %d", p.sb.count, keep)
	}
	for i := range keep {
		row := p.sb.get(i)
		want := rune('m' + i)
		for c, g := range row {
			if g.Char != want {
				t.Fatalf("sb row %d col %d = %q, want %q", i, c, g.Char, want)
			}
		}
	}
	for r := range rows {
		if got, want := p.term.Cell(0, r).Char, rune('r'+r); got != want {
			t.Fatalf("screen row %d = %q, want %q", r, got, want)
		}
	}
}

// TestCaptureAndWrite_ScrollSwapAltScreen: alt-screen scrolls stay out of
// scrollback, exactly as with the copying callback.
func TestCaptureAndWrite_ScrollSwapAltScreen(t *testing.T) {
	const cols, rows = 4, 2
	p := &Pane{scrollbackLines: 10, sb: sbRing{maxLines: 10}}
	p.term = vt10x.New(vt10x.WithSize(cols, rows), vt10x.WithScrollSwapCallback(p.onScrollSwap))
	p.mu.Lock()
	p.captureAndWrite([]byte("\x1b[?1049haaaa\r\nbbbb\r\ncccc\r\ndddd"))
	p.mu.Unlock()
	if p.sb.count != 0 {
		t.Fatalf("alt screen pushed %d rows", p.sb.count)
	}
}

func TestLoadConfig_ScrollbackMB(t *testing.T) {
	path := t.TempDir() + "/config.toml"
	if err := os.WriteFile(path, []byte("scrollback = 5000\nscrollback_mb = 1.5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Scrollback != 5000 || cfg.ScrollbackBytes != 3<<19 {
		t.Fatalf("got lines=%d bytes=%d", cfg.Scrollback, cfg.ScrollbackBytes)
	}
}

func TestPane_SbCapacity(t *testing.T) {
	for _, tt := range []struct {
		name               string
		lines, bytes, cols int
		want               int
	}{
		{"lines only", 10000, 0, 200, 10000},
		{"bytes lower", 10000, 1 << 20, 100, (1 << 20) / (100 * glyphBytes)},
		{"lines lower", 1000, 1 << 30, 100, 1000},
		{"wider pane keeps fewer rows", 10000, 1 << 20, 200, (1 << 20) / (200 * glyphBytes)},
		{"tiny cap keeps a floor", 10000, 1, 100, 100},
	} {
		t.Run(tt.name, func(t *testing.T) {
			p := &Pane{scrollbackLines: tt.lines, scrollbackBytes: tt.bytes}
			if got := p.sbCapacity(tt.cols); got != tt.want {
				t.Fatalf("sbCapacity(%d) = %d, want %d", tt.cols, got, tt.want)
			}
		})
	}
}

// TestPasteCannotEscapeBracketedMode: clipboard text (which any program can
// set via OSC 52) must not end bracketed paste early, even with nested or
// C1 forms of the end marker.
func TestPasteCannotEscapeBracketedMode(t *testing.T) {
	for _, evil := range []string{
		"hi\x1b[201~rm -rf ~\n",
		"hi\x1b[20\x1b[201~1~id\n", // reassembles after a one-pass removal
		"hi\u009b201~id\n",         // C1 CSI
		"\x1b[200~nested\x1b[201~",
	} {
		got := string(pasteBytes(evil, true))
		inner := strings.TrimSuffix(strings.TrimPrefix(got, "\x1b[200~"), "\x1b[201~")
		if strings.ContainsAny(inner, "\x1b\u009b") {
			t.Errorf("paste of %q leaks an escape: %q", evil, got)
		}
		if !strings.HasPrefix(got, "\x1b[200~") || !strings.HasSuffix(got, "\x1b[201~") {
			t.Errorf("paste of %q is not bracketed: %q", evil, got)
		}
	}
	if got := string(pasteBytes("a\r\nb\nc", false)); got != "a\rb\rc" {
		t.Errorf("unbracketed line endings: %q", got)
	}
}
