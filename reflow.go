// reflow.go - terminal content reflow on pane resize and scrollback rebuild.
//
// On any pane resize (split or close), the full terminal history — scrollback
// ring plus the current visible vt10x grid — is collected, the scrollback is
// rebuilt, and the portion that fits in the new terminal is re-injected so
// content reflows naturally at the new column width.
//
// The same rawBuf replay is also used when the user first enters scrollback
// mode (Shift+PgUp).  detectShift can only record rows that were visible
// *before* each PTY chunk arrived; a single large TCP burst (common over SSH)
// can scroll through many screenfuls in one read, silently dropping every
// intermediate line.  Replaying rawBuf into a tall scratch terminal captures
// all of them in one shot.
//
// The key insight: each captured row is a []vt10x.Glyph whose length is the
// column count at capture time.  When a row is wider than the new terminal,
// vt10x auto-wraps it; when narrower, it fits as a short line.  No additional
// bookkeeping is needed.
//
// Limitations:
//   - Rows in the scrollback and visible grid represent physical (already
//     wrapped) lines, not logical lines.  A line that was split across two
//     rows by auto-wrap at the OLD width will not be re-joined before being
//     re-wrapped at the new width.  This is the same behaviour as tmux/screen.
//   - The scrollback is rebuilt from the combined history on a column-width
//     resize.  The user's scroll position is preserved via content-anchor
//     matching (centre row fingerprint) with proportional fallback.
package main

import (
	"bytes"
	"context"
	"strconv"

	"bunker/internal/vt10x"
)

// stripAltScreen removes alt-screen content from buf, keeping pre-entry and
// post-exit bytes.  This prevents the scratch terminal from allocating a huge
// alt-screen buffer during replay, and ensures pre-vim shell history is
// preserved across resize (the previous approach of discarding everything
// before the last exit sequence lost all pre-vim content).
func stripAltScreen(buf []byte) []byte {
	entrySeqs := [][]byte{
		[]byte("\x1b[?1049h"),
		[]byte("\x1b[?1047h"),
		[]byte("\x1b[?47h"),
	}
	exitSeqs := [][]byte{
		[]byte("\x1b[?1049l"),
		[]byte("\x1b[?1047l"),
		[]byte("\x1b[?47l"),
	}

	var result []byte
	pos := 0
	found := false
	for pos < len(buf) {
		entryPos := -1
		for _, seq := range entrySeqs {
			if p := bytes.Index(buf[pos:], seq); p >= 0 {
				absPos := pos + p
				if entryPos < 0 || absPos < entryPos {
					entryPos = absPos
				}
			}
		}
		if entryPos < 0 {
			if found {
				result = append(result, buf[pos:]...)
			}
			break
		}
		found = true
		result = append(result, buf[pos:entryPos]...)

		exitEnd := -1
		for _, seq := range exitSeqs {
			if p := bytes.Index(buf[entryPos:], seq); p >= 0 {
				absEnd := entryPos + p + len(seq)
				if exitEnd < 0 || absEnd < exitEnd {
					exitEnd = absEnd
				}
			}
		}
		if exitEnd < 0 {
			break // entry with no exit — discard from entry onward
		}
		pos = exitEnd
	}
	if !found {
		return buf
	}
	return result
}

// rowContentEnd returns the index one past the last non-blank cell in row.
// Only cells with an actual visible character (non-NUL, non-space) are
// considered content.  Trailing spaces are ignored regardless of their
// background colour — shells commonly use \x1b[K (erase-to-EOL) to fill
// the rest of a prompt line with a coloured background; we must not replay
// those filled cells or they tint the entire pane.
func rowContentEnd(row []vt10x.Glyph) int {
	end := len(row)
	for end > 0 {
		g := row[end-1]
		if g.Width < 0 || (g.Char != 0 && g.Char != ' ') {
			break
		}
		end--
	}
	return end
}

// rowChars extracts the visible rune sequence from a Glyph row (up to the
// last non-blank character).  Interior spaces are included — they are
// meaningful content that distinguishes e.g. "Mar 23" from "Mar23".
// Trailing blank cells (NUL or space) are excluded via rowContentEnd.
func rowChars(row []vt10x.Glyph) []rune {
	end := rowContentEnd(row)
	if end == 0 {
		return nil
	}
	chars := make([]rune, 0, end)
	for i := 0; i < end; i++ {
		if c := row[i].Char; c != 0 { // include spaces, exclude only unset cells
			chars = append(chars, c)
			chars = append(chars, []rune(row[i].Combining())...)
		}
	}
	return chars
}

