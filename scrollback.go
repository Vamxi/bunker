// scrollback.go - per-pane scrollback buffer.
//
// Architecture
// ─────────────
// Scrollback capture uses a native vt10x scroll callback
// (WithScrollSwapCallback). vt10x calls Pane.onScrollSwap() synchronously
// inside scrollUp() for each row that leaves the top of the primary screen,
// before that row is cleared; the ring takes the row and returns its evicted
// slot (sbRing.swapIn), so rows are never copied.
//
// Alternate screen (vim, htop, less in fullscreen) sets vt10x.ModeAltScreen.
// onScrollSwap checks this flag and ignores callbacks while it is set — alt-
// screen apps use absolute positioning and their scrolls must not appear in
// primary scrollback.
//
// rawBuf (in pane.go) retains the raw PTY byte stream for resize/reflow:
// when the terminal is resized, the entire raw history is replayed through a
// scratch terminal at the new width so that line wrapping is recalculated.
// This is separate from scrollback capture and is not affected by this change.
//
// Scrollback ring buffer
// ──────────────────────
// sbRing is a fixed-capacity circular buffer of captured lines.  Each entry
// owns its backing []vt10x.Glyph storage (allocated or reused by push).  When
// the ring is full the oldest line is evicted and its slot is reused.
//
// User navigation (Shift+PgUp / Shift+PgDn):
//
//	sbOff == 0     → live view (normal)
//	sbOff == N     → display starting N lines above the live view
//	Any non-scroll key → snap back to live view automatically
package main

import "bunker/internal/vt10x"

// sbRing is a fixed-capacity circular buffer of captured Glyph rows.
// maxLines must be set before the first push (typically from the config).
type sbRing struct {
	maxLines int             // ring capacity (from config scrollback setting)
	lines    [][]vt10x.Glyph // allocated on first push, length = maxLines
	used     []int           // per slot: cells from here on are blank (see vt10x.ScrollSwapFunc)
	head     int             // index of the oldest entry
	count    int             // number of valid entries (0 … maxLines)
}

func (s *sbRing) alloc() {
	if s.lines == nil {
		s.lines = make([][]vt10x.Glyph, s.maxLines)
		s.used = make([]int, s.maxLines)
	}
}

// push appends one captured row to the ring.  When the ring is full, the
// oldest entry is evicted.  The ring owns its backing storage: each slot's
// []vt10x.Glyph is reused when the incoming row fits (same or narrower width),
// or reallocated when the row is wider.  The incoming row is always copied —
// the ring never retains a reference into the caller's slice (e.g. a
// captureGrid slab), so the caller's allocation can be freed immediately.
func (s *sbRing) push(row []vt10x.Glyph) {
	if s.maxLines <= 0 {
		return
	}
	s.alloc()

	// Determine the destination slot.
	var slot int
	if s.count < s.maxLines {
		slot = (s.head + s.count) % s.maxLines
		s.count++
	} else {
		// Ring is full: overwrite oldest slot (head), advance head.
		slot = s.head
		s.head = (s.head + 1) % s.maxLines
	}

	// Reuse the slot's existing backing array when capacity allows;
	// allocate a new one only when the row is wider than the current slot.
	if cap(s.lines[slot]) >= len(row) {
		s.lines[slot] = s.lines[slot][:len(row)]
	} else {
		s.lines[slot] = make([]vt10x.Glyph, len(row))
	}
	copy(s.lines[slot], row)
	s.used[slot] = vt10x.UsedCells(row)
}

// swapIn stores row, whose cells from used on are blank, in the ring without
// copying, taking ownership of the slice. It returns a same-length buffer
// the caller may overwrite, with its used count: the evicted oldest slot
// when the ring is full, otherwise a fresh allocation. Returns nil (row not
// captured, caller keeps it) when scrollback is off.
func (s *sbRing) swapIn(row []vt10x.Glyph, used int) ([]vt10x.Glyph, int) {
	if s.maxLines <= 0 {
		return nil, 0
	}
	s.alloc()
	var slot int
	if s.count < s.maxLines {
		slot = (s.head + s.count) % s.maxLines
		s.count++
	} else {
		slot = s.head
		s.head = (s.head + 1) % s.maxLines
	}
	old, oldUsed := s.lines[slot], s.used[slot]
	s.lines[slot], s.used[slot] = row, used
	switch {
	case cap(old) < len(row):
		return make([]vt10x.Glyph, len(row)), len(row)
	case len(old) < len(row):
		// Cells past the old row's end are stale; the caller clears them all.
		return old[:len(row)], len(row)
	}
	return old[:len(row)], min(oldUsed, len(row))
}

// clear empties the ring in place.  Backing row storage is kept so
// subsequent pushes can reuse it without reallocating.
func (s *sbRing) clear() {
	s.head = 0
	s.count = 0
}

// get returns the line at logical index i, where 0 is the OLDEST surviving
// line and count-1 is the most recently pushed line.  Returns nil on bounds
// violation.
func (s *sbRing) get(i int) []vt10x.Glyph {
	if i < 0 || i >= s.count {
		return nil
	}
	return s.lines[(s.head+i)%s.maxLines]
}

// ---------------------------------------------------------------------------
// Scrollback capture helpers (called from pane.go readPTY, under Pane.mu)
// ---------------------------------------------------------------------------

// captureRow allocates a fresh []vt10x.Glyph slice and copies the current
// vt10x row r into it.  Must be called with Pane.mu held.
func captureRow(term vt10x.Terminal, r, cols int) []vt10x.Glyph {
	row := make([]vt10x.Glyph, cols)
	for c := 0; c < cols; c++ {
		row[c] = term.RawCell(c, r)
	}
	return row
}
