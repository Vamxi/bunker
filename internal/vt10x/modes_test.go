package vt10x

// TDD tests for terminal state that bunk previously tracked manually in pane.go.
// These tests are written BEFORE the implementation so they fail first, then
// pass once the features are added to vt10x.
//
// Features covered:
//   - DECSET 2004 (bracketed paste)      → ModeSetPaste
//   - DECSET 2026 (synchronized update)  → ModeSync
//   - DECSCUSR (cursor shape)            → Cursor.Shape
//   - QueryPrivateMode()                 → centralized DECRQM responses

import "testing"

// newTestTerm creates a fresh terminal with the given dimensions using the
// internal newTerminal constructor so Write() is available in package tests.
func newTestTerm(cols, rows int) *terminal {
	return newTerminal(TerminalInfo{w: nil, cols: cols, rows: rows})
}

// ---------------------------------------------------------------------------
// DECSET 2004 — Bracketed paste mode
// ---------------------------------------------------------------------------

func TestMode2004_BracketedPaste_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("bracketed paste must be off by default")
	}
}

func TestMode2004_BracketedPaste_EnableDisable(t *testing.T) {
	term := newTestTerm(80, 24)

	term.Write([]byte("\x1b[?2004h")) //nolint:errcheck
	if term.Mode()&ModeSetPaste == 0 {
		t.Fatal("ModeSetPaste should be set after \\x1b[?2004h")
	}

	term.Write([]byte("\x1b[?2004l")) //nolint:errcheck
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("ModeSetPaste should be clear after \\x1b[?2004l")
	}
}

func TestMode2004_BracketedPaste_LastWins(t *testing.T) {
	// When enable and disable appear in the same chunk, last one wins.
	term := newTestTerm(80, 24)

	// enable then disable — disable wins
	term.Write([]byte("\x1b[?2004h\x1b[?2004l")) //nolint:errcheck
	if term.Mode()&ModeSetPaste != 0 {
		t.Fatal("disable after enable in same write: ModeSetPaste should be clear")
	}

	// disable then enable — enable wins
	term.Write([]byte("\x1b[?2004l\x1b[?2004h")) //nolint:errcheck
	if term.Mode()&ModeSetPaste == 0 {
		t.Fatal("enable after disable in same write: ModeSetPaste should be set")
	}
}

// ---------------------------------------------------------------------------
// DECSET 2026 — Synchronized update mode
// ---------------------------------------------------------------------------

func TestMode2026_SyncUpdate_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Mode()&ModeSync != 0 {
		t.Fatal("sync update must be off by default")
	}
}

func TestMode2026_SyncUpdate_EnableDisable(t *testing.T) {
	term := newTestTerm(80, 24)

	term.Write([]byte("\x1b[?2026h")) //nolint:errcheck
	if term.Mode()&ModeSync == 0 {
		t.Fatal("ModeSync should be set after \\x1b[?2026h")
	}

	term.Write([]byte("\x1b[?2026l")) //nolint:errcheck
	if term.Mode()&ModeSync != 0 {
		t.Fatal("ModeSync should be clear after \\x1b[?2026l")
	}
}

// ---------------------------------------------------------------------------
// DECSCUSR — Set cursor style
// ESC [ Ps SP q   (SP = 0x20, final byte = 'q')
// ---------------------------------------------------------------------------

func TestDECSCUSR_CursorShape_Default(t *testing.T) {
	term := newTestTerm(80, 24)
	if term.Cursor().Shape != 0 {
		t.Fatalf("initial cursor shape = %d, want 0", term.Cursor().Shape)
	}
}

func TestDECSCUSR_CursorShape_AllValues(t *testing.T) {
	for ps := 0; ps <= 6; ps++ {
		term := newTestTerm(80, 24)
		seq := []byte{0x1b, '[', byte('0' + ps), ' ', 'q'}
		term.Write(seq) //nolint:errcheck
		if got := term.Cursor().Shape; got != ps {
			t.Errorf("DECSCUSR %d: Cursor.Shape = %d, want %d", ps, got, ps)
		}
	}
}

