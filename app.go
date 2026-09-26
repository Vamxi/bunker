// app.go - App struct, event loop, key handling, and pane management.
package main

import (
	"fmt"
	"math"
	"os"
	"sync"
	"time"

	"github.com/gdamore/tcell/v2"
)

// App holds every piece of global state and mediates between the event loop,
// the render loop, and the layout tree.
type App struct {
	screen tcell.Screen
	theme  resolvedTheme // active colour theme, set at startup
	keys   Keybindings   // resolved hotkey configuration

	scrollback      int // max scrollback lines per pane (from config)
	scrollbackBytes int // optional scrollback memory cap per pane; 0 = none

	// cellAspect is the pixel height-to-width ratio of a single terminal cell
	// (cellH / cellW).  Typical fonts give ~1.8-2.2.  Used by splitActive to
	// decide whether a pane is wider or taller in actual pixels.
	// Set at startup from TIOCGWINSZ ws_xpixel/ws_ypixel; falls back to 2.0.
	cellAspect float64

	// mu guards root, active, and nextID.  All layout mutations must hold it.
	mu     sync.Mutex
	root   *Node // root of the BSP layout tree
	active *Pane // the pane that currently receives keystrokes
	nextID int   // monotonically increasing pane ID

	// redraw is a 1-element channel; any goroutine writes a token to ask for a
	// screen repaint.  The render loop drains it.  The buffer of 1 coalesces
	// rapid bursts into a single repaint.
	redraw chan struct{}

	// paneDead receives a *Pane when its shell process exits.
	paneDead chan *Pane

	// done is closed to signal all background goroutines to stop.
	done     chan struct{}
	shutOnce sync.Once
	renderWg sync.WaitGroup // tracks the render loop goroutine

	// oscBuf accumulates OSC sequences from pane readPTY goroutines (and the
	// clipboard helper) for delivery to the host terminal. The render loop
	// flushes it to os.Stdout before tcell.Show() so the host terminal
	// receives OSC 7/8/52/133 (CWD, hyperlinks, clipboard, prompt markers).
	oscBuf *oscBuffer

	// lastEmittedTitle is the most recent title we wrote to the host terminal
	// via OSC 0.  Compared each frame to avoid redundant writes.
	lastEmittedTitle string

	// lastCursorStyle is the DECSCUSR value (0-6) last written to the host
	// terminal.  Compared each frame to avoid redundant writes.
	lastCursorStyle int
	lastCursorColor tcell.Color

	// hostOSCColors caches the outer terminal's probed default fg/bg/cursor
	// colours for theme="terminal" mode so new panes can answer OSC 10/11/12
	// queries consistently without re-probing after tcell owns stdin.
	hostOSCColors hostOSCColors

	// prevMouseBtn remembers the last pressed mouse button so mouseToBytes can
	// generate release events (only used in the event loop goroutine).
	prevMouseBtn tcell.ButtonMask

	// Double-click detection (event loop goroutine only - no lock needed).
	lastClickTime  time.Time
	lastClickPos   selPos
	lastClickPane  *Pane
	dblClickActive bool // release should not overwrite word selection

	// dragPane is the pane where the current Button1 drag started.
	// All drag-select and release events are clamped to this pane so that
	// moving the cursor outside the originating pane doesn't extend the
	// selection into a different pane.  Cleared on Button1 release.
	dragPane *Pane

	// dragEdgeStop stops the edge-auto-scroll goroutine when a drag moves
	// away from the edge or the button is released.  Nil when no goroutine
	// is running.  Access only from the event-loop goroutine.
	dragEdgeStop chan struct{}

	// sbDragPane is non-nil while the user is dragging the scrollbar.
	// sbDragLastY is the Y cell from the previous drag event (for relative deltas).
	// sbDragAccum is the accumulated fractional sbOff so sub-line deltas don't vanish.
	// All three are accessed only from the event-loop goroutine.
	sbDragPane  *Pane
	sbDragLastY int
	sbDragAccum float64

	// Search state.  All fields are protected by mu (read by the render loop,
	// written by the event loop via enterSearch/exitSearch/updateSearch).
	searchMode    bool
	searchQuery   string
	searchPane    *Pane
	searchMatches []searchMatch
	searchIdx     int
	searchGen     int // incremented on every search state change; guards async goroutines
	searchWake    chan struct{}
	searchOnce    sync.Once

	// Zoom state.  Protected by mu.
	// When zoomedPane is non-nil, only that pane is drawn (fullscreen).
	zoomedPane *Pane
	zoomGeom   [4]int // saved {x, y, w, h} to restore on unzoom

	// resizeTimer coalesces rapid terminal resize events.  The lightweight
	// PTY-size update happens immediately; the expensive rawBuf replay is
	// deferred until 50ms after the last resize event.
	resizeTimer *time.Timer

	// GUI hosts (no tcell screen) set these. sizeFn reports the grid size
	// a zoomed pane fills; onEmpty runs, instead of shutdown, when the last
	// pane is removed. Both may be nil when screen is set.
	sizeFn  func() (w, h int)
	onEmpty func()
	// clipboard replaces the TUI's clipboard tools (see clipboard.go).
	clipboard clipboard
	// onAlert receives the panes' alerts (pane_alert.go) on their reader
	// goroutines; the GUI sets it before the first pane starts.
	onAlert func(*Pane, paneAlert)
	// post runs a function on the UI thread (the GTK main loop, or the
	// event loop below). nil runs it at once, which tests rely on.
	post func(func())
}

