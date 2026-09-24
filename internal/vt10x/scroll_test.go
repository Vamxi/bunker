package vt10x

// TDD tests for the native scroll callback added to vt10x.
//
// The callback fires synchronously inside scrollUp(), before the departing
// rows are cleared, for every row that leaves the top of the primary screen
// (orig == 0).  It does NOT fire for:
//   - In-place cursor-up + overwrite (no scroll operation)
//   - Scroll regions whose top is not row 0 (orig != 0)
//
// The caller (bunk) is responsible for copying the row slice if it needs to
// retain content after the callback returns; the slice is owned by the
// terminal and will be cleared immediately after the callback.

import "testing"

// newTestTermWithCb builds a terminal with a scroll callback installed. The
// swap callback returns nil, so the terminal keeps (and clears) its row and
// cb sees exactly what a copying consumer would.
func newTestTermWithCb(cols, rows int, cb func(row []Glyph)) *terminal {
	swap := func(row []Glyph) []Glyph { cb(row); return nil }
	return newTerminal(TerminalInfo{cols: cols, rows: rows, scrollSwapCb: swap})
}

// collectRow copies a callback row and appends it to a slice.
// The callback row slice is only valid for the duration of the call, so a
// copy is required for any test that inspects content after the write.
func collectRow(dst *[][]Glyph) func([]Glyph) {
	return func(row []Glyph) {
		cp := make([]Glyph, len(row))
		copy(cp, row)
		*dst = append(*dst, cp)
	}
}

// ---------------------------------------------------------------------------
// Normal newline-driven scroll
// ---------------------------------------------------------------------------

// TestScrollCallback_NormalScroll verifies that a single newline past the
// bottom of a full-screen terminal fires the callback once with the content
// of the row that scrolled off the top.
func TestScrollCallback_NormalScroll(t *testing.T) {
	const cols, rows = 5, 3
	var got [][]Glyph
	term := newTestTermWithCb(cols, rows, collectRow(&got))

	// Fill all three rows and trigger one scroll with a fourth newline.
	// \r\n used so cursor returns to column 0 on each line.
	term.Write([]byte("AAAAA\r\nBBBBB\r\nCCCCC\r\n")) //nolint:errcheck

	if len(got) != 1 {
		t.Fatalf("callback fired %d times, want 1", len(got))
	}
	if len(got[0]) != cols {
		t.Fatalf("captured row len = %d, want %d", len(got[0]), cols)
	}
	for i, g := range got[0] {
		if g.Char != 'A' {
			t.Errorf("captured[0][%d].Char = %q, want 'A'", i, g.Char)
		}
	}
}

// TestScrollCallback_MultiRowCSIS verifies that CSI S n fires the callback
// once per departing row, in top-to-bottom order.
func TestScrollCallback_MultiRowCSIS(t *testing.T) {
	const cols, rows = 4, 5
	var got [][]Glyph
	term := newTestTermWithCb(cols, rows, collectRow(&got))

	// Write 5 rows: row 0 = "AAAA", row 1 = "BBBB", ..., row 4 = "EEEE".
	for i, ch := range []byte("ABCDE") {
		for c := 0; c < cols; c++ {
			term.Write([]byte{ch}) //nolint:errcheck
		}
		if i < rows-1 {
			term.Write([]byte("\r\n")) //nolint:errcheck
		}
	}
	// Confirm no scroll yet.
	if len(got) != 0 {
		t.Fatalf("expected 0 callbacks after filling without overflow, got %d", len(got))
	}

	// CSI 3 S — scroll up 3 lines: rows 0, 1, 2 leave the screen.
	term.Write([]byte("\x1b[3S")) //nolint:errcheck

	if len(got) != 3 {
		t.Fatalf("CSI 3S: callback fired %d times, want 3", len(got))
	}
	wantChars := []byte("ABC")
	for i, want := range wantChars {
		if got[i][0].Char != rune(want) {
			t.Errorf("row %d: first char = %q, want %q", i, got[i][0].Char, rune(want))
		}
	}
}

// ---------------------------------------------------------------------------
// In-place rewrite must NOT trigger callback
// ---------------------------------------------------------------------------

// TestScrollCallback_InPlaceRewriteNoCallback verifies that cursor-up +
// overwrite (the progress-bar pattern) does not fire the callback.
func TestScrollCallback_InPlaceRewriteNoCallback(t *testing.T) {
	const cols, rows = 10, 5
	fired := 0
	term := newTestTermWithCb(cols, rows, func(_ []Glyph) { fired++ })

	// Write two lines (no overflow).
	term.Write([]byte("AAAAAAAAAA\r\nBBBBBBBBBB")) //nolint:errcheck

	// Move cursor up one line and overwrite — no scroll.
	term.Write([]byte("\x1b[1A")) //nolint:errcheck // cursor up
	for c := 0; c < cols; c++ {
		term.Write([]byte("X")) //nolint:errcheck
	}

	if fired != 0 {
		t.Errorf("in-place rewrite fired callback %d times, want 0", fired)
	}
}

// ---------------------------------------------------------------------------
// Partial scroll region not anchored at top
// ---------------------------------------------------------------------------

// TestScrollCallback_PartialScrollRegionNoCallback verifies that a scroll
// region whose top row is not row 0 does not fire the callback.
// When orig > 0, rows leave an internal region but do not reach the top of
// the visible screen.
func TestScrollCallback_PartialScrollRegionNoCallback(t *testing.T) {
	const cols, rows = 8, 6
	fired := 0
	term := newTestTermWithCb(cols, rows, func(_ []Glyph) { fired++ })

	// Set scroll region to rows 2–5 (1-indexed), i.e., 0-indexed rows 1–4.
	// t.top will be 1, not 0.
	term.Write([]byte("\x1b[2;5r")) //nolint:errcheck

	// Move cursor to bottom of scroll region and trigger several scrolls.
	term.Write([]byte("\x1b[5;1H")) //nolint:errcheck // row 5, col 1
	for i := 0; i < 8; i++ {
		term.Write([]byte("X\r\n")) //nolint:errcheck
	}

	if fired != 0 {
		t.Errorf("partial scroll region fired callback %d times, want 0", fired)
	}
}

// ---------------------------------------------------------------------------
// Callback fires with pre-clear content
// ---------------------------------------------------------------------------

// TestScrollCallback_ContentBeforeClear verifies that the callback receives
// the row content BEFORE scrollUp clears it.  After the write the content has
// been replaced with spaces in the live grid, but the captured snapshot
// (taken inside the callback) must still reflect the original content.
func TestScrollCallback_ContentBeforeClear(t *testing.T) {
	const cols, rows = 6, 2
	var got [][]Glyph
	term := newTestTermWithCb(cols, rows, collectRow(&got))

	// Fill two rows and trigger one scroll.
	term.Write([]byte("ZZZZZZ\r\nYYYYYY\r\n")) //nolint:errcheck

	if len(got) != 1 {
		t.Fatalf("expected 1 captured row, got %d", len(got))
	}
	// Captured row must be "ZZZZZZ" (the pre-clear content of row 0).
	for i, g := range got[0] {
		if g.Char != 'Z' {
			t.Errorf("captured[0][%d].Char = %q, want 'Z'", i, g.Char)
		}
	}
	// The live grid row 0 should now contain the second row's content (YYYYYY).
	for x := 0; x < cols; x++ {
		if g := term.Cell(x, 0); g.Char != 'Y' {
			t.Errorf("live cell(%d,0).Char = %q after scroll, want 'Y'", x, g.Char)
		}
	}
}