func TestDECSCUSR_CursorShape_DefaultReset(t *testing.T) {
	// ESC [ SP q (no explicit digit → Ps=0) should reset to 0 (default).
	term := newTestTerm(80, 24)
	term.Write([]byte{0x1b, '[', '5', ' ', 'q'}) //nolint:errcheck // set blinking bar
	if got := term.Cursor().Shape; got != 5 {
		t.Fatalf("prerequisite failed: shape = %d after ESC[5 q, want 5", got)
	}
	term.Write([]byte{0x1b, '[', ' ', 'q'}) //nolint:errcheck // ESC[ SP q = reset
	if got := term.Cursor().Shape; got != 0 {
		t.Fatalf("shape = %d after ESC[ q, want 0 (default)", got)
	}
}

func TestDECSCUSR_CursorShape_LastOccurrence(t *testing.T) {
	term := newTestTerm(80, 24)
	term.Write([]byte{ //nolint:errcheck
		0x1b, '[', '2', ' ', 'q', // steady underline
		0x1b, '[', '4', ' ', 'q', // blinking underline
	})
	if got := term.Cursor().Shape; got != 4 {
		t.Errorf("last DECSCUSR wins: shape = %d, want 4", got)
	}
}

func TestDECSCUSR_CursorShape_PlainText_Unaffected(t *testing.T) {
	term := newTestTerm(80, 24)
	term.Write([]byte("hello q world")) //nolint:errcheck
	if got := term.Cursor().Shape; got != 0 {
		t.Errorf("plain 'q' in text changed cursor shape to %d", got)
	}
}

// ---------------------------------------------------------------------------
// QueryPrivateMode — centralized DECRQM answers
// ---------------------------------------------------------------------------

func TestQueryPrivateMode_UnknownMode(t *testing.T) {
	term := newTestTerm(80, 24)
	for _, m := range []int{0, 99, 9999} {
		got := term.QueryPrivateMode(m)
		if got != '0' {
			t.Errorf("QueryPrivateMode(%d) = %c, want '0' (not recognized)", m, got)
		}
	}
}

func TestQueryPrivateMode_KnownModes_DefaultReset(t *testing.T) {
	term := newTestTerm(80, 24)
	// These are all reset (2) by default.
	for _, m := range []int{1, 1000, 1002, 1003, 1004, 1006, 1049, 2004, 2026} {
		got := term.QueryPrivateMode(m)
		if got != '2' {
			t.Errorf("QueryPrivateMode(%d) default = %c, want '2' (reset)", m, got)
		}
	}
	// Mode 7 (DECAWM, auto-wrap) is ON by default.
	if got := term.QueryPrivateMode(7); got != '1' {
		t.Errorf("QueryPrivateMode(7) default = %c, want '1' (DECAWM set by default)", got)
	}
}

func TestQueryPrivateMode_Mode25_CursorVisible_Default(t *testing.T) {
	// Mode 25 = DECTCEM. Cursor is visible by default → ModeHide is off → return '1'.
	term := newTestTerm(80, 24)
	if got := term.QueryPrivateMode(25); got != '1' {
		t.Errorf("QueryPrivateMode(25) default = %c, want '1' (cursor visible)", got)
	}
	// Hide cursor, then query
	term.Write([]byte("\x1b[?25l")) //nolint:errcheck
	if got := term.QueryPrivateMode(25); got != '2' {
		t.Errorf("QueryPrivateMode(25) after hide = %c, want '2'", got)
	}
}

func TestQueryPrivateMode_Mode2004_ReflectsState(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(2004); got != '2' {
		t.Fatalf("2004 before enable = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?2004h")) //nolint:errcheck
	if got := term.QueryPrivateMode(2004); got != '1' {
		t.Fatalf("2004 after enable = %c, want '1'", got)
	}
	term.Write([]byte("\x1b[?2004l")) //nolint:errcheck
	if got := term.QueryPrivateMode(2004); got != '2' {
		t.Fatalf("2004 after disable = %c, want '2'", got)
	}
}