// viewSize is the full grid size available to panes.
func (app *App) viewSize() (int, int) {
	if app.screen == nil && app.sizeFn != nil {
		return app.sizeFn()
	}
	return app.screen.Size()
}

// shutdown is safe to call multiple times.  It closes every pane (sending
// SIGHUP to each shell), finalises the tcell screen (restoring the host
// terminal), and closes done so background goroutines exit cleanly.
//
// NOTE: post-Fini terminal cleanup (clearing the screen, stty sane, etc.) is
// intentionally done in run() AFTER app.eventLoop() returns, not here.
func (app *App) shutdown() {
	app.shutOnce.Do(func() {
		L.Info("shutdown: closing all panes")
		app.closeAllPanes()
		close(app.done)

		waitCh := make(chan struct{})
		go func() { app.renderWg.Wait(); close(waitCh) }()
		select {
		case <-waitCh:
		case <-time.After(500 * time.Millisecond):
		}

		app.screen.DisableMouse()
		// Reset cursor style to default so the host terminal isn't left
		// with a non-default shape after bunk exits.
		app.screen.SetCursorStyle(tcell.CursorStyleDefault, tcell.ColorReset)
		app.screen.Fini()
	})
}

// closeAllPanes sends SIGHUP to every live shell process and closes every
// PTY master.  It is safe to call concurrently.
func (app *App) closeAllPanes() {
	app.mu.Lock()
	var panes []*Pane
	if app.root != nil {
		for _, leaf := range app.root.leaves() {
			panes = append(panes, leaf.pane)
		}
	}
	app.mu.Unlock()
	for _, p := range panes {
		p.close()
	}
}

// triggerRedraw sends a non-blocking redraw signal.
func (app *App) triggerRedraw() {
	select {
	case app.redraw <- struct{}{}:
	default:
	}
}

// reinitHost recovers from host-terminal corruption (typically caused by a
// pane that printed binary data containing escape sequences) by emitting a
// targeted reset and forcing tcell to repaint every cell.
//
// We deliberately do NOT call screen.Suspend()/Resume() here.  Suspend()
// goes through tcell's disengage() which calls cells.Resize(0,0) — wiping
// the cell buffer.  After Resume() the buffer is empty and Sync() paints
// nothing, leaving a blank screen until the shell happens to redraw on the
// next keystroke.  We hit exactly that bug while developing this command.
//
// We also do NOT emit RIS (ESC c).  RIS exits alt-screen mode, but tcell
// has no way to know it needs to re-enter — its internal state still says
// "alt-screen on", so subsequent paints would land on the user's primary
// scrollback.
//
// What we DO emit, written directly to stdout under app.mu:
//
//	\x1b[m       SGR reset — clears bold/colours/etc. left over from the
//	              binary stream.
//	\x1b(B       Set G0 charset to ASCII — fixes line-drawing-mode glyph
//	              lockup, the most common after-cat-binary symptom.
//	\x1b]8;;\x1b\\  Close any active OSC 8 hyperlink.
//
// Then we invalidate bunk's per-frame emit caches (so title and cursor
// style get re-sent) and call screen.Sync().  Sync()'s clearScreen() emits
// its own AttrOff + exitUrl + default fg/bg + clear before invalidating
// every cell and repainting from tcell's still-intact cell buffer.
//
// Net effect: the host terminal is reset enough to render correctly, tcell
// re-emits all visual state, and the user's panes redraw immediately
// without a keystroke.
func (app *App) reinitHost() {
	app.mu.Lock()
	defer app.mu.Unlock()

	if _, err := os.Stdout.Write([]byte("\x1b[m\x1b(B\x1b]8;;\x1b\\")); err != nil {
		L.Warn("reinitHost: reset write failed", "err", err)
	}

	// Invalidate emit caches so the next frame re-sends title and cursor
	// style — the host may have lost them, and the cache would otherwise
	// suppress the re-emit.
	app.lastEmittedTitle = ""
	app.lastCursorStyle = -1

	app.screen.Sync()
	L.Info("reinitHost: host terminal re-initialised")
}

// ---------------------------------------------------------------------------
// Event loop
// ---------------------------------------------------------------------------

// eventLoop processes tcell events until the screen is finalised (PollEvent
// returns nil) or Ctrl+Q is pressed.
func (app *App) eventLoop() {
	for {
		ev := app.screen.PollEvent()
		if ev == nil {
			L.Debug("eventLoop: PollEvent returned nil, exiting")
			return // screen.Fini() was called
		}
		switch ev := ev.(type) {
		case *uiFuncEvent:
			ev.f()
		case *tcell.EventResize:
			L.Debug("event: resize")
			app.handleResize()
		case *tcell.EventKey:
			L.Debug("event: key", "key", ev.Name(), "mod", ev.Modifiers(), "rune", ev.Rune())
			if !app.handleKey(ev) {
				L.Info("eventLoop: shutdown requested via key")
				app.shutdown()
				return
			}
		case *tcell.EventMouse:
			x, y := ev.Position()
			L.Debug("event: mouse", "btn", ev.Buttons(), "x", x, "y", y, "mod", ev.Modifiers())
			app.handleMouse(ev)
		case *tcell.EventPaste:
			L.Debug("event: paste", "start", ev.Start())
			app.handlePaste(ev)
		case *tcell.EventFocus:
			app.handleFocus(ev.Focused)
		}
	}
}

