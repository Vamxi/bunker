// render.go - screen painting.
//
// The render loop waits for redraw signals, then:
//  1. Iterates every leaf's vt10x virtual grid and copies cells to tcell.
//  2. Draws separator borders (gray for inactive, green for active).
//  3. Positions the terminal cursor inside the active pane.
//
// VT100 parsing bridge (read side):
//
//	pane.term (vt10x.Terminal) is the virtual terminal updated by readPTY.
//	renderPane reads pane.term.Cell(col, row) for every cell in the pane,
//	converts the vt10x Glyph colour/mode to tcell equivalents, and calls
//	screen.SetContent to stage the glyph for display.
//	pane.mu is held during the scan to prevent races with readPTY.
//
// vt10x attribute bit-mask values are now exported from internal/vt10x as
// AttrXxx constants (e.g. vt10x.AttrBold).  The local vtAttrXxx aliases
// below exist for brevity in render.go.
package main

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"
	"unicode/utf8"

	"bunker/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

// vtAttr aliases — thin local shorthands for the exported vt10x constants.
const (
	vtAttrReverse            int16 = vt10x.AttrReverse
	vtAttrUnderline          int16 = vt10x.AttrUnderline
	vtAttrUnderlineStyleBit0 int16 = vt10x.AttrUnderlineStyleBit0
	vtAttrUnderlineStyleBit1 int16 = vt10x.AttrUnderlineStyleBit1
	vtAttrUnderlineStyleBit2 int16 = vt10x.AttrUnderlineStyleBit2
	vtAttrUnderlineStyleMask int16 = vt10x.AttrUnderlineStyleMask
	vtAttrHasULColor         int16 = vt10x.AttrHasULColor // SGR 58 was explicitly set
	vtAttrBold               int16 = vt10x.AttrBold
	vtAttrItalic             int16 = vt10x.AttrItalic
	vtAttrBlink              int16 = vt10x.AttrBlink
	vtAttrWrap               int16 = vt10x.AttrWrap          // last cell of a soft-wrapped row
	vtAttrDim                int16 = vt10x.AttrDim           // SGR 2
	vtAttrStrikethrough      int16 = vt10x.AttrStrikethrough // SGR 9
	vtAttrInvisible          int16 = vt10x.AttrInvisible     // SGR 8
	vtAttrOverline           int16 = vt10x.AttrOverline      // SGR 53 — overline
)

// ---------------------------------------------------------------------------
// Render loop
// ---------------------------------------------------------------------------

const (
	// renderMinInterval caps painting at ~120 fps, which prevents burning CPU
	// when PTY output arrives faster than the terminal can display it.
	renderMinInterval = 8 * time.Millisecond

	// renderSettleInterval lets bursty erase-and-redraw output land in the
	// virtual terminal before we sample a frame.  Download progress bars often
	// emit "\r <spaces> \r" in one PTY read and the replacement text in the
	// next; rendering the intermediate clear produces visible flicker.
	renderSettleInterval = 2 * time.Millisecond
)

// renderLoop drains the redraw channel and repaints the screen.
func (app *App) renderLoop() {
	defer app.renderWg.Done()
	var lastRender time.Time
	for {
		select {
		case <-app.redraw:
			if !app.waitForNextRender(lastRender) {
				return
			}
			drainRedraw(app.redraw)
			app.render()
			lastRender = time.Now()
		case <-app.done:
			return
		}
	}
}

func (app *App) waitForNextRender(lastRender time.Time) bool {
	delay := nextRenderDelay(lastRender, time.Now())
	if delay <= 0 {
		return true
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-app.done:
		return false
	}
}

func nextRenderDelay(lastRender, now time.Time) time.Duration {
	delay := renderSettleInterval
	if !lastRender.IsZero() {
		if minDelay := renderMinInterval - now.Sub(lastRender); minDelay > delay {
			delay = minDelay
		}
	}
	if delay < 0 {
		return 0
	}
	return delay
}

func drainRedraw(redraw <-chan struct{}) {
	for {
		select {
		case <-redraw:
		default:
			return
		}
	}
}

