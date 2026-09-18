package vt10x

import (
	"bytes"
	"strings"
	"testing"

	"bunk/internal/graphics"
)

func writeGraphics(t *testing.T, term Terminal, data string) {
	t.Helper()
	if _, err := term.Write([]byte(data)); err != nil {
		t.Fatal(err)
	}
}

func TestGraphicsCellsCursorAndDeletion(t *testing.T) {
	var replies bytes.Buffer
	term := New(WithSize(6, 4), WithWriter(&replies))
	writeGraphics(t, term, "\x1b_Ga=T,i=1,p=2,f=24,s=1,v=1,c=2,r=1,C=1;/wAA\x1b\\")
	for x := 0; x < 2; x++ {
		if g := term.Cell(x, 0); g.Image == nil || g.Image.Top.R != 255 || g.Char != '▀' {
			t.Fatalf("cell %d: %+v", x, g)
		}
	}
	if cur := term.Cursor(); cur.X != 0 || cur.Y != 0 {
		t.Fatal("C=1 moved cursor")
	}
	if replies.String() != "\x1b_Gi=1,p=2;OK\x1b\\" {
		t.Fatalf("reply=%q", replies.String())
	}
	writeGraphics(t, term, "\x1b[2;2H\x1b_Ga=p,i=1,p=3,c=2,r=1;\x1b\\")
	if cur := term.Cursor(); cur.X != 3 || cur.Y != 2 {
		t.Fatalf("placement cursor=%+v", cur)
	}
	writeGraphics(t, term, "\x1b_Ga=d,d=i,i=1,p=2\x1b\\")
	if term.Cell(0, 0).Image != nil || term.Cell(1, 1).Image == nil {
		t.Fatal("placement deletion affected wrong cells")
	}
	writeGraphics(t, term, "\x1b_Ga=d,d=p,x=3,y=2\x1b\\")
	if term.Cell(1, 1).Image != nil || term.Cell(2, 1).Image != nil {
		t.Fatal("point deletion did not remove whole placement")
	}
	writeGraphics(t, term, "\x1b_Ga=p,i=1,c=1,r=1,C=1\x1b\\Z")
	if term.Cell(3, 2).Image != nil || term.Cell(3, 2).Char != 'Z' {
		t.Fatal("text did not overwrite image")
	}
}

func TestGraphicsClippingScrollingAndAltScreen(t *testing.T) {
	var history [][]Glyph
	term := New(WithSize(3, 2), WithScrollCallback(func(row []Glyph) { history = append(history, append([]Glyph(nil), row...)) }))
	writeGraphics(t, term, "\x1b[1;3H\x1b_Ga=T,f=24,s=1,v=1,c=4,r=3,C=1;/wAA\x1b\\")
	if len(history) != 0 || term.Cell(0, 1).Image != nil || term.Cell(2, 1).Image == nil {
		t.Fatal("image wrapped or scrolled with C=1")
	}
	writeGraphics(t, term, "\x1b[?1049h\x1b[2J\x1b[H")
	if term.Cell(2, 0).Image != nil {
		t.Fatal("primary image leaked into alternate screen")
	}
	writeGraphics(t, term, "\x1b[?1049l")
	if term.Cell(2, 0).Image == nil {
		t.Fatal("primary image was lost")
	}
	writeGraphics(t, term, "\x1b[H\x1bP0;1q#1;2;0;100;0!8~-!8~-!8~\x1b\\")
	if len(history) == 0 || history[0][0].Image == nil {
		t.Fatal("image did not enter scrollback")
	}
	writeGraphics(t, term, "\x1b[2J")
	for y := 0; y < 2; y++ {
		for x := 0; x < 3; x++ {
			if term.Cell(x, y).Image != nil {
				t.Fatal("erase retained image")
			}
		}
	}
}

func TestGraphicsOversizedAndCancelledStrings(t *testing.T) {
	term := New(WithSize(10, 2))
	writeGraphics(t, term, "\x1b_G"+strings.Repeat("A", graphics.MaxSequenceBytes+1)+"\x1b\\OK")
	if !strings.HasPrefix(term.String(), "OK") {
		t.Fatal("oversized image escaped into grid")
	}
	writeGraphics(t, term, "\r\x1b_Gi=1,m=1;AAAA\x1b\\\x1b_Gm=0;\x18Z")
	if term.Cell(0, 0).Char != 'Z' {
		t.Fatal("cancelled image corrupted parser")
	}
	writeGraphics(t, term, "\x1b_Gm=0;AAAA\x1b\\")
	if term.Cell(1, 0).Image != nil {
		t.Fatal("cancelled transfer was resumed")
	}
}

func TestLongOSCHyperlinkBudget(t *testing.T) {
	term := New(WithSize(5, 2))
	state := term.(*terminal).State
	// Simulate an almost-full table without allocating its entire byte budget.
	state.linkBytes = maxLinkBytes - 8
	writeGraphics(t, term, "\x1b]8;;123456789\x1b\\X")
	if term.Cell(0, 0).Link != 0 || len(state.links) != 0 {
		t.Fatal("hyperlink exceeded byte budget")
	}
	writeGraphics(t, term, "\x1b]8;;12345678\x1b\\Y")
	if term.Cell(1, 0).Link == 0 || state.linkBytes != maxLinkBytes {
		t.Fatal("bounded hyperlink was not interned")
	}
	writeGraphics(t, term, "\x1b]8;;12345678\x1b\\Z")
	if term.Cell(2, 0).Link != term.Cell(1, 0).Link {
		t.Fatal("existing link unavailable at capacity")
	}
}

func TestGraphicsEmbeddedEscapeDoesNotLeak(t *testing.T) {
	for _, prefix := range []string{"\x1b_Ga=T,f=24,s=1,v=1;", "\x1b]1337;File=inline=1:", "\x1bPq"} {
		t.Run(prefix, func(t *testing.T) {
			term := New(WithSize(20, 3))
			writeGraphics(t, term, prefix+"\x1b[2;2HLEAK\x1b\\OK")
			if term.Cell(0, 0).Char != 'O' || term.Cell(1, 0).Char != 'K' || strings.Contains(term.String(), "LEAK") {
				t.Fatalf("malformed image escaped into grid: %q", term.String())
			}
		})
	}
}