// handleResize is called when the host terminal changes size.
// During a drag-resize the host terminal fires many SIGWINCH events in rapid
// succession.  Each resize involves an expensive rawBuf replay (O(rawBuf) per
// pane).  We coalesce events by postponing the actual reflow if another resize
// arrives within 50ms.  A lightweight PTY-size-only update is applied
// immediately so the shell receives SIGWINCH promptly and can redraw, while
// the costly rawBuf replay is deferred to the final size.
func (app *App) handleResize() {
	app.screen.Sync()
	w, h := app.screen.Size()
	L.Debug("handleResize", "w", w, "h", h)

	// Cancel any pending deferred resize.
	if app.resizeTimer != nil {
		app.resizeTimer.Stop()
	}

	// Immediate lightweight pass: update pane x/y/w/h and PTY sizes so
	// the shells receive SIGWINCH right away.  Skip the expensive rawBuf
	// replay (resizeAndReflow) — that's deferred below.
	app.mu.Lock()
	if app.root != nil {
		app.root.resizePTYOnly(0, 0, w, h)
	}
	if p := app.zoomedPane; p != nil {
		app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}
		p.resizePTYOnly(0, 0, w, h)
	}
	app.mu.Unlock()
	app.triggerRedraw()

	// Deferred reflow: wait 50ms for more resize events before doing the
	// expensive rawBuf replay.  If another resize arrives, the timer is
	// reset and only the final size triggers the replay.
	app.resizeTimer = time.AfterFunc(50*time.Millisecond, func() {
		sw, sh := app.screen.Size()
		L.Debug("handleResize: deferred reflow", "w", sw, "h", sh)
		app.mu.Lock()
		if app.root != nil {
			app.root.resize(0, 0, sw, sh)
		}
		if p := app.zoomedPane; p != nil {
			app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}
			p.resize(0, 0, sw, sh)
		}
		app.mu.Unlock()
		app.triggerRedraw()
	})
}

