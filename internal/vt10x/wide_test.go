package vt10x

import "testing"

func TestWideCellOperations(t *testing.T) {
	for _, tc := range []struct {
		name, output string
		cols, x, y   int
		want         rune
	}{
		{"wrap", "界界AB", 5, 0, 1, 'B'},
		{"wrap before wide", "abcd界", 5, 0, 1, '界'},
		{"one column", "界", 1, 0, 0, '\uFFFD'},
		{"overwrite lead", "界X\rA", 5, 1, 0, ' '},
		{"overwrite continuation", "界X\x1b[2GB", 5, 0, 0, ' '},
		{"erase lead", "界X\r\x1b[X", 5, 1, 0, ' '},
		{"erase continuation", "界X\x1b[2G\x1b[X", 5, 0, 0, ' '},
		{"insert before wide", "界X\r\x1b[@", 5, 1, 0, '界'},
		{"insert inside wide", "界X\x1b[2G\x1b[@", 5, 0, 0, ' '},
		{"delete lead", "界X\r\x1b[P", 5, 0, 0, ' '},
		{"delete continuation", "界X\x1b[2G\x1b[P", 5, 0, 0, ' '},
	} {
		t.Run(tc.name, func(t *testing.T) {
			term := New(WithSize(tc.cols, 3))
			if _, err := term.Write([]byte(tc.output)); err != nil {
				t.Fatal(err)
			}
			if got := term.Cell(tc.x, tc.y).Char; got != tc.want {
				t.Fatalf("cell (%d,%d) = %q, want %q", tc.x, tc.y, got, tc.want)
			}
			for y := 0; y < 3; y++ {
				for x := 0; x < tc.cols; x++ {
					g := term.RawCell(x, y)
					if g.Width == 2 && (x+1 == tc.cols || term.RawCell(x+1, y).Width != -1) {
						t.Fatal("wide glyph has no continuation")
					}
					if g.Width == -1 && (x == 0 || term.RawCell(x-1, y).Width != 2) {
						t.Fatal("orphan continuation")
					}
				}
			}
		})
	}
}

func TestReplaceScreenPreservesState(t *testing.T) {
	clears := 0
	term := New(WithSize(10, 3), WithScrollbackClearCallback(func() { clears++ }))
	if _, err := term.Write([]byte("\x1b]0;title\x07\x1b]11;#112233\x07\x1b[?2004h\x1b[?1004h\x1b[?25l\x1b[5 q\x1b[31m\x1b]8;;https://old.example\x07A\x1b]8;;\x07")); err != nil {
		t.Fatal(err)
	}
	before := term.Cursor()
	mode := term.Mode()
	source := New(WithSize(10, 3))
	if _, err := source.Write([]byte("\x1b]8;;https://new.example\x07界X")); err != nil {
		t.Fatal(err)
	}
	row := make([]Glyph, 10)
	for x := range row {
		row[x] = source.RawCell(x, 0)
	}
	term.ReplaceScreen(10, 4, [][]Glyph{row}, source.Cursor(), source)
	if term.Mode() != mode || term.Cursor().Shape != before.Shape || term.Cursor().Attr != before.Attr || term.Title() != "title" {
		t.Fatal("non-screen terminal state changed")
	}
	if got := term.Link(term.Cell(0, 0).Link); got != "https://new.example" {
		t.Fatalf("remapped link = %q", got)
	}
	if term.RawCell(0, 0).BG != DefaultBG || term.Cell(0, 0).BG != Color(0x112233) {
		t.Fatal("default colour identity/override was lost")
	}
	if _, err := term.Write([]byte("\x1b]111\x07")); err != nil {
		t.Fatal(err)
	}
	if term.Cell(0, 0).BG != DefaultBG {
		t.Fatal("dynamic colour reset no longer affects copied cells")
	}
	if _, err := term.Write([]byte("\x1b[3J")); err != nil {
		t.Fatal(err)
	}
	if clears != 1 {
		t.Fatal("scrollback clear callback lost")
	}
	imported := term.ImportGlyph(source.RawCell(0, 0), source)
	if term.Link(imported.Link) != "https://new.example" {
		t.Fatal("glyph import lost link")
	}
}

func TestReplaceScreenExtendsTabStops(t *testing.T) {
	term := New(WithSize(10, 3))
	term.ReplaceScreen(24, 3, nil, term.Cursor(), term)
	if _, err := term.Write([]byte("\t\tX")); err != nil {
		t.Fatal(err)
	}
	if term.Cell(16, 0).Char != 'X' {
		t.Fatal("tab stops were not extended to the new width")
	}
}