// render performs a full repaint of the tcell screen from the current state.
//
// app.mu is held for the entire render pass.  This prevents concurrent
// layout mutations (split, remove, resize) from modifying BSP-tree node
// pointers or Pane x/y/w/h fields while we are traversing them, which would
// be a data race.  The lock ordering throughout the codebase is always
// app.mu → Pane.mu, so acquiring Pane.mu inside renderPane is safe.
func (app *App) render() {
	app.mu.Lock()
	defer app.mu.Unlock()

	root := app.root
	active := app.active

	if root == nil {
		app.screen.Clear()
		app.screen.Show()
		return
	}

	rt := app.theme

	// Zoomed mode: draw only the zoomed pane, no borders.
	if zp := app.zoomedPane; zp != nil {
		// Skip rendering mid DEC 2026 sync update — avoids flushing a blank
		// frame after \x1b[2J but before the app has drawn its content.
		// readPTY triggers a fresh redraw when the end marker arrives.
		zp.mu.Lock()
		syncing := zp.term.Mode()&vt10x.ModeSync != 0
		zp.mu.Unlock()
		if syncing {
			return
		}
		if paneHasTransientLineClear(zp) {
			return
		}

		zNode := &Node{pane: zp}
		drawPaneContents(app.screen, zNode, rt)
		drawScrollbars(app.screen, zNode, rt)
		drawAllPaneStatus(app.screen, zNode, active, rt, true)

		zp.mu.Lock()
		dead := zp.dead
		sbOff := zp.sbOff
		cur := zp.term.Cursor()
		curDisplayX := cursorDisplayX(zp, cur)
		visible := zp.term.CursorVisible()
		transientLineClear := zp.transientLineClear
		progressCursorHidden := zp.progressCursorHiddenUntil.After(time.Now())
		zp.mu.Unlock()
		if !dead && visible && sbOff == 0 && !transientLineClear && !progressCursorHidden {
			app.screen.ShowCursor(zp.x+curDisplayX, zp.y+cur.Y)
		} else {
			app.screen.HideCursor()
		}

		app.oscBuf.flush(os.Stdout)
		app.emitTitle(active)
		app.emitCursorStyle(zp)
		closeHostHyperlink(os.Stdout)
		zp.mu.Lock()
		needsSync := zp.needsSync
		zp.needsSync = false
		zp.mu.Unlock()
		if needsSync {
			app.screen.Sync()
		} else {
			app.screen.Show()
		}
		return
	}

	// Step 1 - draw pane contents.
	drawPaneContents(app.screen, root, rt)

	// Step 2 - draw inter-pane separator lines.
	drawBorders(app.screen, root, rt)

	// Step 3 - re-draw borders adjacent to the active pane in accent color.
	if active != nil {
		activeStyle := tcell.StyleDefault.
			Foreground(rt.activeBorder).
			Background(rt.bg)
		paintActiveBorders(app.screen, root, active, activeStyle)
	}

	// Step 3.5 - scrollbars.
	drawScrollbars(app.screen, root, rt)

	// Step 3.6 - status badges.
	drawAllPaneStatus(app.screen, root, active, rt, false)

	// Step 4 - place the hardware cursor inside the active pane.
	// Hidden when the pane is in scrollback mode (cursor is in live view,
	// not in the scrolled-back view the user is reading).
	if active != nil {
		active.mu.Lock()
		dead := active.dead
		sbOff := active.sbOff
		cur := active.term.Cursor()
		curDisplayX := cursorDisplayX(active, cur)
		visible := active.term.CursorVisible() && active.term.Mode()&vt10x.ModeSync == 0
		transientLineClear := active.transientLineClear
		progressCursorHidden := active.progressCursorHiddenUntil.After(time.Now())
		active.mu.Unlock()
		if !dead && visible && sbOff == 0 && !transientLineClear && !progressCursorHidden {
			app.screen.ShowCursor(active.x+curDisplayX, active.y+cur.Y)
		} else {
			app.screen.HideCursor()
		}
	} else {
		app.screen.HideCursor()
	}

	// Step 4.5 - search bar overlay (drawn on top of pane content).
	if app.searchMode && app.searchPane != nil {
		drawSearchBar(app.screen, app.searchPane, &app.keys, app.searchQuery,
			app.searchIdx, len(app.searchMatches))
	}

	// Step 5 - drain OSC passthrough sequences and update host tab title.
	app.oscBuf.flush(os.Stdout)
	app.emitTitle(active)
	app.emitCursorStyle(active)
	closeHostHyperlink(os.Stdout)

	if checkAndClearNeedsSync(root) {
		app.screen.Sync()
	} else {
		app.screen.Show()
	}
}

// checkAndClearNeedsSync walks the pane tree, clears the needsSync flag on
// every pane that has it set, and returns true if at least one pane needed it.
// Called once per render frame; the result decides Show() vs Sync().
func checkAndClearNeedsSync(n *Node) bool {
	if n == nil {
		return false
	}
	if n.isLeaf() {
		n.pane.mu.Lock()
		v := n.pane.needsSync
		n.pane.needsSync = false
		n.pane.mu.Unlock()
		return v
	}
	l := checkAndClearNeedsSync(n.left)
	r := checkAndClearNeedsSync(n.right)
	return l || r
}