func TestQueryPrivateMode_Mode2026_ReflectsState(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(2026); got != '2' {
		t.Fatalf("2026 before enable = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?2026h")) //nolint:errcheck
	if got := term.QueryPrivateMode(2026); got != '1' {
		t.Fatalf("2026 after enable = %c, want '1'", got)
	}
}

func TestQueryPrivateMode_Mode1049_AltScreen(t *testing.T) {
	term := newTestTerm(80, 24)

	if got := term.QueryPrivateMode(1049); got != '2' {
		t.Fatalf("1049 before alt screen = %c, want '2'", got)
	}
	term.Write([]byte("\x1b[?1049h")) //nolint:errcheck
	if got := term.QueryPrivateMode(1049); got != '1' {
		t.Fatalf("1049 after entering alt screen = %c, want '1'", got)
	}
}

// ---------------------------------------------------------------------------
// ESC c (RIS — Reset to Initial State)
// ---------------------------------------------------------------------------

func TestRIS_ClearsFullScreen(t *testing.T) {
	// Bug: reset() called clear(0, 0, rows-1, cols-1) — swapped axes.
	// On a wide terminal (cols >> rows) only the first `rows` columns were
	// blanked, leaving the rest of each line intact. ESC c should blank every
	// cell in the grid.
	const cols, rows = 220, 24
	term := newTestTerm(cols, rows)

	// Fill every cell with 'X'.
	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			term.Write([]byte("X")) //nolint:errcheck
		}
	}

	// ESC c = RIS
	term.Write([]byte("\x1bc")) //nolint:errcheck

	for r := 0; r < rows; r++ {
		for c := 0; c < cols; c++ {
			g := term.Cell(c, r)
			if g.Char != 0 && g.Char != ' ' {
				t.Errorf("cell(%d,%d) = %q after RIS, want blank", c, r, g.Char)
				return
			}
		}
	}
}

// ---------------------------------------------------------------------------
// OSC 110/111/112 — reset dynamic fg/bg/cursor color
// ---------------------------------------------------------------------------

// TestOSC110_111_112_ResetOverrides verifies that dynamic colour resets clear
// the corresponding OSC 10/11/12 overrides instead of being silently ignored.
func TestOSC110_111_112_ResetOverrides(t *testing.T) {
	term := newTestTerm(80, 24)
	term.Write([]byte("\x1b]10;rgb:1111/2222/3333\x07")) //nolint:errcheck
	term.Write([]byte("\x1b]11;rgb:4444/5555/6666\x07")) //nolint:errcheck
	term.Write([]byte("\x1b]12;rgb:7777/8888/9999\x07")) //nolint:errcheck

	if _, ok := term.ColorOverride(DefaultFG); !ok {
		t.Fatal("DefaultFG override missing after OSC 10 set")
	}
	if _, ok := term.ColorOverride(DefaultBG); !ok {
		t.Fatal("DefaultBG override missing after OSC 11 set")
	}
	if _, ok := term.ColorOverride(DefaultCursor); !ok {
		t.Fatal("DefaultCursor override missing after OSC 12 set")
	}

	term.Write([]byte("\x1b]110\x07")) //nolint:errcheck // reset fg
	term.Write([]byte("\x1b]111\x07")) //nolint:errcheck // reset bg
	term.Write([]byte("\x1b]112\x07")) //nolint:errcheck // reset cursor

	if _, ok := term.ColorOverride(DefaultFG); ok {
		t.Fatal("DefaultFG override still present after OSC 110 reset")
	}
	if _, ok := term.ColorOverride(DefaultBG); ok {
		t.Fatal("DefaultBG override still present after OSC 111 reset")
	}
	if _, ok := term.ColorOverride(DefaultCursor); ok {
		t.Fatal("DefaultCursor override still present after OSC 112 reset")
	}
}

// ---------------------------------------------------------------------------
// OSC 8 — hyperlinks land on glyphs and resolve back via Link()
// ---------------------------------------------------------------------------

