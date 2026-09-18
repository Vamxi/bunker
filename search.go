// search.go - incremental in-pane text search (Ctrl+F).
//
// Usage:
//
//	Ctrl+F          → enter search mode on the active pane
//	Type characters → query grows; matches highlight live (amber = all, orange = current)
//	Enter / Ctrl+N  → jump to next match
//	Ctrl+P          → jump to previous match
//	Backspace       → delete last character from query
//	Ctrl+C          → copy the active pane's mouse selection to the clipboard
//	Ctrl+V          → paste clipboard text into the search query (controls/newlines stripped)
//	Escape          → exit search mode, clear all highlights
//
// The search is case-insensitive and covers both the scrollback ring and the
// live terminal grid (the full virtual row space).
//
// Thread safety: App.search* fields are protected by app.mu.  Pane.searchHL is
// protected by pane.mu.  updateSearch acquires each lock independently (never
// both at once) to avoid deadlocks.
//
// Async scan: updateSearch signals a single background worker.  The worker
// snapshots the virtual rows to [][]rune under p.mu (fast memcpy), then scans
// outside the lock.  The wake channel is size 1, so rapid typing coalesces to
// "scan the latest query once" instead of spawning one goroutine per keypress.
// A searchGen counter still guards against stale publishes.
package main

import (
	"strings"
	"time"
	"unicode"

	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// searchMatch identifies one occurrence of the search query inside a pane's
// virtual grid (scrollback ring + live terminal rows).
type searchMatch struct {
	vRow, col, length int
}

// searchSpan is a highlighted column range within one virtual row [col, end).
type searchSpan struct{ col, end int }

// searchHighlight holds span-based match positions for renderPane.
// regular contains all matches; current contains only the selected match.
// Both map virtual row → spans for that row.
type searchHighlight struct {
	regular map[int][]searchSpan
	current map[int][]searchSpan
}

// buildSearchHighlight maps match positions to per-row spans for renderPane.
// idx is the currently selected match; it is highlighted via current spans.
func buildSearchHighlight(matches []searchMatch, idx int) *searchHighlight {
	if len(matches) == 0 {
		return nil
	}
	hl := &searchHighlight{
		regular: make(map[int][]searchSpan),
		current: make(map[int][]searchSpan),
	}
	for i, m := range matches {
		span := searchSpan{col: m.col, end: m.col + m.length}
		if i == idx {
			hl.current[m.vRow] = append(hl.current[m.vRow], span)
		} else {
			hl.regular[m.vRow] = append(hl.regular[m.vRow], span)
		}
	}
	return hl
}

func (app *App) ensureSearchWorker() {
	app.searchOnce.Do(func() {
		if app.searchWake == nil {
			app.searchWake = make(chan struct{}, 1)
		}
		go app.searchWorker()
	})
}

func (app *App) searchWorker() {
	for {
		select {
		case <-app.done:
			return
		case <-app.searchWake:
			app.runSearchScan()
			for {
				select {
				case <-app.searchWake:
					app.runSearchScan()
				default:
					goto next
				}
			}
		}
	next:
	}
}

func (app *App) runSearchScan() {
	// Snapshot search state under app.mu.
	app.mu.Lock()
	p := app.searchPane
	query := app.searchQuery
	idx := app.searchIdx
	gen := app.searchGen
	app.mu.Unlock()

	if p == nil || query == "" {
		return
	}

	lq := strings.ToLower(query)
	lqRunes := []rune(lq)
	lqLen := len(lqRunes)

	// Snapshot the virtual grid to [][]rune under p.mu.
	// This is a pure memcpy — no pattern matching yet — so lock hold time is
	// O(sbCount × cols) iterations of simple ToLower calls.
	p.mu.Lock()
	cols, rows := p.term.Size()
	sbCount := p.sb.count
	totalRows := sbCount + rows
	snapshot := make([][]rune, totalRows)
	columns := make([][]int, totalRows)
	ends := make([][]int, totalRows)
	for vRow := 0; vRow < totalRows; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else {
			cells = captureRow(p.term, vRow-sbCount, cols)
		}
		row := make([]rune, 0, cols)
		positions := make([]int, 0, cols+1)
		endColumns := make([]int, 0, cols)
		for i := 0; i < cols; i++ {
			var ch rune
			if i < len(cells) {
				if cells[i].Width < 0 {
					continue
				}
				ch = cells[i].Char
			}
			if ch == 0 {
				ch = ' '
			}
			row = append(row, unicode.ToLower(ch))
			positions = append(positions, i)
			width := 1
			if i < len(cells) {
				width = max(1, int(cells[i].Width))
			}
			endColumns = append(endColumns, i+width)
			if i < len(cells) {
				for _, c := range cells[i].Combining {
					row = append(row, unicode.ToLower(c))
					positions = append(positions, i)
					endColumns = append(endColumns, i+width)
				}
			}
		}
		snapshot[vRow] = row
		columns[vRow] = append(positions, cols)
		ends[vRow] = endColumns
	}
	p.mu.Unlock()

	var matches []searchMatch
	for vRow, row := range snapshot {
		for offset := 0; offset+lqLen <= len(row); offset++ {
			match := true
			for k := 0; k < lqLen; k++ {
				if row[offset+k] != lqRunes[k] {
					match = false
					break
				}
			}
			if match {
				matches = append(matches, searchMatch{
					vRow:   vRow,
					col:    columns[vRow][offset],
					length: ends[vRow][offset+lqLen-1] - columns[vRow][offset],
				})
				offset += lqLen - 1 // skip to end of match (loop adds 1)
			}
		}
	}

	clampedIdx := idx
	if clampedIdx >= len(matches) {
		clampedIdx = 0
	}
	hl := buildSearchHighlight(matches, clampedIdx)

	app.mu.Lock()
	if app.searchGen != gen {
		app.mu.Unlock()
		return
	}
	app.searchMatches = matches
	app.searchIdx = clampedIdx
	app.mu.Unlock()

	p.mu.Lock()
	p.searchHL = hl
	p.searchHLGen++
	p.mu.Unlock()

	L.Debug("search: scan done", "query", query, "matches", len(matches), "idx", clampedIdx, "gen", gen)
	app.triggerRedraw()
}

