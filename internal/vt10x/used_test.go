package vt10x

import (
	"fmt"
	"io"
	"math/rand/v2"
	"strings"
	"testing"
)

// checkUsed verifies the invariant clear relies on: in both screens, every
// cell at or past a row's used count is blankGlyph.
func checkUsed(t *testing.T, term *terminal, step int, token string) {
	t.Helper()
	for _, screen := range []struct {
		name  string
		lines []line
	}{{"primary", term.lines}, {"alternate", term.altLines}} {
		if term.mode&ModeAltScreen != 0 {
			screen.name = map[string]string{"primary": "alternate", "alternate": "primary"}[screen.name]
		}
		for y, l := range screen.lines {
			if len(l.cells) != term.cols || l.used < 0 || l.used > term.cols {
				t.Fatalf("step %d (%q): %s row %d: %d cells, used %d, cols %d", step, token, screen.name, y, len(l.cells), l.used, term.cols)
			}
			for x := l.used; x < len(l.cells); x++ {
				if l.cells[x] != blankGlyph {
					t.Fatalf("step %d (%q): %s row %d cell %d is %+v past used %d", step, token, screen.name, y, x, l.cells[x], l.used)
				}
			}
		}
	}
}

// usedTokens are the operations that write, erase, move, or shift cells.
func usedToken(r *rand.Rand, cols, rows int) string {
	n := func(max int) int { return 1 + r.IntN(max) }
	switch r.IntN(35) {
	case 0, 1, 2, 3:
		return strings.Repeat(string(rune('a'+r.IntN(26))), n(cols+3))
	case 4:
		return "\r\n"
	case 5:
		return "\n"
	case 6:
		return "\r"
	case 7:
		return []string{"\t", "\b", "\x1bM", "\x1bD", "\x1bE", "\x1b7", "\x1b8"}[r.IntN(7)]
	case 8:
		return []string{"日本", "漢", "😀", "👩\u200d💻", "e\u0301", "1\ufe0f\u20e3", "\u0301", "\u200d"}[r.IntN(8)]
	case 9:
		return fmt.Sprintf("\x1b[%d;%dH", n(rows+2), n(cols+2))
	case 10:
		return fmt.Sprintf("\x1b[%dJ", r.IntN(4))
	case 11:
		return fmt.Sprintf("\x1b[%dK", r.IntN(3))
	case 12:
		return fmt.Sprintf("\x1b[%d@", n(cols))
	case 13:
		return fmt.Sprintf("\x1b[%dP", n(cols))
	case 14:
		return fmt.Sprintf("\x1b[%dX", n(cols))
	case 15:
		return fmt.Sprintf("\x1b[%dL", n(rows))
	case 16:
		return fmt.Sprintf("\x1b[%dM", n(rows))
	case 17:
		return fmt.Sprintf("\x1b[%dS", n(rows))
	case 18:
		return fmt.Sprintf("\x1b[%dT", n(rows))
	case 19:
		top := n(rows)
		return fmt.Sprintf("\x1b[%d;%dr", top, top+r.IntN(rows))
	case 20:
		return "\x1b[r"
	case 21, 22:
		// Colours, including a non-default background that erases paint.
		return []string{"\x1b[0m", "\x1b[41m", "\x1b[7m", "\x1b[1;32m", "\x1b[48;5;236m", "\x1b[49m", "\x1b[4:3;58;5;1m", "\x1b[39;49m"}[r.IntN(8)]
	case 23:
		return []string{"\x1b[?7l", "\x1b[?7h", "\x1b[4h", "\x1b[4l"}[r.IntN(4)]
	case 24:
		return []string{"\x1b[?1049h", "\x1b[?1049l", "\x1b[?47h", "\x1b[?47l"}[r.IntN(4)]
	case 25:
		return "\x1b#8" // DECALN fills the screen
	case 26:
		if r.IntN(8) == 0 {
			return "\x1bc" // RIS
		}
		return "x"
	case 27:
		return fmt.Sprintf("\x1b[%dG", n(cols+2))
	case 28:
		return fmt.Sprintf("\x1b[%dd", n(rows+2))
	case 29:
		return "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\"
	case 30:
		// A small Kitty image, then sometimes delete it.
		if r.IntN(2) == 0 {
			return "\x1b_Ga=T,i=3,f=24,s=1,v=1,c=2,r=1;/wAA\x1b\\"
		}
		return "\x1b_Ga=d,d=a\x1b\\"
	case 31:
		return fmt.Sprintf("\x1b[%db", n(cols))
	case 32:
		return fmt.Sprintf("\x1b[%dA\x1b[%dC", n(rows), n(cols))
	case 33:
		// A cluster that turns wide at the last column wraps with padding.
		return fmt.Sprintf("\x1b[%dG", cols) + []string{"1\ufe0f\u20e3", "\u2764\ufe0f", "#\ufe0f\u20e3"}[r.IntN(3)]
	case 34:
		return "\x1b[3J"
	}
	panic("unreachable")
}

// TestUsedInvariant drives random streams through a terminal with a
// scrollback ring and checks, after every operation, that no cell past a
// row's used count differs from blank, so clearing only used cells is the
// same as clearing whole rows. Rows handed to the ring must obey the same
// rule for the count they come with.
func TestUsedInvariant(t *testing.T) {
	for seed := range uint64(300) {
		r := rand.New(rand.NewPCG(seed, 0x6275_6e6b))
		cols, rows := 2+r.IntN(30), 1+r.IntN(12)
		ring := newSwapRing(1 + r.IntN(8))
		var term *terminal
		swap := func(row []Glyph, used int) ([]Glyph, int) {
			for x := used; x < len(row); x++ {
				if row[x] != blankGlyph {
					t.Fatalf("seed %d: row handed to scrollback has cell %d %+v past used %d", seed, x, row[x], used)
				}
			}
			return ring.swap(row, used)
		}
		term = newTerminal(TerminalInfo{w: io.Discard, cols: cols, rows: rows, cellWidth: 8, cellHeight: 16, scrollSwapCb: swap})
		for step := range 400 {
			var token string
			switch r.IntN(60) {
			case 0:
				cols, rows = 2+r.IntN(30), 1+r.IntN(12)
				token = fmt.Sprintf("resize %dx%d", cols, rows)
				term.Resize(cols, rows)
			case 1:
				// Reflow installs cells from elsewhere, some wider than the grid.
				cells := make([][]Glyph, rows)
				for y := range cells {
					for range r.IntN(cols + 2) {
						cells[y] = append(cells[y], Glyph{Char: rune('A' + r.IntN(26)), Width: 1, FG: DefaultFG, BG: Color(r.IntN(3)) + DefaultBG - 1, UL: DefaultUL})
					}
				}
				token = "ReplaceScreen"
				term.ReplaceScreen(cols, rows, cells, term.Cursor(), term)
			default:
				token = usedToken(r, cols, rows)
				term.Write([]byte(token)) //nolint:errcheck
			}
			checkUsed(t, term, step, token)
		}
	}
}

// Scrolled-off short lines keep blank tails, and recycled scrollback rows
// are only cleared as far as they were used.
func TestUsedStaysShortForShortLines(t *testing.T) {
	term := newTerminal(TerminalInfo{w: io.Discard, cols: 80, rows: 5, scrollSwapCb: newSwapRing(4).swap})
	for i := range 50 {
		fmt.Fprintf(term, "%d\r\n", i)
	}
	for y, l := range term.lines {
		if l.used > 2 {
			t.Errorf("row %d: used %d after short lines, want at most 2", y, l.used)
		}
	}
}