func TestOSC8_HyperlinkOnGlyph(t *testing.T) {
	term := newTestTerm(40, 4)
	// Open hyperlink, write "foo", close, write " bar" (no link).
	term.Write([]byte("\x1b]8;;https://example.com/\x1b\\foo\x1b]8;;\x1b\\ bar")) //nolint:errcheck

	// "foo" cells should carry a non-zero Link ID resolving to the URL.
	for i, want := range []rune{'f', 'o', 'o'} {
		g := term.Cell(i, 0)
		if g.Char != want {
			t.Errorf("cell(%d,0).Char = %q, want %q", i, g.Char, want)
		}
		if g.Link == 0 {
			t.Errorf("cell(%d,0).Link = 0, want non-zero", i)
		}
		if got := term.Link(g.Link); got != "https://example.com/" {
			t.Errorf("Link(%d) = %q, want example.com URL", g.Link, got)
		}
	}
	// " bar" cells (col 3..6) must NOT carry a link.
	for i := 3; i < 7; i++ {
		if g := term.Cell(i, 0); g.Link != 0 {
			t.Errorf("cell(%d,0).Link = %d, want 0 (after close)", i, g.Link)
		}
	}
}

func TestOSC8_LinkInterning(t *testing.T) {
	// Same URL emitted twice should reuse a single ID.
	term := newTestTerm(40, 4)
	term.Write([]byte("\x1b]8;;u\x1b\\a\x1b]8;;\x1b\\b\x1b]8;;u\x1b\\c")) //nolint:errcheck
	id1 := term.Cell(0, 0).Link                                           // 'a' under url u
	id2 := term.Cell(2, 0).Link                                           // 'c' under url u (after close + re-open)
	if id1 == 0 {
		t.Fatal("first 'a' should have a link ID")
	}
	if id1 != id2 {
		t.Errorf("same URL produced different IDs: %d vs %d", id1, id2)
	}
}

