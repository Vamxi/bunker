package vt10x

import (
	"strings"
	"testing"
)

func TestGraphemeClusters(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		width      int
	}{
		{"accent", "e\u0301", 1}, {"family", "👩‍👩‍👧‍👦", 2},
		{"skin tone", "👍🏽", 2}, {"flag", "🇳🇱", 2}, {"keycap", "1️⃣", 2},
		{"variation selector", "❤️", 2},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(10, 2))
			for _, c := range tc.text {
				if _, err := term.Write([]byte(string(c))); err != nil {
					t.Fatal(err)
				}
			}
			g := term.Cell(0, 0)
			if g.Text() != tc.text || term.Cursor().X != tc.width {
				t.Fatalf("glyph=%+v cursor=%+v, want %q width %d", g, term.Cursor(), tc.text, tc.width)
			}
			if _, err := term.Write([]byte("Z")); err != nil {
				t.Fatal(err)
			}
			if term.Cell(tc.width, 0).Char != 'Z' {
				t.Fatal("next character used the wrong column")
			}
		})
	}
}

func TestGraphemeWidthGrowthAtMargin(t *testing.T) {
	term := New(WithSize(3, 2))
	if _, err := term.Write([]byte("ab❤️X")); err != nil {
		t.Fatal(err)
	}
	if term.Cell(2, 0).Width != -2 || term.Cell(0, 1).Text() != "❤️" || term.Cell(1, 1).Width != -1 || term.Cell(2, 1).Char != 'X' {
		t.Fatalf("bad wrapped cluster: %q", term.String())
	}
}

func TestGraphemeModeAndBounds(t *testing.T) {
	term := New(WithSize(8, 2))
	if term.QueryPrivateMode(2027) != '1' {
		t.Fatal("grapheme mode must default to enabled")
	}
	if _, err := term.Write([]byte("\x1b[?2027l👩‍👩")); err != nil {
		t.Fatal(err)
	}
	if term.Cursor().X != 4 || term.QueryPrivateMode(2027) != '2' {
		t.Fatal("legacy widths were not restored")
	}
	if _, err := term.Write([]byte("\x1bcA" + strings.Repeat("\u0301", 2000))); err != nil {
		t.Fatal(err)
	}
	if len(term.Cell(0, 0).Combining()) > 1024 || term.Cursor().X != 1 {
		t.Fatal("combining sequence not bounded")
	}
	if _, err := term.Write([]byte("\rB")); err != nil {
		t.Fatal(err)
	}
	if term.Cell(0, 0).Text() != "B" {
		t.Fatal("overwrite retained old combining marks")
	}
}

func TestAuditAttributesAndStatus(t *testing.T) {
	for _, tc := range []struct{ name, input, query, want string }{
		{"double underline keeps bold", "\x1b[1;21m", "m", "\x1bP1$r0;1;4:2m\x1b\\"},
		{"overline", "\x1b[53m", "m", "\x1bP1$r0;53m\x1b\\"},
		{"reset", "\x1b[53;55m", "m", "\x1bP1$r0m\x1b\\"},
		{"margins", "\x1b[2;4r", "r", "\x1bP1$r2;4r\x1b\\"},
		{"steady bar", "\x1b[5 q\x1b[?12l", " q", "\x1bP1$r6 q\x1b\\"},
		{"blinking underline", "\x1b[4 q\x1b[?12h", " q", "\x1bP1$r3 q\x1b\\"},
		{"unsupported", "", "nonsense", "\x1bP0$r\x1b\\"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(10, 5))
			if _, err := term.Write([]byte(tc.input)); err != nil {
				t.Fatal(err)
			}
			if got := term.StatusString(tc.query); got != tc.want {
				t.Fatalf("reply=%q want %q", got, tc.want)
			}
		})
	}
}