// rowCharsMatch reports whether a and b share a common prefix of at least
// minLen runes.  This handles both wrap directions: after narrowing, the
// original line is split so the captured anchor is a prefix of the new row;
// after widening, the new row contains the full original line so the anchor
// matches its beginning.
func rowCharsMatch(a, b []rune, minLen int) bool {
	n := min(len(a), len(b))
	if n < minLen {
		return false
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// reflowSbOff computes the new sbOff value to use after a column-width resize.
//
// It tries to keep the same content line at the centre of the viewport:
//  1. If anchorChars has ≥ anchorMinLen runes, scan the scrollback portion of
//     the scratch terminal for a row whose character content matches (prefix
//     match handles wrap in both directions).  When multiple rows match, prefer
//     the one closest to the proportional estimate.  Set sbOff so that row
//     appears at newRows/2 from the top of the viewport.
//  2. Fall back to proportional mapping of the top-of-viewport row when the
//     anchor is missing, too short, or not found in the scratch terminal.
func reflowSbOff(
	oldSbOff, oldSbCount, oldRows int,
	newSbCount, newRows int,
	anchorChars []rune, anchorOldRingIdx, anchorMinLen int,
	scratch vt10x.Terminal, newCols int,
) int {
	if oldSbOff == 0 {
		return 0
	}

	// Content-match path.
	if len(anchorChars) >= anchorMinLen {
		// Proportional estimate within the ring only (anchor is always a ring
		// row, so ring-only fractions give a more accurate tiebreaker than
		// including the live terminal rows in the denominator).
		propEstRow := 0
		if oldSbCount > 0 && anchorOldRingIdx >= 0 {
			propEstRow = anchorOldRingIdx * newSbCount / oldSbCount
		}

		bestRow := -1
		bestDist := newSbCount + 1
		rowBuf := make([]vt10x.Glyph, newCols)
		// Only search the scrollback portion of the scratch terminal.
		// If the anchor content ended up in the live terminal region
		// (rows newSbCount..contentRows-1), sbOff=0 is correct — and
		// searching that region could incorrectly win over the scrollback
		// match if it happens to be closer to propEstRow.
		for r := 0; r < newSbCount; r++ {
			for c := 0; c < newCols; c++ {
				rowBuf[c] = scratch.Cell(c, r)
			}
			if rowCharsMatch(anchorChars, rowChars(rowBuf), anchorMinLen) {
				d := r - propEstRow
				if d < 0 {
					d = -d
				}
				if d < bestDist {
					bestDist = d
					bestRow = r
				}
			}
		}

		if bestRow >= 0 {
			sbOff := newSbCount - bestRow + newRows/2
			if sbOff > newSbCount {
				sbOff = newSbCount
			}
			return sbOff
		}
	}

	// Proportional fallback: scale the top-of-viewport row index by the
	// ratio of total content rows (old vs new).
	oldTotal := oldSbCount + oldRows
	newTotal := newSbCount + newRows
	oldTopRow := oldSbCount - oldSbOff
	if oldTopRow < 0 {
		oldTopRow = 0
	}
	newTopRow := 0
	if oldTotal > 0 {
		newTopRow = oldTopRow * newTotal / oldTotal
	}
	if newTopRow < newSbCount {
		return newSbCount - newTopRow
	}
	return 0
}

// rendered in a terminal that is cols columns wide.
func rowVisualHeight(row []vt10x.Glyph, cols int) int {
	end := rowContentEnd(row)
	if end == 0 || cols <= 0 {
		return 1
	}
	return (end + cols - 1) / cols
}

// reflowInject writes rows into term (already resized) as ANSI-coded text.
// Each row ends with \r\n except the last content row; long rows auto-wrap at
// the new terminal width.  Trailing blank rows are skipped so the cursor lands
// right after the last visible content, not at the bottom of the terminal.
// Must be called with p.mu held.
func reflowInject(term vt10x.Terminal, rows [][]vt10x.Glyph) {
	if len(rows) == 0 {
		return
	}

	// Find the last row that has any visible content so we don't emit
	// trailing \r\n sequences that would push the cursor to the bottom.
	lastContent := -1
	for r := len(rows) - 1; r >= 0; r-- {
		if rowContentEnd(rows[r]) > 0 {
			lastContent = r
			break
		}
	}
	if lastContent < 0 {
		return // nothing to inject
	}

	var buf bytes.Buffer
	buf.WriteString("\x1b[0m\x1b[2J\x1b[H") // reset attrs, clear, cursor home

	prevFG, prevBG := vt10x.DefaultFG, vt10x.DefaultBG
	var prevMode int16

	for r := 0; r <= lastContent; r++ {
		row := rows[r]
		end := rowContentEnd(row)
		for c := 0; c < end; c++ {
			g := row[c]
			if g.Width < 0 {
				continue
			}
			if g.FG != prevFG || g.BG != prevBG || g.Mode != prevMode {
				emitSGR(&buf, g)
				prevFG, prevBG, prevMode = g.FG, g.BG, g.Mode
			}
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			buf.WriteRune(ch)
			buf.WriteString(g.Combining())
		}
		if r < lastContent {
			buf.WriteString("\x1b[0m\r\n")
			prevFG, prevBG, prevMode = vt10x.DefaultFG, vt10x.DefaultBG, 0
		}
	}
	buf.WriteString("\x1b[0m")
	if _, err := term.Write(buf.Bytes()); err != nil {
		L.Log(context.Background(), LevelTrace, "reflow: write rows", "err", err)
	}
}

// emitSGR writes a complete SGR escape sequence for the given glyph's
// attributes and colours into buf.  Always starts with \x1b[0 (full reset).
func emitSGR(buf *bytes.Buffer, g vt10x.Glyph) {
	buf.WriteString("\x1b[0")
	if g.Mode&vtAttrBold != 0 {
		buf.WriteString(";1")
	}
	if g.Mode&vtAttrDim != 0 {
		buf.WriteString(";2")
	}
	if g.Mode&vtAttrItalic != 0 {
		buf.WriteString(";3")
	}
	if g.Mode&vtAttrUnderline != 0 {
		switch (g.Mode & vtAttrUnderlineStyleMask) / vtAttrUnderlineStyleBit0 {
		case 1:
			buf.WriteString(";4:2") // double
		case 2:
			buf.WriteString(";4:3") // curly
		case 3:
			buf.WriteString(";4:4") // dotted
		case 4:
			buf.WriteString(";4:5") // dashed
		default:
			buf.WriteString(";4") // solid
		}
	}
	if g.Mode&vtAttrBlink != 0 {
		buf.WriteString(";5")
	}
	if g.Mode&vtAttrReverse != 0 {
		buf.WriteString(";7")
	}
	if g.Mode&vtAttrInvisible != 0 {
		buf.WriteString(";8")
	}
	if g.Mode&vtAttrStrikethrough != 0 {
		buf.WriteString(";9")
	}
	if g.Mode&vtAttrOverline != 0 {
		buf.WriteString(";53")
	}
	emitColorCode(buf, g.FG, true)
	emitColorCode(buf, g.BG, false)
	if g.Mode&vtAttrHasULColor != 0 {
		emitULColorCode(buf, g.UL)
	}
	buf.WriteByte('m')
}

// emitColorCode appends SGR colour sub-parameters for c.
// Default colours are skipped (the leading \x1b[0 already resets them).
func emitColorCode(buf *bytes.Buffer, c vt10x.Color, isFG bool) {
	if c >= vt10x.DefaultFG {
		return
	}
	switch {
	case c < 8:
		buf.WriteByte(';')
		if isFG {
			buf.Write(strconv.AppendInt(nil, int64(30+c), 10))
		} else {
			buf.Write(strconv.AppendInt(nil, int64(40+c), 10))
		}
	case c < 16:
		buf.WriteByte(';')
		if isFG {
			buf.Write(strconv.AppendInt(nil, int64(90+c-8), 10))
		} else {
			buf.Write(strconv.AppendInt(nil, int64(100+c-8), 10))
		}
	case c < 256:
		if isFG {
			buf.WriteString(";38;5;")
		} else {
			buf.WriteString(";48;5;")
		}
		buf.Write(strconv.AppendInt(nil, int64(c), 10))
	default: // truecolor: r<<16|g<<8|b
		r := (c >> 16) & 0xff
		g := (c >> 8) & 0xff
		b := c & 0xff
		if isFG {
			buf.WriteString(";38;2;")
		} else {
			buf.WriteString(";48;2;")
		}
		buf.Write(strconv.AppendInt(nil, int64(r), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(g), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(b), 10))
	}
}

// emitULColorCode appends SGR 58 underline-colour sub-parameters for c.
func emitULColorCode(buf *bytes.Buffer, c vt10x.Color) {
	switch {
	case c < 256:
		buf.WriteString(";58;5;")
		buf.Write(strconv.AppendInt(nil, int64(c), 10))
	default: // truecolor
		r := (c >> 16) & 0xff
		g := (c >> 8) & 0xff
		b := c & 0xff
		buf.WriteString(";58;2;")
		buf.Write(strconv.AppendInt(nil, int64(r), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(g), 10))
		buf.WriteByte(';')
		buf.Write(strconv.AppendInt(nil, int64(b), 10))
	}
}
