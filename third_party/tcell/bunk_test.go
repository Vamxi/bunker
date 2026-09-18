package tcell

import (
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2/terminfo"
	"golang.org/x/text/transform"
)

func TestBunkKeypadIdentity(t *testing.T) {
	for _, tc := range []struct {
		input string
		key   Key
		ch    rune
	}{
		{"\x1bOp", KeyRune, '0'}, {"\x1bOu", KeyRune, '5'}, {"\x1bOM", KeyEnter, 0},
		{"\x1b[57404u", KeyRune, '5'}, {"\x1b[57414u", KeyEnter, 0},
	} {
		t.Run(tc.input, func(t *testing.T) {
			ch := make(chan Event, 4)
			NewInputProcessor(ch).ScanUTF8([]byte(tc.input))
			select {
			case ev := <-ch:
				key, ok := ev.(*EventKey)
				if !ok || key.Key() != tc.key || key.Rune() != tc.ch || key.Modifiers()&ModKeypad == 0 {
					t.Fatalf("event=%+v", ev)
				}
			default:
				t.Fatal("no keypad event")
			}
		})
	}
}

func TestBunkOverlineStyle(t *testing.T) {
	_, _, attrs := StyleDefault.Overline(true).Decompose()
	if attrs&AttrOverline == 0 {
		t.Fatal("overline not stored")
	}
	_, _, attrs = StyleDefault.Overline(true).Overline(false).Decompose()
	if attrs&AttrOverline != 0 {
		t.Fatal("overline not reset")
	}
}

func TestBunkOverlineTerminalOutput(t *testing.T) {
	s := &tScreen{ti: &terminfo.Terminfo{AttrOff: "\x1b[0m"}, w: 4, h: 1, buffering: true, encoder: transform.Nop}
	s.cells.Resize(4, 1)
	s.cells.Put(0, 0, "x", StyleDefault.Overline(true))
	s.cells.Put(1, 0, "y", StyleDefault)
	s.drawCell(0, 0)
	s.drawCell(1, 0)
	if got := s.buf.String(); got != "\x1b[0m\x1b[53mx\x1b[0my" {
		t.Fatalf("output=%q", got)
	}
}

func TestBunkCursorColorTerminalOutput(t *testing.T) {
	s := &tScreen{ti: &terminfo.Terminfo{XTermLike: true}, w: 4, h: 1, buffering: true}
	s.cells.Resize(4, 1)
	s.prepareCursorStyles()
	s.cursorColor = NewRGBColor(0x11, 0x22, 0x33)
	s.showCursor()
	if !strings.Contains(s.buf.String(), "\x1b]12;#112233\a") {
		t.Fatalf("output=%q", s.buf.String())
	}
	s.buf.Reset()
	s.cursorColor = ColorReset
	s.showCursor()
	if !strings.Contains(s.buf.String(), "\x1b]112\a") {
		t.Fatalf("reset output=%q", s.buf.String())
	}
}

func TestBunkKeycapWidth(t *testing.T) {
	var cells CellBuffer
	cells.Resize(4, 1)
	_, width := cells.Put(0, 0, "1️⃣", StyleDefault)
	if width != 2 {
		t.Fatalf("keycap width=%d", width)
	}
}

func TestBunkLongGraphemeOutput(t *testing.T) {
	s := &tScreen{encoder: transform.Nop}
	cluster := "a" + strings.Repeat("\u0301", 100)
	if got := string(s.encodeStr(cluster)); got != cluster {
		t.Fatalf("long grapheme lost: %q", got)
	}
}