// handleKey routes a key event.  Returns false to initiate a clean shutdown.
func (app *App) handleKey(ev *tcell.EventKey) bool {
	app.mu.Lock()
	pane := app.active
	passthrough := false
	if pane != nil {
		pane.mu.Lock()
		if app.keys.Passthrough.Matches(ev) {
			pane.passthrough = !pane.passthrough
		}
		passthrough = pane.passthrough
		pane.mu.Unlock()
	}
	app.mu.Unlock()
	if app.keys.Passthrough.Matches(ev) {
		if app.searchMode {
			app.exitSearch()
		}
		L.Debug("handleKey: passthrough", "enabled", passthrough)
		app.triggerRedraw()
		return true
	}
	if passthrough {
		app.forwardKey(ev)
		return true
	}
	if app.searchMode {
		return app.handleSearchKey(ev)
	}

	kb := &app.keys

	// Copy key: copy selection if active, otherwise forward raw bytes to PTY.
	if kb.Copy.Matches(ev) {
		app.mu.Lock()
		active := app.active
		app.mu.Unlock()
		if active != nil {
			active.mu.Lock()
			text := active.selText()
			if text != "" {
				active.selActive = false
			}
			active.mu.Unlock()
			if text != "" {
				app.copyToClipboard(text)
				active.SetStatus("COPIED", 3*time.Second)
				app.triggerRedraw()
				// Schedule a redraw after the badge expires so it clears
				// even if the terminal is idle.
				go func() {
					time.Sleep(3 * time.Second)
					app.triggerRedraw()
				}()
				return true
			}
		}
		// No selection - fall through so the key is forwarded to the PTY.
	}

	switch {
	case kb.Split.Matches(ev):
		L.Debug("handleKey: split", "key", kb.Split)
		app.zoomOut() // unzoom before splitting
		app.splitActive(false)
		return true
	case kb.SplitContext.Matches(ev):
		L.Debug("handleKey: split into context", "key", kb.SplitContext)
		app.zoomOut()
		app.splitActive(true)
		return true
	case kb.Quit.Matches(ev):
		L.Debug("handleKey: quit", "key", kb.Quit)
		return false
	case kb.ReinitHost.Matches(ev) && app.screen != nil: // a GUI has no host terminal to reset
		L.Debug("handleKey: re-initialise host terminal", "key", kb.ReinitHost)
		app.reinitHost()
		return true
	case kb.Search.Matches(ev):
		L.Debug("handleKey: enter search", "key", kb.Search)
		app.enterSearch()
		return true
	case kb.Zoom.Matches(ev):
		L.Debug("handleKey: zoom toggle", "key", kb.Zoom)
		app.zoomToggle()
		return true
	case kb.Paste.Matches(ev):
		L.Debug("handleKey: paste", "key", kb.Paste)
		if app.pasteFromClipboard() {
			return true
		}
		// Empty clipboard — fall through so the key reaches the child app.
	case kb.NavUp.Matches(ev):
		L.Debug("handleKey: navigate up")
		app.zoomOut()
		app.navigate(dirUp)
		return true
	case kb.NavDown.Matches(ev):
		L.Debug("handleKey: navigate down")
		app.zoomOut()
		app.navigate(dirDown)
		return true
	case kb.NavLeft.Matches(ev):
		L.Debug("handleKey: navigate left")
		app.zoomOut()
		app.navigate(dirLeft)
		return true
	case kb.NavRight.Matches(ev):
		L.Debug("handleKey: navigate right")
		app.zoomOut()
		app.navigate(dirRight)
		return true
	}

	// Scrollback keys.
	app.mu.Lock()
	active := app.active
	h := 0
	if active != nil {
		h = active.h
	}
	app.mu.Unlock()
	switch {
	case kb.ScrollUp.Matches(ev):
		if active != nil {
			L.Debug("handleKey: scrollback up", "pane", active.id, "amount", max(1, h/2))
			active.scrollUp(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	case kb.ScrollDown.Matches(ev):
		if active != nil {
			L.Debug("handleKey: scrollback down", "pane", active.id, "amount", max(1, h/2))
			active.scrollDown(max(1, h/2))
			app.triggerRedraw()
		}
		return true
	}

	app.forwardKey(ev)
	return true
}

func (app *App) forwardKey(ev *tcell.EventKey) {
	app.mu.Lock()
	active := app.active
	app.mu.Unlock()
	if active != nil && !active.isDead() {
		needRedraw := false
		if active.inScrollback() {
			L.Debug("handleKey: leaving scrollback on keypress", "pane", active.id)
			active.scrollReset()
			needRedraw = true
		}
		active.mu.Lock()
		if active.selActive {
			active.selActive = false
			needRedraw = true
		}
		active.mu.Unlock()
		if needRedraw {
			app.triggerRedraw()
		}
		kittyFlags := 0
		active.mu.Lock()
		if len(active.kittyStack) > 0 {
			kittyFlags = active.kittyStack[len(active.kittyStack)-1]
		}
		mode := active.term.Mode()
		active.mu.Unlock()
		if data := keyToBytesMode(ev, kittyFlags, mode); len(data) > 0 {
			L.Debug("handleKey: forwarding to PTY", "pane", active.id, "bytes", len(data))
			active.writeInput(data)
		}
	}
}

// ---------------------------------------------------------------------------
// Pane management
// ---------------------------------------------------------------------------

// splitActive splits the active pane. With inheritContext the new pane
// enters the same container, SSH host, or sudo session. Finding that can run
// podman or docker, which may take seconds, so it runs off the UI thread and
// the split happens when the answer arrives.
func (app *App) splitActive(inheritContext bool) {
	app.mu.Lock()
	src := app.active
	app.mu.Unlock()
	if src == nil {
		L.Debug("splitActive: no active pane, skipping")
		return
	}
	if !inheritContext {
		app.splitPane(src, nil)
		return
	}
	c := src.splitContext()
	app.offUIThread(func() {
		args := resolveSplitArgs(c)
		app.onUIThread(func() { app.splitPane(src, args) })
	})
}

// paneAlert forwards a pane's alert to the frontend, if it wants them.
func (app *App) paneAlert(p *Pane, a paneAlert) {
	if app.onAlert != nil {
		app.onAlert(p, a)
	}
}

// offUIThread runs slow work (external commands) where it cannot freeze the
// UI; it pairs with onUIThread to hand the result back.
func (app *App) offUIThread(work func()) {
	if app.post == nil {
		work()
		return
	}
	go work()
}

// onUIThread runs f on the UI thread.
func (app *App) onUIThread(f func()) {
	if app.post == nil {
		f()
		return
	}
	app.post(f)
}

// uiFuncEvent carries a function to the TUI event loop.
type uiFuncEvent struct {
	tcell.EventTime
	f func()
}

// splitContext is what a context split needs to know about its pane, read
// once under the pane's lock.
type splitContext struct {
	pane          int
	cid, ct       string // container ID (or image name) and type
	fgProc, shell string
	shellPid      int
}

func (p *Pane) splitContext() splitContext {
	p.mu.Lock()
	c := splitContext{pane: p.id, cid: p.containerID, ct: p.containerType, fgProc: p.fgProcess}
	if p.cmd.Process != nil {
		c.shellPid = p.cmd.Process.Pid
	}
	p.mu.Unlock()
	c.shell = os.Getenv("SHELL")
	if c.shell == "" {
		c.shell = "/bin/sh"
	}
	return c
}

// resolveSplitArgs returns the command that enters c's context, or nil for
// the host shell. It may run podman or docker and read /proc.
func resolveSplitArgs(c splitContext) []string {
	L.Debug("splitActive: context inherit requested",
		"pane", c.pane, "fgProc", c.fgProc, "shellPid", c.shellPid,
		"containerType", c.ct, "containerID", c.cid)
	var spawnArgs []string
	switch {
	case (c.ct == "lxc" || c.ct == "incus") && c.cid != "" && c.shellPid > 0:
		// `lxc exec` is the foreground process — reuse its exact cmdline
		// so that `lxc exec xx -- su --login jsn` is preserved verbatim.
		if args := fgProcessCmdline(c.shellPid); len(args) > 0 {
			spawnArgs = args
			L.Debug("splitActive: cloning lxc/incus exec cmdline", "spawnArgs", spawnArgs)
		} else if isRunningContainer(c.cid, c.ct) {
			spawnArgs = containerSpawnArgs(c.cid, c.ct, c.shell)
			L.Debug("splitActive: lxc/incus fallback to containerSpawnArgs", "spawnArgs", spawnArgs)
		}
	case c.ct == "lxd" && c.shellPid > 0:
		// "lxd" means we're inside an LXC container as seen from the host.
		// Walk the process tree up from the foreground to find the
		// `lxc exec` ancestor and reuse its full cmdline — this preserves
		// any `su --login <user>` or other entry command.
		fgPid := termFgPGID(c.shellPid)
		if fgPid <= 0 {
			fgPid = c.shellPid
		}
		if args := findLXCAncestorCmdline(fgPid); len(args) > 0 {
			spawnArgs = args
			L.Debug("splitActive: cloning lxc exec ancestor", "spawnArgs", spawnArgs)
		} else {
			L.Debug("splitActive: lxd container but no lxc ancestor found (bunk is inside container), using host shell")
		}
	case c.cid != "" && c.ct != "" && isRunningContainer(c.cid, c.ct):
		spawnArgs = containerSpawnArgs(c.cid, c.ct, c.shell)
		L.Debug("splitActive: using container context", "spawnArgs", spawnArgs)
	case (c.ct == "podman" || c.ct == "docker") && c.shellPid > 0:
		// "podman run <image>" — cid is an image name, not a running
		// container ID.  Try two strategies:
		// 1. Walk child process tree looking for libpod cgroup entry.
		// 2. Ask podman/docker to list containers with the ancestor image.
		fgPid := termFgPGID(c.shellPid)
		if fgPid <= 0 {
			fgPid = c.shellPid
		}
		containerID := findRunningContainerFromPID(fgPid)
		L.Debug("splitActive: process-tree walk result", "containerID", containerID, "fgPid", fgPid)
		if containerID == "" && c.cid != "" {
			containerID = findContainerByAncestor(c.cid, c.ct)
			L.Debug("splitActive: ancestor filter result", "containerID", containerID, "image", c.cid)
		}
		if containerID != "" {
			spawnArgs = []string{c.ct, "exec", "-it", containerID, c.shell}
			L.Debug("splitActive: resolved podman/docker run container", "containerID", containerID, "spawnArgs", spawnArgs)
		} else {
			L.Debug("splitActive: podman/docker run but could not resolve container ID", "fgPid", fgPid, "image", c.cid)
		}
	case (c.fgProc == "ssh" || c.fgProc == "mosh") && c.shellPid > 0:
		if args := fgProcessCmdline(c.shellPid); len(args) > 0 {
			spawnArgs = args
			L.Debug("splitActive: cloning ssh/mosh session", "spawnArgs", spawnArgs)
		} else {
			L.Debug("splitActive: ssh/mosh detected but cmdline empty", "shellPid", c.shellPid)
		}
	case c.fgProc == "sudo" && c.shellPid > 0:
		if args := fgProcessCmdline(c.shellPid); len(args) > 0 {
			spawnArgs = args
			L.Debug("splitActive: cloning sudo session", "spawnArgs", spawnArgs)
		} else {
			L.Debug("splitActive: sudo detected but cmdline empty", "shellPid", c.shellPid)
		}
	default:
		L.Debug("splitActive: no context to inherit, using host shell")
	}
	return spawnArgs
}

// splitPane splits src, if it is still open, running spawnArgs in the new
// pane (nil: the host shell).
func (app *App) splitPane(src *Pane, spawnArgs []string) {
	app.mu.Lock()
	defer app.mu.Unlock()

	if app.root == nil {
		return
	}
	node := app.root.findPane(src)
	if node == nil {
		L.Debug("splitActive: pane closed before the split", "pane", src.id)
		return
	}

	var d splitDir
	// Compare pane dimensions in pixels using the measured cell aspect ratio
	// so the split direction matches what the user actually sees.
	// cellAspect = cellPixelH / cellPixelW; pane is wider-in-pixels when
	// node.w * cellW > node.h * cellH  →  node.w > node.h * cellAspect.
	pixelW := float64(node.w)
	pixelH := float64(node.h) * app.cellAspect
	if pixelW >= pixelH+5 { // if almost square, prefer horizontal split to avoid very narrow panes
		d = splitVertical
	} else {
		d = splitHorizontal
	}
	L.Debug("splitActive: direction decision",
		"pane", src.id,
		"node_cols", node.w, "node_rows", node.h,
		"cell_aspect", app.cellAspect,
		"pixel_w", int(pixelW), "pixel_h", int(pixelH),
		"wider_in_pixels", pixelW >= pixelH,
		"dir", map[splitDir]string{splitVertical: "vertical (side-by-side)", splitHorizontal: "horizontal (top-bottom)"}[d],
	)

	var lw, lh, rx, ry, nw, nh int
	if d == splitVertical {
		half := node.w / 2
		lw, lh = half, node.h
		rx, ry = node.x+half+1, node.y
		nw, nh = node.w-half-1, node.h
	} else {
		half := node.h / 2
		lw, lh = node.w, half
		rx, ry = node.x, node.y+half+1
		nw, nh = node.w, node.h-half-1
	}
	if lw < 4 || lh < 2 || nw < 4 || nh < 2 {
		L.Debug("splitActive: panes too small to split", "lw", lw, "lh", lh, "nw", nw, "nh", nh)
		return
	}
	nx, ny := rx, ry

	// Inherit the working directory from the pane's foreground process.
	dir := src.cwd()
	c := src.splitContext()

	newPane, err := NewPane(
		app.nextID, nx, ny, nw, nh, app.scrollback, app.scrollbackBytes, dir, spawnArgs,
		app.paneOSCColors(),
		app.redraw, app.paneDead, app.done, app.oscBuf,
		app.cellAspect,
	)
	if err != nil {
		L.Error("splitActive: NewPane", "err", err, "dir", dir, "spawnArgs", spawnArgs, "container", c.ct, "containerID", c.cid)
		return
	}
	L.Debug("splitActive: new pane created", "new_pane", newPane.id, "x", nx, "y", ny, "w", nw, "h", nh)
	newPane.setAlertHook(app.paneAlert)
	app.nextID++

	node.split(newPane, d)
	app.active = newPane
	app.triggerRedraw()
}

// ---------------------------------------------------------------------------
// Zoom (fullscreen toggle)
// ---------------------------------------------------------------------------

// zoomToggle toggles fullscreen zoom on the active pane.
func (app *App) zoomToggle() {
	app.mu.Lock()
	if app.zoomedPane != nil {
		app.mu.Unlock()
		app.zoomOut()
	} else {
		app.mu.Unlock()
		app.zoomIn()
	}
}

// zoomIn maximises the active pane to fill the entire screen.
// Must NOT hold app.mu on entry (acquires it internally).
func (app *App) zoomIn() {
	app.mu.Lock()
	defer app.mu.Unlock()

	p := app.active
	if p == nil || app.root == nil {
		return
	}
	// Only one pane → nothing to zoom.
	if app.root.isLeaf() {
		return
	}

	app.zoomedPane = p
	app.zoomGeom = [4]int{p.x, p.y, p.w, p.h}

	w, h := app.viewSize()
	L.Debug("zoomIn", "pane", p.id, "screen", w, "x", h)
	p.resize(0, 0, w, h)
	app.triggerRedraw()
}

// zoomOut restores the zoomed pane to its original geometry.
// Safe to call when not zoomed (no-op).
// Must NOT hold app.mu on entry (acquires it internally).
func (app *App) zoomOut() {
	app.mu.Lock()
	defer app.mu.Unlock()

	p := app.zoomedPane
	if p == nil {
		return
	}
	app.zoomedPane = nil

	g := app.zoomGeom
	L.Debug("zoomOut", "pane", p.id, "restore", g)
	p.resize(g[0], g[1], g[2], g[3])

	// While zoomed, the zoomed pane's frame filled the entire tcell cell
	// buffer, so every other pane's screen region now holds stale zoomed
	// content.  Those panes have no vt10x dirty rows (nothing wrote to
	// them), so renderPane would skip them — force one full repaint.
	if app.root != nil {
		for _, leaf := range app.root.leaves() {
			leaf.pane.markFullRepaint()
		}
	}
	app.triggerRedraw()
}

// navigate moves focus to the nearest pane in direction d.
func (app *App) navigate(d dir) {
	app.mu.Lock()

	var prev, best *Pane
	if app.root != nil && app.active != nil {
		leaves := app.root.leaves()
		if len(leaves) >= 2 {
			ax := float64(app.active.x) + float64(app.active.w)/2
			ay := float64(app.active.y) + float64(app.active.h)/2
			bestDist := math.MaxFloat64

			for _, leaf := range leaves {
				p := leaf.pane
				if p == app.active || p.isDead() {
					continue
				}
				cx := float64(p.x) + float64(p.w)/2
				cy := float64(p.y) + float64(p.h)/2

				switch d {
				case dirRight:
					if cx <= ax {
						continue
					}
				case dirLeft:
					if cx >= ax {
						continue
					}
				case dirDown:
					if cy <= ay {
						continue
					}
				case dirUp:
					if cy >= ay {
						continue
					}
				}

				if dist := math.Hypot(cx-ax, cy-ay); dist < bestDist {
					bestDist = dist
					best = p
				}
			}
		}
	}

	if best != nil {
		prev = app.active
		L.Debug("navigate: focus change", "dir", d, "from_pane", prev.id, "to_pane", best.id)
		app.active = best
		app.triggerRedraw()
	} else {
		L.Debug("navigate: no target found", "dir", d)
	}
	app.mu.Unlock()

	if best != nil {
		sendFocusOut(prev)
		sendFocusIn(best)
	}
}

// deathWatcher waits for dead-pane notifications and cleans them up.
func (app *App) deathWatcher() {
	for {
		select {
		case p := <-app.paneDead:
			app.removePane(p)
		case <-app.done:
			return
		}
	}
}

// removePane removes a dead pane from the layout and re-focuses if needed.
func (app *App) removePane(p *Pane) {
	L.Debug("removePane: removing pane", "pane", p.id)
	app.mu.Lock()

	if app.root == nil {
		app.mu.Unlock()
		return
	}

	// If the zoomed pane died, unzoom (no geometry restore needed — it's gone).
	wasZoomed := app.zoomedPane == p
	if wasZoomed {
		app.zoomedPane = nil
	}

	var newActive *Pane
	if app.active == p {
		newActive = bestFocusAfterRemove(app.root, p)
		L.Debug("removePane: refocusing", "from_pane", p.id, "to_pane", func() int {
			if newActive != nil {
				return newActive.id
			}
			return -1
		}())
		app.active = newActive
	}

	app.root = removeFromTree(app.root, p)
	shutdown := app.root == nil
	if wasZoomed && !shutdown {
		// Same staleness as zoomOut: the dead pane's fullscreen frame is
		// still in tcell's cell buffer over every surviving pane.
		for _, leaf := range app.root.leaves() {
			leaf.pane.markFullRepaint()
		}
	}
	app.mu.Unlock()

	if shutdown {
		if app.onEmpty != nil {
			L.Info("removePane: last pane removed")
			app.onEmpty()
			return
		}
		L.Info("removePane: last pane removed, shutting down")
		go app.shutdown()
		return
	}

	if newActive != nil {
		sendFocusIn(newActive)
	}

	app.triggerRedraw()
}

func bestFocusAfterRemove(root *Node, dying *Pane) *Pane {
	node := root.findPane(dying)
	if node == nil {
		return nil
	}
	cx := float64(dying.x) + float64(dying.w)/2
	cy := float64(dying.y) + float64(dying.h)/2

	if node.parent != nil {
		sibling := node.parent.right
		if node.parent.right == node {
			sibling = node.parent.left
		}
		if best := closestLivePane(sibling, cx, cy, dying); best != nil {
			return best
		}
	}
	return closestLivePane(root, cx, cy, dying)
}

func closestLivePane(n *Node, cx, cy float64, exclude *Pane) *Pane {
	var best *Pane
	bestDist := math.MaxFloat64
	for _, leaf := range n.leaves() {
		p := leaf.pane
		if p == exclude || p.isDead() {
			continue
		}
		px := float64(p.x) + float64(p.w)/2
		py := float64(p.y) + float64(p.h)/2
		if d := math.Hypot(px-cx, py-cy); d < bestDist {
			bestDist = d
			best = p
		}
	}
	return best
}

// ---------------------------------------------------------------------------
// Navigation direction type
// ---------------------------------------------------------------------------

type dir int

const (
	dirUp dir = iota
	dirDown
	dirLeft
	dirRight
)

// ---------------------------------------------------------------------------
// Key → PTY byte translation
// ---------------------------------------------------------------------------

func keyToBytes(ev *tcell.EventKey, kittyFlags int) []byte {
	mod := ev.Modifiers()

	// Kitty keyboard protocol: when the child app has pushed disambiguate
	// mode (flags bit 0), encode keys as CSI u sequences so the app can
	// tell Alt+key from ESC+key, Ctrl+I from Tab, etc.
	useCSIu := kittyFlags&1 != 0

	// Kitty modifier parameter: 1 + bitmask (shift=1, alt=2, ctrl=4).
	kittyMod := 1
	if mod&tcell.ModShift != 0 {
		kittyMod += 1
	}
	if mod&tcell.ModAlt != 0 {
		kittyMod += 2
	}
	if mod&tcell.ModCtrl != 0 {
		kittyMod += 4
	}

	if ev.Key() == tcell.KeyRune {
		r := ev.Rune()
		if useCSIu && kittyMod > 1 {
			return fmt.Appendf(nil, "\x1b[%d;%du", r, kittyMod)
		}
		if mod&tcell.ModAlt != 0 {
			return append([]byte{'\x1b'}, []byte(string(r))...)
		}
		return []byte(string(r))
	}

	k := ev.Key()

	// CSI u encoding for special keys whose legacy form is ambiguous.
	if useCSIu {
		cp := 0
		km := kittyMod
		switch k {
		case tcell.KeyEnter:
			cp = 13
		case tcell.KeyTab:
			cp = 9
		case tcell.KeyBacktab:
			cp = 9
			km = 1 + ((km - 1) | 1) // BackTab always implies Shift
		case tcell.KeyBackspace, tcell.KeyBackspace2:
			cp = 127
		case tcell.KeyEsc:
			cp = 27
		}
		if cp > 0 {
			if km > 1 {
				return fmt.Appendf(nil, "\x1b[%d;%du", cp, km)
			}
			return fmt.Appendf(nil, "\x1b[%du", cp)
		}
		// Ctrl+letter: codepoint is the lowercase letter.
		if k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ {
			cp = int('a' + (k - tcell.KeyCtrlA))
			km = 1 + ((km - 1) | 4) // modifier parameters are one-based
			return fmt.Appendf(nil, "\x1b[%d;%du", cp, km)
		}
		// Fall through to legacy for arrows, F-keys, etc. which already
		// have unambiguous CSI representations.
	}

	// Legacy encoding.
	if mod&tcell.ModAlt != 0 && (k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ || k == tcell.KeyBackspace || k == tcell.KeyBackspace2 || k == tcell.KeyEsc || k == tcell.KeyEnter || k == tcell.KeyTab) {
		plain := tcell.NewEventKey(k, ev.Rune(), mod&^tcell.ModAlt)
		return append([]byte{27}, keyToBytes(plain, 0)...)
	}
	if k >= tcell.KeyCtrlA && k <= tcell.KeyCtrlZ {
		return []byte{byte(k-tcell.KeyCtrlA) + 1}
	}

	// xtermMod returns the xterm modifier parameter (1 + shift + alt*2 + ctrl*4).
	// Identical encoding to kittyMod — reuse the already-computed value.
	// When kittyMod == 1 (no modifiers), omit the parameter entirely (bare seq).
	xm := kittyMod

	switch k {
	case tcell.KeyBackspace:
		return []byte{'\x08'}
	case tcell.KeyBackspace2:
		return []byte{'\x7f'}
	case tcell.KeyEnter:
		return []byte{'\r'}
	case tcell.KeyTab:
		return []byte{'\t'}
	case tcell.KeyBacktab: // Shift+Tab
		return []byte{'\x1b', '[', 'Z'}
	case tcell.KeyEsc:
		return []byte{'\x1b'}

	// Arrow keys — bare CSI when unmodified, CSI 1;<mod> when modified.
	// The `return nil` guards that used to be here for Alt+arrows are removed:
	// handleKey already consumed them as keybindings before reaching keyToBytes.
	// Any key that arrives here must be forwarded.
	case tcell.KeyUp:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dA", xm)
		}
		return []byte{'\x1b', '[', 'A'}
	case tcell.KeyDown:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dB", xm)
		}
		return []byte{'\x1b', '[', 'B'}
	case tcell.KeyRight:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dC", xm)
		}
		return []byte{'\x1b', '[', 'C'}
	case tcell.KeyLeft:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dD", xm)
		}
		return []byte{'\x1b', '[', 'D'}

	// Home / End
	case tcell.KeyHome:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dH", xm)
		}
		return []byte{'\x1b', '[', 'H'}
	case tcell.KeyEnd:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dF", xm)
		}
		return []byte{'\x1b', '[', 'F'}

	// PgUp / PgDn / Insert / Delete
	case tcell.KeyPgUp:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[5;%d~", xm)
		}
		return []byte{'\x1b', '[', '5', '~'}
	case tcell.KeyPgDn:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[6;%d~", xm)
		}
		return []byte{'\x1b', '[', '6', '~'}
	case tcell.KeyInsert:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[2;%d~", xm)
		}
		return []byte{'\x1b', '[', '2', '~'}
	case tcell.KeyDelete:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[3;%d~", xm)
		}
		return []byte{'\x1b', '[', '3', '~'}

	// F1–F4: SS3 form when unmodified, CSI 1;<mod> form when modified.
	case tcell.KeyF1:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dP", xm)
		}
		return []byte{'\x1b', 'O', 'P'}
	case tcell.KeyF2:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dQ", xm)
		}
		return []byte{'\x1b', 'O', 'Q'}
	case tcell.KeyF3:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dR", xm)
		}
		return []byte{'\x1b', 'O', 'R'}
	case tcell.KeyF4:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[1;%dS", xm)
		}
		return []byte{'\x1b', 'O', 'S'}

	// F5–F12: tilde form when unmodified, <code>;<mod>~ when modified.
	case tcell.KeyF5:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[15;%d~", xm)
		}
		return []byte{'\x1b', '[', '1', '5', '~'}
	case tcell.KeyF6:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[17;%d~", xm)
		}
		return []byte{'\x1b', '[', '1', '7', '~'}
	case tcell.KeyF7:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[18;%d~", xm)
		}
		return []byte{'\x1b', '[', '1', '8', '~'}
	case tcell.KeyF8:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[19;%d~", xm)
		}
		return []byte{'\x1b', '[', '1', '9', '~'}
	case tcell.KeyF9:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[20;%d~", xm)
		}
		return []byte{'\x1b', '[', '2', '0', '~'}
	case tcell.KeyF10:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[21;%d~", xm)
		}
		return []byte{'\x1b', '[', '2', '1', '~'}
	case tcell.KeyF11:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[23;%d~", xm)
		}
		return []byte{'\x1b', '[', '2', '3', '~'}
	case tcell.KeyF12:
		if xm > 1 {
			return fmt.Appendf(nil, "\x1b[24;%d~", xm)
		}
		return []byte{'\x1b', '[', '2', '4', '~'}

	// F13–F24: these are the standard xterm Shift+F1–F12 encodings.
	// tcell may report Shift+F1 as either KeyF1+ModShift (handled above via
	// the xm>1 path) or as KeyF13 directly, depending on the host terminal's
	// terminfo.  We handle both so apps receive the correct sequence either way.
	case tcell.KeyF13:
		return []byte{'\x1b', '[', '1', ';', '2', 'P'} // Shift+F1
	case tcell.KeyF14:
		return []byte{'\x1b', '[', '1', ';', '2', 'Q'} // Shift+F2
	case tcell.KeyF15:
		return []byte{'\x1b', '[', '1', ';', '2', 'R'} // Shift+F3
	case tcell.KeyF16:
		return []byte{'\x1b', '[', '1', ';', '2', 'S'} // Shift+F4
	case tcell.KeyF17:
		return []byte{'\x1b', '[', '1', '5', ';', '2', '~'} // Shift+F5
	case tcell.KeyF18:
		return []byte{'\x1b', '[', '1', '7', ';', '2', '~'} // Shift+F6
	case tcell.KeyF19:
		return []byte{'\x1b', '[', '1', '8', ';', '2', '~'} // Shift+F7
	case tcell.KeyF20:
		return []byte{'\x1b', '[', '1', '9', ';', '2', '~'} // Shift+F8
	case tcell.KeyF21:
		return []byte{'\x1b', '[', '2', '0', ';', '2', '~'} // Shift+F9
	case tcell.KeyF22:
		return []byte{'\x1b', '[', '2', '1', ';', '2', '~'} // Shift+F10
	case tcell.KeyF23:
		return []byte{'\x1b', '[', '2', '3', ';', '2', '~'} // Shift+F11
	case tcell.KeyF24:
		return []byte{'\x1b', '[', '2', '4', ';', '2', '~'} // Shift+F12
	}
	return nil
}

// tcellColorToXParse converts a tcell.Color to XParseColor format
// "rgb:<rrrr>/<gggg>/<bbbb>" for use in OSC 10/11 terminal colour query
// responses.  Each 8-bit RGB component is doubled to 16-bit (0xd0 → "d0d0").
// Returns "" when the colour is not known as an RGB value (for example the
// `terminal` theme's ColorDefault), so bunk suppresses the reply instead of
// claiming a made-up colour.
func tcellColorToXParse(c tcell.Color) string {
	r, g, b := c.RGB()
	if r < 0 {
		return ""
	}
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", r, r, g, g, b, b)
}