func paneHasTransientLineClear(p *Pane) bool {
	p.mu.Lock()
	v := p.transientLineClear
	p.mu.Unlock()
	return v
}

// cursorDisplayX uses the emulator's display-cell coordinates.
func cursorDisplayX(_ *Pane, cur vt10x.Cursor) int {
	return cur.X
}

// closeHostHyperlink writes an OSC 8 close to w.  The render loop calls this
// before every tcell screen.Show()/Sync() to clear any active OSC 8 hyperlink
// state on the host terminal.
//
// Why we need it:
//
// tcell's diff-based painting (tscreen.go's draw()) sets t.curstyle =
// styleInvalid at the start of every frame.  styleInvalid.url is "" (zero
// value).  When the previous frame's last dirty cell had url="X", the host
// terminal is in hyperlink-X state but tcell's internal curstyle has been
// reset to invalid for the new frame.  If the new frame's first dirty cell
// has url="", the URL-transition check
//
//	t.curstyle.url ("") != style.url ("")
//
// is false, so tcell does NOT emit exitUrl.  The host terminal stays inside
// hyperlink X and the new frame's characters render as part of that link —
// which is how `ls --hyperlink=auto` followed by a fresh PS1 prompt produces
// a clickable PS1.
//
// Emitting the close here, BEFORE tcell paints, sidesteps the tcell quirk
// without touching tcell's internal state: the host's URL state is now
// closed, and tcell will re-emit enterUrl for the first dirty cell that has
// url != "" (because styleInvalid.url="" still differs from "X").  Existing
// hyperlinks keep working; new ones don't leak.
func closeHostHyperlink(w io.Writer) {
	w.Write([]byte("\x1b]8;;\x1b\\")) //nolint:errcheck
}

// emitTitle writes an OSC 0 window-title sequence to the host terminal if the
// active pane's title has changed since the last call.  This keeps the tab
// title in Blackbox, Ptyxis, and other tabbed terminals up to date.
//
// Title priority:
//  1. The title set by the pane's shell via OSC 0/1/2 (e.g. bash PROMPT_COMMAND).
//  2. Fallback: "<fgProcess>: <cwd base>" when the shell emits no title.
func (app *App) emitTitle(active *Pane) {
	if active == nil {
		return
	}
	active.mu.Lock()
	title := active.term.Title()
	fgProc := active.fgProcess
	active.mu.Unlock()

	if title == "" {
		// Construct a basic title from process + cwd.
		cwd := active.cwd()
		if cwd != "" {
			// Show only the last two path components to keep it short.
			parts := strings.Split(strings.TrimRight(cwd, "/"), "/")
			if len(parts) > 2 {
				cwd = "…/" + parts[len(parts)-2] + "/" + parts[len(parts)-1]
			}
		}
		switch {
		case fgProc != "" && cwd != "":
			title = fgProc + ": " + cwd
		case cwd != "":
			title = cwd
		case fgProc != "":
			title = fgProc
		default:
			title = "bunk"
		}
	}

	// Append [active/total] when there are multiple panes.
	if app.root != nil {
		leaves := app.root.leaves()
		total := len(leaves)
		if total > 1 {
			idx := 1
			for i, n := range leaves {
				if n.pane == active {
					idx = i + 1
					break
				}
			}
			title = fmt.Sprintf("%s [%d/%d]", title, idx, total)
		}
	}

	// Strip control bytes and invalid UTF-8 that would corrupt the host
	// terminal. Pane titles are set by the shell via OSC 0/1/2 — but a pane
	// that displayed binary content (cat /bin/ls) may have a title full of
	// arbitrary bytes that must not be re-emitted to the host.
	title = sanitizeTitle(title)

	if title == app.lastEmittedTitle {
		return
	}
	app.lastEmittedTitle = title
	// OSC 0 sets both icon name and window title; BEL-terminated.
	os.Stdout.Write([]byte("\x1b]0;" + title + "\x07")) //nolint:errcheck
}