// enterSearch activates search mode for the currently active pane.
func (app *App) enterSearch() {
	app.mu.Lock()
	p := app.active
	if p == nil {
		app.mu.Unlock()
		return
	}
	L.Debug("search: entering search mode", "pane", p.id)
	app.searchMode = true
	app.searchQuery = ""
	app.searchPane = p
	app.searchMatches = nil
	app.searchIdx = 0
	app.searchGen++
	app.mu.Unlock()

	p.mu.Lock()
	p.searchHL = nil
	p.searchHLGen++
	p.mu.Unlock()

	app.triggerRedraw()
}

// exitSearch deactivates search mode and removes all highlights.
func (app *App) exitSearch() {
	app.mu.Lock()
	p := app.searchPane
	L.Debug("search: exiting search mode")
	app.searchMode = false
	app.searchQuery = ""
	app.searchPane = nil
	app.searchMatches = nil
	app.searchIdx = 0
	app.searchGen++ // invalidate any in-flight scan goroutine
	app.mu.Unlock()

	if p != nil {
		p.mu.Lock()
		p.searchHL = nil
		p.searchHLGen++
		p.mu.Unlock()
	}
	app.triggerRedraw()
}

// updateSearch marks the current search state dirty and wakes the background
// worker.  Empty queries are still cleared synchronously so the highlight
// disappears immediately when the user backspaces to nothing.
func (app *App) updateSearch() {
	app.mu.Lock()
	app.searchGen++
	p := app.searchPane
	query := app.searchQuery
	app.mu.Unlock()

	if p == nil {
		return
	}
	if query == "" {
		p.mu.Lock()
		p.searchHL = nil
		p.searchHLGen++
		p.mu.Unlock()
		app.mu.Lock()
		app.searchMatches = nil
		app.searchIdx = 0
		app.mu.Unlock()
		app.triggerRedraw()
		return
	}

	app.ensureSearchWorker()
	select {
	case app.searchWake <- struct{}{}:
	default:
	}
}