// Reproduce the user's actual chunks from /tmp/bunk.log to find why PS1
// renders as a link. Stream is verbatim from production, including the OSCs
// from bash/vte between commands and the SGR-styled prompt.
func TestOSC8_RealUserChunks(t *testing.T) {
	term := newTestTerm(80, 24)
	// chunk 1+2: ls --hyperlink output (truncated for test)
	term.Write([]byte( //nolint:errcheck
		"\x1b]8;;file://ydell/home/jsn/workspace/bunk/AGENTS.MD\x1b\\AGENTS.MD\x1b]8;;\x1b\\      " +
			"\x1b]8;;file://ydell/home/jsn/workspace/bunk/go.mod\x1b\\go.mod\x1b]8;;\x1b\\\r\n" +
			"\x1b]8;;file://ydell/home/jsn/workspace/bunk/reflow_test.go\x1b\\reflow_test.go\x1b]8;;\x1b\\\r\n",
	))
	// chunks 42-50: OSCs that arrive between ls and PS1
	term.Write([]byte("\x1b]0;jsn@ydell:~/workspace/bunk\a"))                                                                                                                                                                                                        //nolint:errcheck
	term.Write([]byte("\x1b]3008;end=12fb10d4-9155-4970-8bb1-bd06ff65691f;exit=success\x1b\\"))                                                                                                                                                                      //nolint:errcheck
	term.Write([]byte("\x1b]3008;start=ed2246a1-e4f1-437b-a965-10f98e6428cd;machineid=2edfbf51e2e34d64a9096ebdb31f64a9;user=jsn;hostname=ydell;bootid=54980873-4669-47b6-9af5-b9462259d4ee;pid=00000000000000390800;type=shell;cwd=/home/jsn/workspace/bunk\x1b\\")) //nolint:errcheck
	term.Write([]byte("\x1b]666;vte.shell.postexec=0\x1b\\"))                                                                                                                                                                                                        //nolint:errcheck
	term.Write([]byte("\x1b]666;vte.shell.precmd!\x1b\\"))                                                                                                                                                                                                           //nolint:errcheck
	term.Write([]byte("\x1b]7;file://ydell/home/jsn/workspace/bunk\x1b\\"))                                                                                                                                                                                          //nolint:errcheck
	// chunk 51: PS1 with SGR-only styling
	term.Write([]byte("\x1b[?2004h\x1b[01;32mjsn@ydell\x1b[0m \x1b[01;34m~/workspace/bunk\x1b[0m [\x1b[0;33mmaster\x1b[0m \x1b[1;31m!\x1b[0m]\r\r\n$ ")) //nolint:errcheck

	// Find the row containing "$ " — should be the PS1 row.
	// Then verify NO cell on that row carries a Link.
	for row := 0; row < 24; row++ {
		// Build the row's text to identify it.
		var line string
		for col := 0; col < 80; col++ {
			c := term.Cell(col, row).Char
			if c == 0 {
				c = ' '
			}
			line += string(c)
		}
		if !contains(line, "jsn@ydell") && !contains(line, "$ ") {
			continue
		}
		// This row contains PS1 content. Inspect every cell.
		for col := 0; col < 80; col++ {
			g := term.Cell(col, row)
			if g.Link != 0 {
				t.Errorf("row %d col %d (%q) carries Link=%d (URL=%q) — PS1 row should be link-free", row, col, g.Char, g.Link, term.Link(g.Link))
			}
		}
	}
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Trailing cells after a hyperlink (cleared by clear-to-EOL or scroll while
// the link is open) must NOT carry the link ID. These are the "right of $ "
// cells visible on the prompt line.
func TestOSC8_NoLeakIntoTrailingClearedCells(t *testing.T) {
	term := newTestTerm(20, 4)
	// Open a link, write a few chars, then ESC[K (clear to EOL) WHILE the
	// link is still open — this is what bash's clear-to-eol after PS1 would
	// look like if the prompt itself is inside a link.
	term.Write([]byte("\x1b]8;;url\x1b\\foo\x1b[K\x1b]8;;\x1b\\")) //nolint:errcheck
	// Cells past col 3 must not carry the link.
	for col := 3; col < 20; col++ {
		if g := term.Cell(col, 0); g.Link != 0 {
			t.Fatalf("cleared cell at col %d carried Link=%d while link was open", col, g.Link)
		}
	}
}

// Regression for "PS1 highlighted as link": after a sequence of opens/closes
// (ls --hyperlink output), text printed AFTER the final close must not carry
// any hyperlink ID — even when sandwiched between other OSCs (title, CWD).
func TestOSC8_NoLeakIntoSubsequentText(t *testing.T) {
	term := newTestTerm(80, 4)
	stream := []byte("" +
		// Mini ls --hyperlink burst
		"\x1b]8;;file://a\x1b\\AAA\x1b]8;;\x1b\\ " +
		"\x1b]8;;file://b\x1b\\BBB\x1b]8;;\x1b\\\r\n" +
		// Bash post-exec OSCs (similar to what vte/bash emits between commands)
		"\x1b]0;title\a" +
		"\x1b]7;file:///tmp\x1b\\" +
		// New PS1 with SGR codes
		"\x1b[01;32mPS1text\x1b[0m" +
		"")
	term.Write(stream) //nolint:errcheck

	// Find the PS1text on row 1 and verify no link.
	for col := 0; col < 7; col++ {
		g := term.Cell(col, 1)
		if g.Char != rune("PS1text"[col]) {
			t.Fatalf("row 1 col %d expected %q, got %q", col, "PS1text"[col], g.Char)
		}
		if g.Link != 0 {
			t.Errorf("PS1text col %d has Link=%d (was %q), should be 0", col, g.Link, term.Link(g.Link))
		}
	}
}

func TestOSC8_URLContainingSemicolon(t *testing.T) {
	// Pathological: URL with embedded ';' must not be truncated by the
	// strings.Split in handleSTR — handleSTR re-joins args[2:] with ';'.
	term := newTestTerm(40, 4)
	term.Write([]byte("\x1b]8;;https://x.example/?a=1;b=2\x1b\\z")) //nolint:errcheck
	g := term.Cell(0, 0)
	if got := term.Link(g.Link); got != "https://x.example/?a=1;b=2" {
		t.Errorf("URL with ';' was mangled: got %q", got)
	}
}