// sanitizeTitle keeps only printable runes (no C0/C1 controls), validates
// UTF-8, and caps the length to keep OSC 0 sequences small enough that any
// host terminal will accept them.
func sanitizeTitle(s string) string {
	const maxTitleBytes = 512
	if len(s) > maxTitleBytes {
		s = s[:maxTitleBytes]
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		switch {
		case r == utf8.RuneError:
			continue
		case r < 0x20:
			continue
		case r >= 0x7F && r <= 0x9F:
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// emitCursorStyle updates the tcell cursor style when the active pane's
// DECSCUSR setting has changed.  Uses tcell's SetCursor() so the style
// is emitted as part of Show()/Sync() rather than racing with it.
func (app *App) emitCursorStyle(p *Pane) {
	if p == nil {
		return
	}
	p.mu.Lock()
	style := p.term.Cursor().Shape
	color := oscCursorColor(p.themeCursorColor)
	if c, ok := p.term.ColorOverride(vt10x.DefaultCursor); ok {
		color = tcell.NewRGBColor(int32(c>>16&255), int32(c>>8&255), int32(c&255))
	}
	p.mu.Unlock()
	if style == app.lastCursorStyle && color == app.lastCursorColor {
		return
	}
	app.lastCursorStyle = style
	app.lastCursorColor = color
	app.screen.SetCursorStyle(tcell.CursorStyle(style), color)
}

func (app *App) paneOSCColors() hostOSCColors {
	fg, bg, cursor := defaultOSCColors(app.theme, app.hostOSCColors)
	return hostOSCColors{fg: fg, bg: bg, cursor: cursor}
}

// ---------------------------------------------------------------------------
// Pane content rendering
// ---------------------------------------------------------------------------

// drawPaneContents recursively renders every leaf's virtual terminal grid.
func drawPaneContents(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		renderPane(scr, n.pane, rt)
		return
	}
	drawPaneContents(scr, n.left, rt)
	drawPaneContents(scr, n.right, rt)
}

// renderPane paints a single pane's vt10x Glyph grid onto the tcell screen,
// then overlays the scrollbar on the rightmost column.
//
// When p.sbOff == 0 the live vt10x grid is rendered directly.
// When p.sbOff > 0 we display a "virtual" view that combines scrollback lines
// (from p.sb) above the live grid:
//
//	Virtual line 0                = oldest captured scrollback line
//	…
//	Virtual line p.sb.count-1    = most recently scrolled-off line
//	Virtual line p.sb.count      = live term row 0  (current top)
//	…
//	Virtual line p.sb.count+h-1  = live term row h-1 (current bottom)
//
// With sbOff = N we display virtual lines [p.sb.count-N, p.sb.count-N+h).
func renderPane(scr tcell.Screen, p *Pane, rt resolvedTheme) {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Second guard: close the TOCTOU window between render()'s top-level
	// check and this vt10x read. readPTY may have started a new sync frame
	// (blanking vt10x) in between. Returning here leaves tcell's cell buffer
	// unchanged, so Show() sends nothing — no blank flash reaches the terminal.
	if p.term.Mode()&vt10x.ModeSync != 0 || p.transientLineClear {
		return
	}

	cols, rows := p.term.Size()
	sbOff := p.sbOff
	sbCount := p.sb.count
	hasSel := p.selActive
	hasSearch := p.searchHL != nil
	statusKey := statusOverlayKeyLocked(p, time.Now())
	colorGen := p.term.ColorGen()

	// Determine whether a full repaint is needed.
	//
	// Full repaint is required when any overlay changes (scroll position,
	// search highlights, selection, status badges) — a row that vt10x
	// hasn't written to may still need repainting because an overlay on top
	// of it changed.
	//
	// When sbOff > 0 the viewport mixes ring rows (no dirty tracking) with
	// live terminal rows, so full repaint is always used in scroll mode.
	fullRepaint := p.forceFullRepaint ||
		sbOff > 0 ||
		sbOff != p.lastRenderSbOff ||
		hasSel != p.lastRenderSelActive ||
		p.selAnchor != p.lastRenderSelAnchor ||
		p.selCursor != p.lastRenderSelCursor ||
		p.searchHLGen != p.lastRenderSearchHLGen ||
		statusKey != p.lastRenderStatusKey ||
		colorGen != p.lastRenderColorGen

	// Always consume dirty to keep vt10x state clean (avoids accumulating
	// dirty rows while the pane is scrolled that would confuse later renders).
	dirty, anyDirty := p.term.ConsumeDirty()

	// Record the overlay state we are rendering with.
	p.lastRenderSbOff = sbOff
	p.lastRenderSelActive = hasSel
	p.lastRenderSelAnchor = p.selAnchor
	p.lastRenderSelCursor = p.selCursor
	p.lastRenderSearchHLGen = p.searchHLGen
	p.lastRenderStatusKey = statusKey
	p.lastRenderColorGen = colorGen
	p.forceFullRepaint = false

	// Nothing to draw: pane is in live view, no overlay changed, and vt10x
	// reports no cell writes since the last render.
	if !fullRepaint && !anyDirty {
		return
	}

	for row := 0; row < rows; row++ {
		// Skip rows that vt10x hasn't touched and that don't need overlay
		// repainting.  Only valid in live view (sbOff==0) because scrolled
		// rows come from the ring which has no dirty tracking.
		if !fullRepaint && (dirty == nil || !dirty[row]) {
			continue
		}

		// Unified virtual row: stable across scrolls (see selPos in pane.go).
		vRow := (sbCount - sbOff) + row

		var cells []vt10x.Glyph
		useTermDirect := true // read from p.term.Cell() directly
		termRow := row        // which live-terminal row to read (== row unless scrolled)
		if sbOff > 0 {
			useTermDirect = false
			switch {
			case vRow < 0:
				// Before the oldest captured line - render blank.
			case vRow < sbCount:
				cells = p.sb.get(vRow)
			default:
				// In the live grid.  vRow - sbCount is the actual terminal row,
				// which is NOT the same as screen row when sbOff > 0.
				useTermDirect = true
				termRow = vRow - sbCount
			}
		}

		// Fetch per-row search spans once before the column loop so the
		// inner loop does a short linear scan instead of a hash lookup per
		// cell.
		var regSpans, curSpans []searchSpan
		if hasSearch {
			regSpans = p.searchHL.regular[vRow]
			curSpans = p.searchHL.current[vRow]
		}

		for col := 0; col < min(cols, p.w-1); col++ {

			// Default: theme colours for cells not populated from scrollback or
			// the live terminal (blank rows before oldest history, and columns
			// beyond a scrollback row's captured width after a resize).
			cell := vt10x.Glyph{FG: vt10x.DefaultFG, BG: vt10x.DefaultBG}
			if cells != nil && col < len(cells) {
				cell = cells[col]
			} else if useTermDirect {
				cell = p.term.RawCell(col, termRow)
			}
			if cell.Width == -1 {
				continue
			}
			if fg, ok := p.term.ColorOverride(cell.FG); ok {
				cell.FG = fg
			}
			if bg, ok := p.term.ColorOverride(cell.BG); ok {
				cell.BG = bg
			}

			ch := cell.Char
			if ch == 0 {
				ch = ' '
			}

			style := tcell.StyleDefault.
				Foreground(vtColor(cell.FG, rt.fg, rt)).
				Background(vtColor(cell.BG, rt.bg, rt))
			if cell.Image() != nil {
				bg := vtColor(cell.BG, rt.bg, rt)
				style = style.Foreground(imageColor(cell.Image().Top, bg)).Background(imageColor(cell.Image().Bottom, bg))
			}

			// Erased cells have no decorations, but explicitly printed spaces
			// can be underlined/overlined. Invisible text keeps its copy data.
			isBlank := ch == ' '
			if cell.Mode&vtAttrInvisible != 0 {
				ch = ' '
				isBlank = true
			}
			if cell.Mode&vtAttrInvisible == 0 {
				if cell.Link != 0 {
					if url := p.term.Link(cell.Link); url != "" {
						style = style.Url(url)
					}
				}
				if cell.Mode&vtAttrBold != 0 {
					style = style.Bold(true)
				}
				if cell.Mode&vtAttrDim != 0 {
					// SGR 2 (dim/faint): terminals only apply the visual
					// dim effect to palette-indexed colours.  With explicit
					// RGB colours — which bunk always uses for theme FG/BG —
					// most terminals (VTE/Ptyxis, xterm) silently ignore
					// ti.Dim.  We blend the FG 50% toward BG ourselves so
					// dim is always visible regardless of terminal.
					dimFG, dimBG, _ := style.Decompose()
					if dimFG.IsRGB() && dimBG.IsRGB() {
						fr, fg2, fb := dimFG.RGB()
						br, bg2, bb := dimBG.RGB()
						style = style.Foreground(tcell.NewRGBColor(
							(fr+br)/2, (fg2+bg2)/2, (fb+bb)/2,
						))
					} else {
						style = style.Dim(true) // palette colours: let terminal handle it
					}
				}
				if cell.Mode&vtAttrUnderline != 0 {
					// Map the 3-bit underline style field to tcell's UnderlineStyle.
					// style bits: 00=solid 01=double 10=curly 11=dotted 100=dashed
					ulStyle := tcell.UnderlineStyleSolid
					switch (cell.Mode & vtAttrUnderlineStyleMask) / vtAttrUnderlineStyleBit0 {
					case 1:
						ulStyle = tcell.UnderlineStyleDouble
					case 2:
						ulStyle = tcell.UnderlineStyleCurly
					case 3:
						ulStyle = tcell.UnderlineStyleDotted
					case 4:
						ulStyle = tcell.UnderlineStyleDashed
					}
					style = style.Underline(ulStyle)
					// SGR 58: underline colour (only when explicitly set via SGR 58).
					if cell.Mode&vtAttrHasULColor != 0 {
						style = style.Underline(vtColor(cell.UL, rt.fg, rt))
					}
				}
				if cell.Mode&vtAttrBlink != 0 {
					style = style.Blink(true)
				}
				if cell.Mode&vtAttrItalic != 0 {
					style = style.Italic(true)
				}
				if cell.Mode&vtAttrStrikethrough != 0 {
					style = style.StrikeThrough(true)
				}
				if cell.Mode&vt10x.AttrOverline != 0 {
					style = style.Overline(true)
				}
			}
			// NOTE: we do NOT apply tcell.Reverse(true) for vtAttrReverse.
			// vt10x's setChar() pre-swaps FG/BG when attrReverse is set,
			// so the stored cell.FG and cell.BG already reflect the
			// reversed colours.  Applying Reverse(true) would double-swap
			// them, cancelling the effect and producing wrong colours
			// (visible as shifted half-block bar edges in chart TUIs).

			// Selection highlight: toggle reverse video so selected text is
			// always visually distinct regardless of the cell's original style.
			if hasSel && p.selContainsUnlocked(vRow, col) {
				style = style.Reverse(true)
			}

			// Search highlight: amber for regular matches, orange for current.
			// Current-match spans take priority over regular spans.
			if hasSearch {
				switch {
				case spanContains(curSpans, col):
					style = style.Background(tcell.NewRGBColor(0xff, 0xa5, 0x00)).
						Foreground(tcell.ColorBlack)
				case spanContains(regSpans, col):
					style = style.Background(tcell.NewRGBColor(0x80, 0x60, 0x00)).
						Foreground(tcell.NewRGBColor(0xff, 0xe0, 0x80))
				}
			}

			if cell.Width == 2 && col+1 >= min(cols, p.w-1) {
				ch = ' '
			}
			var combining []rune
			if ch == cell.Char {
				combining = []rune(cell.Combining())
			}
			scr.SetContent(p.x+col, p.y+row, ch, combining, style)
			if cell.Width == 2 && isBlank && col+1 < p.w-1 {
				scr.SetContent(p.x+col+1, p.y+row, ' ', nil, style)
			}
		}
		// The grid can be narrower than the pane during resize coalescing.
		blankStyle := tcell.StyleDefault.Background(rt.bg)
		for dc := cols; dc < p.w-1; dc++ {
			scr.SetContent(p.x+dc, p.y+row, ' ', nil, blankStyle)
		}
	}

	// Clear rows below the vt10x grid if the pane grew taller than the
	// terminal during resize coalescing.
	if rows < p.h {
		blankStyle := tcell.StyleDefault.Background(rt.bg)
		for row := rows; row < p.h; row++ {
			for col := 0; col < p.w-1; col++ {
				scr.SetContent(p.x+col, p.y+row, ' ', nil, blankStyle)
			}
		}
	}

}

// drawScrollbars walks the BSP tree and draws a scrollbar for every leaf that
// has scrollback history (sbCount > 0).  The bar always occupies the last
// column of the pane's node region (p.x+p.w-1), which is permanently reserved
// — the PTY terminal is created one column narrower (w-1) so content never
// reaches that column.
func drawScrollbars(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		p := n.pane
		p.mu.Lock()
		sbCount := p.sb.count
		sbOff := p.sbOff
		_, rows := p.term.Size()
		p.mu.Unlock()
		bx := p.x + p.w - 1
		if sbOff > 0 {
			drawScrollbar(scr, bx, p.y, rows, sbCount, sbOff, rt)
		} else {
			// Always draw the scrollbar column so stale scrollbar glyphs
			// don't persist when the user returns to live view.
			blankStyle := tcell.StyleDefault.Background(rt.bg)
			for row := 0; row < rows; row++ {
				scr.SetContent(bx, p.y+row, ' ', nil, blankStyle)
			}
		}
		return
	}
	drawScrollbars(scr, n.left, rt)
	drawScrollbars(scr, n.right, rt)
}

// drawScrollbar draws a narrow one-column scrollbar at screen column bx.
//
// Characters (thin, minimal):
//
//	'▕'  U+2595  RIGHT ONE EIGHTH BLOCK - empty track
//	'▐'  U+2590  RIGHT HALF BLOCK       - scrollbar thumb
func drawScrollbar(scr tcell.Screen, bx, by, h, sbCount, sbOff int, rt resolvedTheme) {
	total := sbCount + h // total virtual lines

	// Handle height: proportional to the visible fraction, minimum 1 row.
	handleH := max(1, h*h/total)

	// Handle top: where in [0, h) the visible window starts.
	// visibleStart = sbCount - sbOff  (0 = top of scrollback, sbCount = live top)
	visibleStart := sbCount - sbOff
	handleTop := visibleStart * h / total

	// Clamp so the handle never overflows the track.
	if handleTop+handleH > h {
		handleTop = h - handleH
	}
	if handleTop < 0 {
		handleTop = 0
	}

	trackStyle := tcell.StyleDefault.
		Foreground(rt.scrollTrack).
		Background(rt.bg)
	thumbStyle := tcell.StyleDefault.
		Foreground(rt.scrollThumb).
		Background(rt.bg)

	for row := 0; row < h; row++ {
		ch := '▕'
		style := trackStyle
		if row >= handleTop && row < handleTop+handleH {
			ch = '▐'
			style = thumbStyle
		}
		scr.SetContent(bx, by+row, ch, nil, style)
	}
}

// ---------------------------------------------------------------------------
// Search bar overlay
// ---------------------------------------------------------------------------

// drawSearchBar renders a one-row search overlay at the bottom of pane p.
// Called from render() (under app.mu) when search mode is active.
//
// Layout, left to right:
//
//	" Search: <query>  <idx>/<n> █                         <hint> "
//	└──────── label + cursor ─────┘    (blank fill)        └hint┘
//
// The right-side hint reminds the user of the chord set ("↵ next · ^P prev
// · ^V paste · Esc exit").  It is rendered only if the pane is wide enough
// to fit label + cursor + 2-column gap + hint without truncation;
// otherwise we drop it silently rather than chop a key name in half.
func drawSearchBar(scr tcell.Screen, p *Pane, kb *Keybindings, query string, matchIdx, matchCount int) {
	y := p.y + p.h - 1

	var label string
	switch {
	case query == "":
		label = " Search: "
	case matchCount == 0:
		label = fmt.Sprintf(" Search: %s  (no matches) ", query)
	default:
		label = fmt.Sprintf(" Search: %s  %d/%d ", query, matchIdx+1, matchCount)
	}

	barStyle := tcell.StyleDefault.
		Background(tcell.NewRGBColor(0x1a, 0x1a, 0x44)).
		Foreground(tcell.ColorWhite)
	noMatchStyle := barStyle.Foreground(tcell.NewRGBColor(0xff, 0x66, 0x66))
	// Dimmer foreground for the hint so it reads as guidance, not content.
	hintStyle := barStyle.Foreground(tcell.NewRGBColor(0x80, 0x80, 0xb0))

	s := barStyle
	if matchCount == 0 && query != "" {
		s = noMatchStyle
	}

	col := p.x
	for _, ch := range label {
		if col >= p.x+p.w {
			break
		}
		scr.SetContent(col, y, ch, nil, s)
		col++
	}
	// Blinking-cursor indicator.
	if col < p.x+p.w {
		scr.SetContent(col, y, '█', nil, s)
		col++
	}

	// Compose the hint from the live keybindings so remapped keys (e.g.
	// search_next = "f3") show their actual binding rather than a stale
	// default.  Only render if it fits with at least a 2-column gap.
	hint := fmt.Sprintf("%s next · %s prev · %s exit",
		compactKeyName(kb.SearchNext.raw),
		compactKeyName(kb.SearchPrev.raw),
		compactKeyName(kb.SearchExit.raw))
	hintRunes := []rune(hint)
	hintStart := p.x + p.w - len(hintRunes) - 1 // 1-col right pad
	if hintStart >= col+2 {                     // 2-col gap after cursor
		// Pad the gap.
		for ; col < hintStart; col++ {
			scr.SetContent(col, y, ' ', nil, barStyle)
		}
		for _, ch := range hintRunes {
			scr.SetContent(col, y, ch, nil, hintStyle)
			col++
		}
	}

	// Pad to end of pane width.
	for ; col < p.x+p.w; col++ {
		scr.SetContent(col, y, ' ', nil, barStyle)
	}
}

// compactKeyName turns a parseKey-style spec ("ctrl+n", "alt+r",
// "shift+pgup", "escape") into a short on-screen hint ("^N", "M-R",
// "S-Pgup", "Esc").  Returned values use one terminal column per visible
// character so drawSearchBar's layout math matches the actual rendered
// width.
func compactKeyName(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	parts := strings.Split(s, "+")
	keyName := parts[len(parts)-1]
	mods := parts[:len(parts)-1]

	// Special standalone keys (no modifiers needed) get glyph hints.  We
	// only short-circuit when there are no modifiers; "ctrl+enter" should
	// still render as "^Enter" rather than "^↵".
	if len(mods) == 0 {
		switch keyName {
		case "escape", "esc":
			return "Esc"
		case "enter", "return":
			return "↵"
		case "tab":
			return "⇥"
		case "backspace":
			return "⌫"
		}
	}

	var prefix strings.Builder
	for _, m := range mods {
		switch m {
		case "ctrl":
			prefix.WriteByte('^')
		case "alt":
			prefix.WriteString("M-")
		case "shift":
			prefix.WriteString("S-")
		}
	}

	// Format key name: single letters uppercase, multi-letter title-case.
	var key string
	switch {
	case len(keyName) == 1 && keyName[0] >= 'a' && keyName[0] <= 'z':
		key = strings.ToUpper(keyName)
	case len(keyName) > 0:
		key = strings.ToUpper(keyName[:1]) + keyName[1:]
	}
	return prefix.String() + key
}

// ---------------------------------------------------------------------------
// Border rendering
// ---------------------------------------------------------------------------

// drawBorders draws a gray separator line at every internal node split point.
func drawBorders(scr tcell.Screen, n *Node, rt resolvedTheme) {
	if n.isLeaf() {
		return
	}
	borderStyle := tcell.StyleDefault.
		Foreground(rt.inactiveBorder).
		Background(rt.bg)

	if n.dir == splitVertical {
		bx := n.left.x + n.left.w
		for y := n.y; y < n.y+n.h; y++ {
			scr.SetContent(bx, y, tcell.RuneVLine, nil, borderStyle)
		}
	} else {
		by := n.left.y + n.left.h
		for x := n.x; x < n.x+n.w; x++ {
			scr.SetContent(x, by, tcell.RuneHLine, nil, borderStyle)
		}
	}

	drawBorders(scr, n.left, rt)
	drawBorders(scr, n.right, rt)
}

// paintActiveBorders re-colours only the segment of each separator that is
// directly adjacent to the active pane.
//
// For a vertical separator at column bx the active segment spans the rows
// [active.y, active.y+active.h).  For a horizontal separator at row by the
// active segment spans the columns [active.x, active.x+active.w).
// This means that in a 2×2 grid only the half of each divider that borders the
// active pane is highlighted; the other half stays in the inactive colour.
//
// Returns true if the active pane is found in the subtree rooted at n, which
// avoids redundant tree walks (O(n) instead of O(n²)).
func paintActiveBorders(scr tcell.Screen, n *Node, active *Pane, style tcell.Style) bool {
	if n.isLeaf() {
		return n.pane == active
	}

	leftHas := paintActiveBorders(scr, n.left, active, style)
	rightHas := paintActiveBorders(scr, n.right, active, style)

	if leftHas || rightHas {
		if n.dir == splitVertical {
			bx := n.left.x + n.left.w
			yStart := max(n.y, active.y)
			yEnd := min(n.y+n.h, active.y+active.h)
			for y := yStart; y < yEnd; y++ {
				scr.SetContent(bx, y, tcell.RuneVLine, nil, style)
			}
		} else {
			by := n.left.y + n.left.h
			xStart := max(n.x, active.x)
			xEnd := min(n.x+n.w, active.x+active.w)
			for x := xStart; x < xEnd; x++ {
				scr.SetContent(x, by, tcell.RuneHLine, nil, style)
			}
		}
	}

	return leftHas || rightHas
}

// ---------------------------------------------------------------------------
// Colour conversion
// ---------------------------------------------------------------------------

// vtColor converts a vt10x Color to the nearest tcell Color, applying the
// theme palette for ANSI colors 0-15 and the theme's fg/bg for defaults.
func vtColor(c vt10x.Color, def tcell.Color, rt resolvedTheme) tcell.Color {
	switch c {
	case vt10x.DefaultFG:
		return rt.fg
	case vt10x.DefaultBG:
		return rt.bg
	case vt10x.DefaultCursor:
		return def
	}
	if c < 16 {
		// ANSI colors 0-15: use theme palette, or fall through to the
		// terminal's own palette when the theme leaves the slot unset.
		if rt.palette[c] != tcell.ColorDefault {
			return rt.palette[c]
		}
		return tcell.PaletteColor(int(c))
	}
	if c < 256 {
		// xterm-256 colors 16-255: standard palette.
		return tcell.PaletteColor(int(c))
	}
	if c < vt10x.DefaultFG {
		// True-color RGB: packed as r<<16 | g<<8 | b by vt10x.
		return tcell.NewRGBColor(int32(c>>16&0xff), int32(c>>8&0xff), int32(c&0xff))
	}
	return def
}