// searchNavigate moves to the next (delta=+1) or previous (delta=-1) match,
// scrolls the pane so the match is visible, and rebuilds highlights.
func (app *App) searchNavigate(delta int) {
	app.mu.Lock()
	matches := app.searchMatches
	if len(matches) == 0 {
		app.mu.Unlock()
		return
	}
	app.searchIdx = (app.searchIdx + delta + len(matches)) % len(matches)
	app.searchGen++ // invalidate any in-flight scan for the previously selected match
	idx := app.searchIdx
	m := matches[idx]
	p := app.searchPane
	app.mu.Unlock()

	L.Debug("search: navigate", "delta", delta, "idx", idx, "total", len(matches), "vrow", m.vRow)

	if p == nil {
		return
	}

	// Scroll so the match is centred vertically in the pane.
	hl := buildSearchHighlight(matches, idx)
	p.mu.Lock()
	sbCount := p.sb.count
	rows := p.h
	targetOff := sbCount - m.vRow + rows/2
	if targetOff < 0 {
		targetOff = 0
	}
	if targetOff > sbCount {
		targetOff = sbCount
	}
	p.sbOff = targetOff
	p.searchHL = hl
	p.searchHLGen++
	p.mu.Unlock()
	app.triggerRedraw()
}

// handleSearchKey processes a key event while search mode is active.
// It returns true to continue the event loop (search mode never triggers shutdown).
func (app *App) handleSearchKey(ev *tcell.EventKey) bool {
	kb := &app.keys
	switch {
	case kb.SearchExit.Matches(ev):
		app.exitSearch()

	case ev.Key() == tcell.KeyEnter, kb.SearchNext.Matches(ev):
		app.searchNavigate(+1)

	case kb.SearchPrev.Matches(ev):
		app.searchNavigate(-1)

	case kb.Copy.Matches(ev):
		app.copySelectionToClipboard()

	case kb.Paste.Matches(ev):
		app.pasteIntoSearchQuery()

	case ev.Key() == tcell.KeyBackspace || ev.Key() == tcell.KeyBackspace2:
		app.mu.Lock()
		q := app.searchQuery
		app.mu.Unlock()
		if len(q) > 0 {
			runes := []rune(q)
			app.mu.Lock()
			app.searchQuery = string(runes[:len(runes)-1])
			app.mu.Unlock()
			app.updateSearch()
		}

	case ev.Key() == tcell.KeyRune:
		app.mu.Lock()
		app.searchQuery += string(ev.Rune())
		app.mu.Unlock()
		app.updateSearch()
	}
	return true
}

// copySelectionToClipboard copies the active pane's mouse selection (if any)
// to the system clipboard.  Mirrors handleKey's Copy branch so search mode
// supports the same select-and-copy round-trip as normal mode — without it,
// users can't move text between a pane and the search bar.
func (app *App) copySelectionToClipboard() {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active == nil {
		return
	}
	active.mu.Lock()
	text := active.selText()
	if text != "" {
		active.selActive = false
	}
	active.mu.Unlock()
	if text == "" {
		return
	}
	app.copyToClipboard(text)
	active.SetStatus("COPIED", 3*time.Second)
	app.triggerRedraw()
	go func() {
		time.Sleep(3 * time.Second)
		app.triggerRedraw()
	}()
}

// pasteIntoSearchQuery reads the system clipboard and appends it to the
// active search query, stripping anything that doesn't belong in a single-line
// query (newlines, C0/C1 control codepoints).  Multi-line selections paste
// as their joined visible characters.
func (app *App) pasteIntoSearchQuery() {
	clean := stripForSearchQuery(readClipboard())
	if clean == "" {
		return
	}
	app.mu.Lock()
	app.searchQuery += clean
	app.mu.Unlock()
	app.updateSearch()
}

// stripForSearchQuery removes C0 (U+0000-U+001F) and C1 (U+007F-U+009F)
// control codepoints from s.  The search bar is single-line, so this also
// drops \r, \n, and \t, leaving a query made of the original's printable
// characters joined together.
func stripForSearchQuery(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r < 0x20:
		case r >= 0x7F && r <= 0x9F:
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

// spanContains reports whether any span in spans covers col.
func spanContains(spans []searchSpan, col int) bool {
	for _, sp := range spans {
		if col >= sp.col && col < sp.end {
			return true
		}
	}
	return false
}
