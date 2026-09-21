// pane.go - PTY spawning and VT100/ANSI emulation bridge.
//
// Each Pane owns:
//   - a PTY master (os.File) connected to a shell subprocess
//   - a vt10x.Terminal: the ANSI/VT100 state machine that parses raw PTY bytes
//     and maintains a virtual 2-D cell grid
//
// The "bridge" is the readPTY goroutine: it reads raw bytes from the PTY master
// and feeds them into term.Write().  The render goroutine then reads the cell
// grid via term.Cell(col, row) and paints it onto the tcell screen.
// Pane.mu serialises those two concurrent accesses to the terminal state.
package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode/utf8"

	"bunk/internal/vt10x"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

// defaultScrollbackLines is the default maximum number of scrollback lines
// retained per pane.  Overridden by the "scrollback" config field.
// Memory cost per pane:
//   - rawBuf:  scrollbackLines × ~200 bytes/line (raw ANSI, varies by content)
//   - sbRing:  scrollbackLines × cols × ~24 bytes (rendered glyphs, on demand)
const defaultScrollbackLines = 10_000

// selPos identifies a cell in the pane's unified virtual coordinate space.
//
//	row 0 … sb.count-1  → scrollback ring entries (oldest … newest)
//	row sb.count + r    → live terminal row r
//
// This coordinate is stable across scrolls: as a line scrolls off the live
// view into the ring, its virtual row index does not change.
type selPos struct {
	row, col int
}

// Pane represents one terminal pane: a shell process attached to a PTY,
// plus a virtual terminal state machine that tracks what should be displayed.
type Pane struct {
	id   int
	x, y int // top-left corner on the host screen (content area, 0-indexed)
	w, h int // width and height of the content area in cells

	ptmx     *os.File  // PTY master - write keystrokes here, read shell output here
	ptmxOnce sync.Once // ensures PTY master fd is closed exactly once
	cmd      *exec.Cmd // the shell process

	// mu serialises all access to term (both writes from readPTY and reads from
	// the render goroutine), plus the dead flag.
	mu   sync.Mutex
	term vt10x.Terminal // VT100/ANSI state machine — also tracks bracketed paste,
	// sync update, cursor shape, and all DEC private modes.
	dead bool // true once the shell process has exited

	// Scrollback buffer - lines that have scrolled off the vt10x grid top.
	// Protected by mu.
	sb              sbRing // ring buffer of captured rows
	sbOff           int    // 0 = live view; N = N lines above live view
	scrollbackLines int    // max scrollback rows (from config)

	// Text selection state.  Protected by mu.
	// selAnchor is where Button1 was pressed; selCursor tracks the drag endpoint.
	// Both are in virtual (scrollback+live) row/col coordinates.
	selAnchor, selCursor selPos
	selActive            bool

	// searchHL holds span-based match highlights for this pane.
	// nil when no search is active.  Protected by mu.
	searchHL    *searchHighlight
	searchHLGen int // incremented on every searchHL assignment

	// Dirty-render tracking: renderPane compares these against the current
	// overlay state to decide between full repaint and dirty-row-only repaint.
	// All fields protected by mu.
	lastRenderSbOff       int
	lastRenderSelActive   bool
	lastRenderSelAnchor   selPos
	lastRenderSelCursor   selPos
	lastRenderSearchHLGen int
	lastRenderStatusKey   string
	lastRenderColorGen    uint64

	// forceFullRepaint makes the next renderPane call repaint every row
	// regardless of vt10x dirty state.  Set when tcell's cell buffer no
	// longer reflects this pane's content — e.g. after another pane was
	// zoomed fullscreen on top of it.  Protected by mu.
	forceFullRepaint bool

	// oscScan is the per-pane OSC pre-scanner (value, no alloc).
	// Forwards OSC 7 (CWD), OSC 8 (hyperlinks), OSC 52 (clipboard) to the
	// host terminal so those features work through the multiplexer.
	oscScan oscScanner

	// Process and container tracking.  All protected by mu.
	fgProcess     string // name of the current foreground process (e.g. "ssh", "sudo")
	containerID   string // active container name (updated live by trackFgProcess)
	containerType string // "toolbox", "distrobox", "podman", "lxc", or ""
	sshHost       string // remote hostname when fgProcess is "ssh" or "mosh"

	// baseContainerType/ID are set once at startup and represent the static
	// container context of the pane itself (e.g. bunk running inside a
	// Toolbox container).  They are used as fallback when the foreground
	// process is not inside any container.  Protected by mu.
	baseContainerType string
	baseContainerID   string

	// rawBuf stores the raw PTY byte stream (capped at rawBufMax) so that the
	// terminal content can be replayed into a fresh vt10x on resize, giving
	// correct line-wrap reflow at the new column width.  Protected by mu.
	rawBuf      []byte
	hasGraphics bool

	// altEntryCursor remembers the primary-screen cursor position at the moment
	// the pane entered the alternate screen.  vt10x shares a single saved-cursor
	// slot for both ESC-7/8 (DECSC/DECRC) and \x1b[?1049h/l, so the primary
	// cursor is not reliably preserved across an alt-screen round-trip.
	// We restore it manually after \x1b[?1049l so programs like gh-copilot
	// (which render inline at the current cursor position) start in the right
	// place.  Protected by mu.
	altEntryCursorX, altEntryCursorY int

	// needsSync is set when the pane exits the alternate screen so that the
	// next render call uses screen.Sync() (full repaint) instead of
	// screen.Show() (differential).  This clears any residual background
	// colours that alt-screen apps like btop may have left in the terminal.
	// Protected by mu.
	needsSync bool

	// transientLineClear is set after a CR-space-CR clear-only progress chunk.
	// The vt10x state is temporarily blank, but a replacement chunk usually
	// follows immediately. Render skips frames while this is set so it doesn't
	// sample and paint the transient blank row.
	// Protected by mu.
	transientLineClear      bool
	transientLineClearUntil time.Time
	syncDeadline            time.Time

	// progressCursorHiddenUntil suppresses the host cursor during rapid
	// carriage-return status/progress rewrites. Some CLI progress bars do not
	// hide the cursor themselves; showing a block cursor while the row is
	// rewritten hundreds of times per second reads as flicker.
	// Protected by mu.
	progressCursorHiddenUntil time.Time

	// kittyStack is the per-pane kitty keyboard protocol flag stack.
	// When pane apps send \x1b[=<flags>u (push), we push onto this stack
	// and respond with the saved value on \x1b[?u (query).
	// On \x1b[<N>u (pop) we pop N levels.
	// All kitty keyboard sequences are stripped before vt10x sees them —
	// vt10x misinterprets the 'u' final byte as DECRC (restore cursor).
	// Protected by mu.
	kittyStack     []int
	kittyOwnerPGID int

	passthrough bool // protected by mu

	// Temporary status message (e.g. "COPIED") shown in the status bar.
	// Clears automatically after statusMsgEnd.  Protected by mu.
	statusMsg    string
	statusMsgEnd time.Time

	// themeFG/BG/Cursor are the default colours bunk exposes to pane apps in
	// XParseColor format ("rgb:<rrrr>/<gggg>/<bbbb>") when no dynamic OSC
	// override is active.  Empty means "unknown" and suppresses the reply.
	themeFGColor     string
	themeBGColor     string
	themeCursorColor string
}

// NewPane spawns a shell inside a new PTY with the given geometry, starts the
// VT10x emulator, and launches the background I/O goroutines.
//
//	spawnArgs - argv for the child process; nil or empty means use $SHELL.
//	            Pass containerSpawnArgs(...) here to open the new pane inside
//	            the same container as the pane that was split.
//	redraw    - signalled after each chunk of PTY output
//	paneDead  - receives p when the shell exits
//	done      - closed by the app on shutdown
//	colors    - default OSC 10/11/12 colours for the pane (from theme or host probe)
//	oscBuf    - receives OSC 7/8/52/133 sequences to forward to the host terminal
func NewPane(id, x, y, w, h, scrollback int, dir string, spawnArgs []string, colors hostOSCColors, redraw chan struct{}, paneDead chan *Pane, done chan struct{}, oscBuf *oscBuffer, cellAspect ...float64) (*Pane, error) {
	if w < 2 || h < 1 {
		return nil, fmt.Errorf("pane too small: %dx%d", w, h)
	}
	shell := os.Getenv("SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	if len(spawnArgs) == 0 {
		spawnArgs = []string{shell}
	}

	cmd := exec.Command(spawnArgs[0], spawnArgs[1:]...)
	if dir != "" {
		cmd.Dir = dir
	}

	// Build the child environment.
	// Filter out TERM and COLORTERM from the host before setting our own.
	// (Simply appending would not override them on most shells/kernels.)
	// • TERM=xterm-256color - the emulation profile we advertise.
	// • COLORTERM=truecolor - tells colour-aware apps 24-bit RGB works.
	filtered := make([]string, 0, len(os.Environ()))
	for _, e := range os.Environ() {
		if !strings.HasPrefix(e, "TERM=") && !strings.HasPrefix(e, "COLORTERM=") &&
			!strings.HasPrefix(e, "VTE_VERSION=") {
			filtered = append(filtered, e)
		}
	}
	// BUNK=1 lets shell rc files detect they're running inside bunk and skip
	// auto-launching it again (prevents recursive invocation).
	// VTE_VERSION tells apps like Neovim that we support VTE 0.78+ features:
	// SGR 4:3 curly underline, SGR 58 colored underline, kitty keyboard protocol.
	cmd.Env = append(filtered, "TERM="+paneTermName, "COLORTERM=truecolor", "BUNK=1", "VTE_VERSION=8203")

	cellWidth, cellHeight := virtualCellPixels(cellAspect)
	ptmx, err := pty.StartWithSize(cmd, paneWinsize(w-1, h, cellWidth, cellHeight))
	if err != nil {
		return nil, fmt.Errorf("pty.StartWithSize: %w", err)
	}

	// Ordinary device queries are answered by captureAndWrite using pane
	// state and theme colours. Graphics acknowledgements are emitted by the
	// decoder, independently, so requests are answered exactly once.
	// Create the pane first so we can pass p.onScrollRow as the scroll
	// callback.  p.term is set immediately after; the callback is only
	// invoked from readPTY (started below), so p.term is always valid
	// by the time the callback could fire.
	p := &Pane{
		id: id, x: x, y: y, w: w, h: h,
		ptmx:             ptmx,
		cmd:              cmd,
		scrollbackLines:  scrollback,
		sb:               sbRing{maxLines: scrollback},
		themeFGColor:     colors.fg,
		themeBGColor:     colors.bg,
		themeCursorColor: colors.cursor,
	}
	p.term = vt10x.New(vt10x.WithSize(w-1, h), vt10x.WithScrollCallback(p.onScrollRow),
		vt10x.WithScrollbackClearCallback(p.onScrollbackClear), vt10x.WithGraphicsReply(p.writeInput), vt10x.WithCellPixels(cellWidth, cellHeight))

	// One-time container detection: read the shell process's own environ.
	if cmd.Process != nil {
		if ct := detectContainerFromPID(cmd.Process.Pid); ct != "" {
			p.containerType = ct
			p.baseContainerType = ct
			if ct == "lxc" {
				name := lxdContainerName()
				p.containerID = name
				p.baseContainerID = name
			}
			L.Debug("pane: container detected", "id", p.id, "type", p.containerType, "name", p.containerID)
		}
	}

	L.Debug("pane spawned", "id", p.id, "x", x, "y", y, "w", w, "h", h)

	go p.readPTY(redraw, oscBuf)      // VT100 parsing bridge (write side)
	go p.waitForExit(paneDead, done)  // monitors shell lifecycle
	go p.trackFgProcess(redraw, done) // polls foreground process name

	return p, nil
}

// readPTY is the VT100 parsing bridge (write side).
//
// For each chunk of raw PTY bytes it:
//  1. Runs the oscScanner to extract and forward OSC 7/8/52 sequences.
//  2. Pre-scans for DECSET 2004 (bracketed paste enable/disable).
//  3. Captures rows that are about to scroll off the top (scrollback).
//  4. Feeds the bytes into vt10x.
//  5. Signals the render loop to repaint.
func (p *Pane) readPTY(redraw chan struct{}, oscBuf *oscBuffer) {
	buf := make([]byte, 32768)
	var stream ptyStream
	var transientClearTimer *time.Timer
	var progressCursorTimer *time.Timer
	var syncTimer *time.Timer
	defer func() {
		if transientClearTimer != nil {
			transientClearTimer.Stop()
		}
		if progressCursorTimer != nil {
			progressCursorTimer.Stop()
		}
		if syncTimer != nil {
			syncTimer.Stop()
		}
	}()

	scheduleTransientClearRedraw := func(until time.Time) {
		if transientClearTimer != nil {
			transientClearTimer.Stop()
		}
		transientClearTimer = time.AfterFunc(transientLineClearDelay, func() {
			p.clearTransientLineClearIfUntil(until)
			select {
			case redraw <- struct{}{}:
			default:
			}
		})
	}
	cancelTransientClearRedraw := func() {
		if transientClearTimer != nil {
			transientClearTimer.Stop()
		}
	}
	scheduleProgressCursorRedraw := func() {
		if progressCursorTimer == nil {
			progressCursorTimer = time.AfterFunc(progressCursorHideDelay, func() {
				select {
				case redraw <- struct{}{}:
				default:
				}
			})
			return
		}
		progressCursorTimer.Reset(progressCursorHideDelay)
	}

	for {
		n, err := p.ptmx.Read(buf)
		if n > 0 {
			chunk := buf[:n]

			chunk = stream.scan(chunk)
			if len(chunk) == 0 {
				if err != nil {
					L.Debug("readPTY: PTY read error (shell exited)", "pane", p.id, "err", err)
					break
				}
				continue
			}

			L.Log(context.Background(), LevelTrace, "readPTY: chunk", "pane", p.id, "data", fmt.Sprintf("%q", chunk))

			// Step 1 - OSC passthrough (CWD, clipboard, prompt markers).
			p.oscScan.Scan(chunk, func(seq []byte) {
				oscBuf.append(seq)
				mirrorOSC52ToNativeClipboard(seq)
			})

			transientClear := isTransientLineClear(chunk)
			inPlaceLineUpdate := isInPlaceLineUpdate(chunk)

			// Steps 2–4 under a single lock: captureAndWrite feeds chunk into
			// vt10x, which updates all mode flags (2004, 2026, cursor shape, etc.)
			// atomically before render can read them.
			now := time.Now()
			p.mu.Lock()
			p.captureAndWrite(chunk)
			scrolling := p.sbOff > 0
			syncing := p.term.Mode()&vt10x.ModeSync != 0
			if syncing && p.syncDeadline.IsZero() {
				until := now.Add(syncUpdateTimeout)
				p.syncDeadline = until
				syncTimer = time.AfterFunc(syncUpdateTimeout, func() {
					p.expireSyncUpdate(until)
					select {
					case redraw <- struct{}{}:
					default:
					}
				})
			} else if !syncing {
				p.syncDeadline = time.Time{}
				if syncTimer != nil {
					syncTimer.Stop()
				}
			}
			visibleTransientClear := transientClear && !scrolling && !syncing
			p.transientLineClear = visibleTransientClear
			transientClearUntil := time.Time{}
			if visibleTransientClear {
				transientClearUntil = now.Add(transientLineClearDelay)
			}
			p.transientLineClearUntil = transientClearUntil
			visibleInPlaceLineUpdate := inPlaceLineUpdate && !scrolling && !syncing
			if visibleInPlaceLineUpdate {
				p.progressCursorHiddenUntil = now.Add(progressCursorHideDelay)
			} else if bytes.Contains(chunk, []byte("\n")) {
				p.progressCursorHiddenUntil = time.Time{}
			}
			p.mu.Unlock()

			// Step 5 - wake the render loop (coalesced).
			// Skip when the user is reading scrollback: new output is buffered
			// silently so the visible view doesn't jump while they are reading.
			// Skip during synchronized updates (DEC 2026): the app is
			// building a frame — repaint only when the end marker arrives.
			if !scrolling && !syncing {
				if visibleTransientClear {
					scheduleTransientClearRedraw(transientClearUntil)
				} else {
					cancelTransientClearRedraw()
					if visibleInPlaceLineUpdate {
						scheduleProgressCursorRedraw()
					}
					select {
					case redraw <- struct{}{}:
					default:
					}
				}
			}
		}
		if err != nil {
			L.Debug("readPTY: PTY read error (shell exited)", "pane", p.id, "err", err)
			break
		}
	}
	p.closePTX()
}

func (p *Pane) clearTransientLineClearIfUntil(until time.Time) {
	p.mu.Lock()
	if p.transientLineClearUntil.Equal(until) {
		p.transientLineClear = false
		p.transientLineClearUntil = time.Time{}
	}
	p.mu.Unlock()
}

const transientLineClearDelay = 40 * time.Millisecond
const progressCursorHideDelay = 120 * time.Millisecond
const syncUpdateTimeout = time.Second

func (p *Pane) expireSyncUpdate(until time.Time) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.syncDeadline.Equal(until) {
		if _, err := p.term.Write([]byte("\x1b[?2026l")); err != nil {
			L.Warn("expire synchronized update", "pane", p.id, "err", err)
		}
		p.syncDeadline = time.Time{}
	}
}

func isTransientLineClear(chunk []byte) bool {
	if len(chunk) < 3 || bytes.ContainsAny(chunk, "\n\x1b") {
		return false
	}

	sawClear := false
	for i := 0; i < len(chunk); {
		if chunk[i] != '\r' {
			return false
		}
		i++

		spaces := 0
		for i < len(chunk) && chunk[i] == ' ' {
			i++
			spaces++
		}
		if spaces == 0 || i >= len(chunk) || chunk[i] != '\r' {
			return false
		}
		i++
		sawClear = true
	}
	return sawClear
}

func isInPlaceLineUpdate(chunk []byte) bool {
	return len(chunk) > 0 &&
		bytes.Contains(chunk, []byte("\r")) &&
		!bytes.ContainsAny(chunk, "\n\x1b")
}

// onScrollRow is the vt10x scroll callback installed in NewPane via
// WithScrollCallback.  It is called synchronously inside vt10x.Write()
// for each row that scrolls off the top of the primary screen (orig == 0
// in vt10x.scrollUp), before that row's backing storage is cleared.
//
// The pane lock (p.mu) is already held by captureAndWrite, which is the
// only caller of term.Write under that lock; no additional locking is needed.
//
// Alt-screen scrolls must be ignored: TUI apps (vim, htop, less) use the
// alt-screen and their scroll events must not populate primary scrollback.
func (p *Pane) onScrollRow(row []vt10x.Glyph) {
	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		return
	}
	oldCount := p.sb.count
	oldSbOff := p.sbOff
	p.sb.push(row)
	p.adjustAfterScrollbackPush(1, oldCount, oldSbOff)
}

// onScrollbackClear is the vt10x scrollback-erase callback installed in
// NewPane via WithScrollbackClearCallback.  It fires synchronously inside
// vt10x.Write() when the application requests scrollback erasure: ED 3
// (CSI 3 J, sent by clear(1) via the xterm E3 capability) or RIS (ESC c,
// sent by reset(1)).  p.mu is already held by captureAndWrite, same as
// onScrollRow.
//
// Besides emptying the ring, rawBuf is cut just past the erase sequence so
// a later resize/reflow replay cannot resurrect the erased history.  The
// sequence was appended to rawBuf by writeTerminalChunk before term.Write
// parsed it, so searching from the end finds it; any same-chunk bytes after
// it (e.g. the prompt redraw that follows clear's output) are kept.  If no
// known spelling is found (e.g. the erase arrived while the alt screen was
// active and was never appended to rawBuf), rawBuf is left intact — for the
// clear(1)/reset(1) streams the replayed 2J/RIS still wipes the scratch
// terminal, so reflow stays correct.
func (p *Pane) onScrollbackClear() {
	L.Debug("scrollback clear requested", "pane", p.id, "dropped", p.sb.count)
	p.sb.clear()
	p.sbOff = 0
	p.selActive = false

	cut := 0
	for _, seq := range [][]byte{[]byte("\x1b[3J"), []byte("\x1bc")} {
		if i := bytes.LastIndex(p.rawBuf, seq); i >= 0 && i+len(seq) > cut {
			cut = i + len(seq)
		}
	}
	p.rawBuf = p.rawBuf[cut:]
}

// captureAndWrite writes chunk to vt10x and answers terminal capability
// queries against the terminal state that exists at that byte offset in the
// stream.  Scrollback capture is handled by the onScrollRow callback installed
// at pane creation — vt10x calls it directly when rows scroll off the top, so
// no post-write diffing is required here.
//
// Must be called with Pane.mu held.
func (p *Pane) captureAndWrite(chunk []byte) {
	if len(chunk) == 0 {
		return
	}

	offset := 0
	for offset < len(chunk) {
		q, ok := nextTerminalQuery(chunk, offset)
		if !ok {
			p.writeTerminalChunk(chunk[offset:])
			return
		}
		if q.start > offset {
			p.writeTerminalChunk(chunk[offset:q.start])
		}
		p.writeTerminalChunk(chunk[q.start:q.end])
		p.replyTerminalQuery(q)
		offset = q.end
	}
}

// writeTerminalChunk feeds chunk into vt10x while preserving bunk's
// scrollback, rawBuf replay, alt-screen hygiene, and kitty-keyboard handling.
func (p *Pane) writeTerminalChunk(chunk []byte) {
	if !p.hasGraphics {
		p.hasGraphics = containsGraphics(chunk)
	}
	altScreen := p.term.Mode()&vt10x.ModeAltScreen != 0
	if L.Enabled(context.Background(), LevelTrace) {
		cur := p.term.Cursor()
		L.Log(context.Background(), LevelTrace, "writeTerminalChunk: start", "pane", p.id, "cursor_y", cur.Y, "cursor_x", cur.X, "alt", altScreen, "chunk_len", len(chunk))
	}

	// Kitty keyboard protocol — bunk acts as the "terminal" for pane apps.
	// Apps negotiate with us using three sequence types:
	//   \x1b[?u        - query current flags  → respond with \x1b[?<flags>u
	//   \x1b[>|=<n>u   - push flags onto stack (> per spec, = legacy)
	//   \x1b[<N>u      - pop N levels (N defaults to 1)
	// All are stripped before vt10x sees them; vt10x interprets the bare 'u'
	// final byte as DECRC (restore cursor), jumping the cursor to 0,0.
	if bytes.ContainsAny(chunk, "u") && bytes.Contains(chunk, []byte("\x1b[")) {
		chunk = p.handleKittyKeyboard(chunk)
	}

	// Accumulate raw bytes for replay-based reflow on resize.
	// Skip while alternate screen is active (vim, htop, etc.) — those apps
	// paint absolute-position content that doesn't replay meaningfully.
	if !altScreen {
		p.rawBuf = append(p.rawBuf, chunk...)
		p.trimRawHistory()
	}

	// If this chunk crosses an alt-screen entry point:
	//   1. Save the primary cursor so we can restore it on exit (vt10x shares
	//      a single saved-cursor slot for ESC-7/8 and \x1b[?1049h/l, so the
	//      real primary position is lost once the alt-screen swap happens).
	//   2. Split processing and inject \x1b[2J\x1b[H right after the entry so
	//      the alt-screen starts with a clean slate (it may contain stale
	//      content if a previous TUI program crashed without sending \x1b[?1049l).
	wrote := false
	exitSplitAt := 0 // byte offset into chunk just after the ?1049l/47l sequence, 0 if none
	if !altScreen {
		for _, seq := range []string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				// Write everything BEFORE the alt-screen entry sequence first,
				// so any cursor-movement sequences in this chunk (e.g. vim's
				// \r\n or \x1b[row;colH) update the vt10x cursor position.
				// Only then read the cursor — otherwise over SSH where many
				// sequences arrive in one chunk, the pre-entry cursor moves
				// are missed and we save y=0 instead of the actual prompt row.
				if i > 0 {
					p.term.Write(chunk[:i]) //nolint:errcheck
				}
				cur := p.term.Cursor()
				p.altEntryCursorX, p.altEntryCursorY = cur.X, cur.Y
				L.Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen entry", "pane", p.id, "seq", seq, "cursor_x", cur.X, "cursor_y", cur.Y)

				end := i + len(b)
				p.term.Write(chunk[i:end])            //nolint:errcheck
				p.term.Write([]byte("\x1b[2J\x1b[H")) //nolint:errcheck
				if end < len(chunk) {
					p.term.Write(chunk[end:]) //nolint:errcheck
				}
				wrote = true
				break
			}
		}
	}
	// If this chunk crosses an alt-screen EXIT point:
	//   1. Inject \x1b[0m right after the exit sequence to prevent TUI background
	//      colour bleeding into the restored primary screen.
	//   2. Inject curRestore BEFORE writing chunk[end:] — over SSH, bash often
	//      sends the shell prompt in the same chunk as \x1b[?1049l.  If curRestore
	//      were injected after chunk[end:], it would rewind the cursor to x=0 on
	//      the prompt line (undoing the natural cursor advance from the prompt text)
	//      causing subsequent input to overwrite PS1.
	if !wrote && altScreen {
		for _, seq := range []string{"\x1b[?1049l", "\x1b[?1047l", "\x1b[?47l"} {
			b := []byte(seq)
			if i := bytes.Index(chunk, b); i >= 0 {
				end := i + len(b)
				exitSplitAt = end
				p.term.Write(chunk[:end])       //nolint:errcheck
				p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
				// Restore primary cursor here — before any trailing output
				// in this chunk (e.g. prompt text) — so the prompt text
				// advances the cursor naturally to the correct position.
				L.Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen exit", "pane", p.id, "cursor_x", p.altEntryCursorX, "cursor_y", p.altEntryCursorY)
				curRestore := fmt.Sprintf("\x1b[%d;%dH", p.altEntryCursorY+1, p.altEntryCursorX+1)
				L.Log(context.Background(), LevelTrace, "captureAndWrite: injecting curRestore", "pane", p.id, "seq", curRestore)
				p.term.Write([]byte(curRestore)) //nolint:errcheck
				if end < len(chunk) {
					p.term.Write(chunk[end:]) //nolint:errcheck
				}
				wrote = true
				break
			}
		}
	}
	if !wrote {
		p.term.Write(chunk) //nolint:errcheck
	}

	// When alt screen exits, append the chunk to rawBuf (skipped above because
	// altScreen=true) and insert the SGR reset at the split point so rawBuf
	// replay uses default colours for subsequent clears.
	if altScreen && p.term.Mode()&vt10x.ModeAltScreen == 0 {
		// Reset kitty keyboard protocol stack.  TUI apps push onto the stack
		// when entering alt-screen but may not pop if they crash, are killed,
		// or exit abnormally.  Clearing here prevents stale flags from
		// affecting the shell after the alt-screen app is gone.
		p.kittyStack = p.kittyStack[:0]

		const sgrReset = "\x1b[0m"
		if exitSplitAt > 0 {
			// Insert sgrReset into rawBuf right after the exit sequence so
			// replay sees the same colour-reset behaviour as the live path.
			p.rawBuf = append(p.rawBuf, chunk[:exitSplitAt]...)
			p.rawBuf = append(p.rawBuf, sgrReset...)
			p.rawBuf = append(p.rawBuf, chunk[exitSplitAt:]...)
		} else {
			// Fallback: exit sequence not found in this chunk (e.g. split
			// across two reads).  curRestore was not injected above, so do it
			// now before any further output from this chunk.
			p.rawBuf = append(p.rawBuf, chunk...)
			p.term.Write([]byte(sgrReset)) //nolint:errcheck
			p.rawBuf = append(p.rawBuf, sgrReset...)
			L.Log(context.Background(), LevelTrace, "captureAndWrite: alt-screen exit (fallback)", "pane", p.id, "cursor_x", p.altEntryCursorX, "cursor_y", p.altEntryCursorY)
			curRestore := fmt.Sprintf("\x1b[%d;%dH", p.altEntryCursorY+1, p.altEntryCursorX+1)
			L.Log(context.Background(), LevelTrace, "captureAndWrite: injecting curRestore (fallback)", "pane", p.id, "seq", curRestore)
			p.term.Write([]byte(curRestore)) //nolint:errcheck
		}
		p.trimRawHistory()

		// Signal the render loop to do a full Sync() repaint so any residual
		// background colour from the TUI app is cleared from the terminal.
		p.needsSync = true
	}

}

type terminalQueryKind int

const (
	terminalQueryDA terminalQueryKind = iota
	terminalQueryDA2
	terminalQueryXTVERSION
	terminalQueryXTGETTCAP
	terminalQueryCPR
	terminalQueryDECRQM
	terminalQueryOSC10
	terminalQueryOSC11
	terminalQueryOSC12
	terminalQueryDA3
	terminalQueryDSR
	terminalQueryDECRQSS
	terminalQuerySize
)

type terminalQuery struct {
	start, end int
	kind       terminalQueryKind
	mode       int
	payload    string
}

func (p *Pane) replyTerminalQuery(q terminalQuery) {
	switch q.kind {
	case terminalQuerySize:
		cols, rows := p.term.Size()
		cw, ch := p.term.CellPixels()
		switch q.mode {
		case 14:
			p.writeInput(fmt.Appendf(nil, "\x1b[4;%d;%dt", rows*ch, cols*cw))
		case 16:
			p.writeInput(fmt.Appendf(nil, "\x1b[6;%d;%dt", ch, cw))
		case 18:
			p.writeInput(fmt.Appendf(nil, "\x1b[8;%d;%dt", rows, cols))
		}
	case terminalQueryDA3:
		p.writeInput([]byte("\x1bP!|00000000\x1b\\"))
	case terminalQueryDSR:
		p.writeInput([]byte("\x1b[0n"))
	case terminalQueryDECRQSS:
		p.writeInput([]byte(p.term.StatusString(q.payload)))
	case terminalQueryDA:
		p.ptmx.Write([]byte("\x1b[?62;1;2;4;6;9;15;22c")) //nolint:errcheck
		L.Log(context.Background(), LevelTrace, "captureAndWrite: DA response", "pane", p.id)
	case terminalQueryDA2:
		p.ptmx.Write([]byte("\x1b[>0;279;0c")) //nolint:errcheck
		L.Log(context.Background(), LevelTrace, "captureAndWrite: DA2 response", "pane", p.id)
	case terminalQueryXTVERSION:
		p.ptmx.Write([]byte("\x1bP>|VTE(8203)\x1b\\")) //nolint:errcheck
		L.Log(context.Background(), LevelTrace, "captureAndWrite: XTVERSION response", "pane", p.id)
	case terminalQueryXTGETTCAP:
		flags := 0
		if len(p.kittyStack) > 0 {
			flags = p.kittyStack[len(p.kittyStack)-1]
		}
		for _, hexCap := range strings.Split(q.payload, ";") {
			if resp := xtgettcapResponse(hexCap, flags, p.term.Mode()); resp != "" {
				p.ptmx.Write([]byte(resp)) //nolint:errcheck
			}
		}
		L.Log(context.Background(), LevelTrace, "captureAndWrite: XTGETTCAP response", "pane", p.id, "payload", q.payload)
	case terminalQueryCPR:
		x, y := p.term.CursorPosition()
		prefix := ""
		if q.mode != 0 {
			prefix = "?"
		}
		resp := fmt.Sprintf("\x1b[%s%d;%dR", prefix, y+1, x+1)
		p.ptmx.Write([]byte(resp)) //nolint:errcheck
		L.Log(context.Background(), LevelTrace, "captureAndWrite: CPR response", "pane", p.id, "row", y+1, "col", x+1)
	case terminalQueryDECRQM:
		status := p.term.QueryPrivateMode(q.mode)
		resp := fmt.Sprintf("\x1b[?%d;%c$y", q.mode, status)
		p.ptmx.Write([]byte(resp)) //nolint:errcheck
		L.Log(context.Background(), LevelTrace, "captureAndWrite: DECRQM response", "pane", p.id, "mode", q.mode, "status", string(status))
	case terminalQueryOSC10:
		p.replyOSCColorQuery(10, vt10x.DefaultFG, p.themeFGColor)
	case terminalQueryOSC11:
		p.replyOSCColorQuery(11, vt10x.DefaultBG, p.themeBGColor)
	case terminalQueryOSC12:
		p.replyOSCColorQuery(12, vt10x.DefaultCursor, p.themeCursorColor)
	}
}

func (p *Pane) replyOSCColorQuery(num int, def vt10x.Color, fallback string) {
	color := p.currentOSCColor(def, fallback)
	if color == "" {
		return
	}
	resp := fmt.Sprintf("\x1b]%d;%s\x1b\\", num, color)
	p.ptmx.Write([]byte(resp)) //nolint:errcheck
	L.Log(context.Background(), LevelTrace, "captureAndWrite: OSC color response", "pane", p.id, "osc_num", num, "color", color)
}

func (p *Pane) currentOSCColor(def vt10x.Color, fallback string) string {
	if c, ok := p.term.ColorOverride(def); ok {
		return vtColorToXParse(c)
	}
	return fallback
}

func vtColorToXParse(c vt10x.Color) string {
	if c >= vt10x.DefaultFG {
		return ""
	}
	r := byte(c >> 16)
	g := byte(c >> 8)
	b := byte(c)
	return fmt.Sprintf("rgb:%02x%02x/%02x%02x/%02x%02x", r, r, g, g, b, b)
}

func nextTerminalQuery(data []byte, from int) (terminalQuery, bool) {
	for i := from; i+1 < len(data); i++ {
		if data[i] != 0x1b {
			continue
		}
		switch data[i+1] {
		case '[':
			if q, ok := parseCSIQuery(data, i); ok {
				return q, true
			}
		case ']':
			end := oscSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if q, ok := parseOSCQuery(data, i, end); ok {
				return q, true
			}
			i = end - 1
		case 'P':
			end := dcsSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP+q")) {
				return terminalQuery{
					start:   i,
					end:     end,
					kind:    terminalQueryXTGETTCAP,
					payload: string(data[i+4 : end-2]),
				}, true
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP$q")) {
				return terminalQuery{start: i, end: end, kind: terminalQueryDECRQSS, payload: string(data[i+4 : end-2])}, true
			}
			i = end - 1
		case '_', '^', 'X':
			if end := dcsSequenceEnd(data, i); end > i {
				i = end - 1
			}
		}
	}
	return terminalQuery{}, false
}

func parseCSIQuery(data []byte, start int) (terminalQuery, bool) {
	rest := data[start:]
	switch {
	case bytes.HasPrefix(rest, []byte("\x1b[14t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 14}, true
	case bytes.HasPrefix(rest, []byte("\x1b[16t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 16}, true
	case bytes.HasPrefix(rest, []byte("\x1b[18t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 18}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=c")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=0c")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[5n")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDSR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0q")):
		return terminalQuery{start: start, end: start + len("\x1b[>0q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>q")):
		return terminalQuery{start: start, end: start + len("\x1b[>q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0c")):
		return terminalQuery{start: start, end: start + len("\x1b[>0c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>c")):
		return terminalQuery{start: start, end: start + len("\x1b[>c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[0c")):
		return terminalQuery{start: start, end: start + len("\x1b[0c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[c")):
		return terminalQuery{start: start, end: start + len("\x1b[c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[6n")):
		return terminalQuery{start: start, end: start + len("\x1b[6n"), kind: terminalQueryCPR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?6n")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryCPR, mode: 1}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?")):
		mode, end, ok := parseDECRQM(data, start)
		if ok {
			return terminalQuery{start: start, end: end, kind: terminalQueryDECRQM, mode: mode}, true
		}
	}
	return terminalQuery{}, false
}

func parseDECRQM(data []byte, start int) (mode, end int, ok bool) {
	i := start + len("\x1b[?")
	j := i
	for j < len(data) && data[j] >= '0' && data[j] <= '9' {
		mode = mode*10 + int(data[j]-'0')
		if mode > 65535 {
			return 0, 0, false
		}
		j++
	}
	if j == i || j+1 >= len(data) || data[j] != '$' || data[j+1] != 'p' {
		return 0, 0, false
	}
	return mode, j + 2, true
}

func parseOSCQuery(data []byte, start, end int) (terminalQuery, bool) {
	bodyEnd := end - 1
	if end-start >= 2 && data[end-1] != 0x07 {
		bodyEnd = end - 2
	}
	switch string(data[start+2 : bodyEnd]) {
	case "10;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC10}, true
	case "11;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC11}, true
	case "12;?":
		return terminalQuery{start: start, end: end, kind: terminalQueryOSC12}, true
	default:
		return terminalQuery{}, false
	}
}

func oscSequenceEnd(data []byte, start int) int {
	for i := start + 2; i < len(data); i++ {
		switch data[i] {
		case 0x07:
			return i + 1
		case 0x1b:
			if i+1 < len(data) && data[i+1] == '\\' {
				return i + 2
			}
		}
	}
	return -1
}

func dcsSequenceEnd(data []byte, start int) int {
	for i := start + 2; i < len(data); i++ {
		if data[i] == 0x1b && i+1 < len(data) && data[i+1] == '\\' {
			return i + 2
		}
	}
	return -1
}

// waitForExit blocks until the shell process exits (or the app shuts down),
// then marks the pane dead and notifies the app so it can remove the pane.
func (p *Pane) waitForExit(paneDead chan *Pane, done chan struct{}) {
	p.cmd.Wait() //nolint:errcheck
	L.Debug("pane process exited", "id", p.id)
	p.mu.Lock()
	p.dead = true
	p.mu.Unlock()
	select {
	case paneDead <- p:
	case <-done:
	}
}

// writeInput sends raw bytes (encoded keystrokes or mouse sequences) to the
// shell via the PTY master.
func (p *Pane) writeInput(data []byte) {
	p.ptmx.Write(data) //nolint:errcheck
}

// scrollUp scrolls the view n lines toward the past (increases sbOff).
// Clamped so sbOff never exceeds the number of captured lines.
// p.sb is populated in real time by onScrollRow; no rebuild is needed here.
func (p *Pane) scrollUp(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.sbOff
	p.sbOff += n
	if p.sbOff > p.sb.count {
		p.sbOff = p.sb.count
	}
	L.Debug("scrollUp", "pane", p.id, "from", before, "to", p.sbOff, "max", p.sb.count)
}

// scrollDown scrolls the view n lines toward the present (decreases sbOff).
func (p *Pane) scrollDown(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	before := p.sbOff
	p.sbOff -= n
	if p.sbOff < 0 {
		p.sbOff = 0
	}
	L.Debug("scrollDown", "pane", p.id, "from", before, "to", p.sbOff)
}

// scrollReset snaps back to the live view (sbOff = 0).
func (p *Pane) scrollReset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.sbOff != 0 {
		L.Debug("scrollReset", "pane", p.id, "was", p.sbOff)
	}
	p.sbOff = 0
}

// inScrollback reports whether the pane is currently showing scrollback.
// Safe to call without Pane.mu (reads an int, which is atomically readable
// on all Go-supported platforms, but we use a lock for correctness).
func (p *Pane) inScrollback() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.sbOff > 0
}

// isDead reports whether the shell has exited.
func (p *Pane) isDead() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.dead
}

// SetStatus shows msg in the pane's status bar for dur, then clears it.
func (p *Pane) SetStatus(msg string, dur time.Duration) {
	p.mu.Lock()
	p.statusMsg = msg
	p.statusMsgEnd = time.Now().Add(dur)
	p.mu.Unlock()
}

// resize updates the pane's screen position and dimensions, sends TIOCSWINSZ
// to the PTY (causing the shell to receive SIGWINCH), and resizes the vt10x
// grid.  If the width changes, the existing terminal content is captured and
// re-injected so it reflows naturally to the new column width instead of being
// silently truncated.
func (p *Pane) resize(x, y, w, h int) {
	L.Debug("pane resize", "pane", p.id, "x", x, "y", y, "w", w, "h", h)
	p.mu.Lock()
	p.x, p.y, p.w, p.h = x, y, w, h
	p.resizeAndReflow(w-1, h)
	cw, ch := p.term.CellPixels()
	p.mu.Unlock()
	if p.ptmx != nil {
		pty.Setsize(p.ptmx, paneWinsize(w-1, h, cw, ch)) //nolint:errcheck
	}
}

// markFullRepaint forces the next renderPane call to repaint every row of
// this pane, even when vt10x reports no dirty rows.  Used when the tcell
// cell buffer under this pane holds another pane's content (zoom exit).
func (p *Pane) markFullRepaint() {
	p.mu.Lock()
	p.forceFullRepaint = true
	p.mu.Unlock()
}

// resizePTYOnly updates the pane's screen coordinates and PTY size without
// performing the expensive rawBuf replay.  This gives the shell an immediate
// SIGWINCH so it can start redrawing while the full reflow is deferred.
func (p *Pane) resizePTYOnly(x, y, w, h int) {
	p.mu.Lock()
	p.x, p.y, p.w, p.h = x, y, w, h
	// Alt-screen apps (btop, vim, …) redraw immediately after SIGWINCH.
	// If vt10x is still at the old size when that redraw arrives, the
	// output is parsed at the wrong grid dimensions → corruption.
	// The vt10x resize is cheap for alt-screen (no rawBuf replay), so
	// do it here before sending SIGWINCH.
	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
		p.term.Resize(w-1, h)
	}
	cw, ch := p.term.CellPixels()
	p.mu.Unlock()
	if p.ptmx != nil {
		pty.Setsize(p.ptmx, paneWinsize(w-1, h, cw, ch)) //nolint:errcheck
	}
}

// resizeAndReflow resizes the pane terminal to (newCols × newRows).
//
// When raw PTY bytes are available, the entire output history is replayed
// into a temporary vt10x at the new width so lines wrap correctly at the
// new column count.  The resulting state is split into a new glyph scrollback
// (rows that don't fit in newRows) and the replacement live grid (the rest).
//
// For alt-screen apps (vim, htop, …) reflow is skipped — they redraw
// themselves after SIGWINCH.
//
// Must be called with p.mu held.
func (p *Pane) resizeAndReflow(newCols, newRows int) {
	if newCols < 1 || newRows < 1 {
		return // terminal too small to be usable
	}
	oldCols, oldRows := p.term.Size()
	if oldCols == newCols && oldRows == newRows {
		return
	}

	if p.term.Mode()&vt10x.ModeAltScreen != 0 {
		// Reset cursor attributes before resize so that vt10x clears the
		// newly created cells (in both the alt and saved normal screen)
		// with default colours instead of the TUI app's current style.
		// Without this, exiting the alt-screen app after a resize leaks
		// its background colour into the normal screen's expanded area.
		p.term.Write([]byte("\x1b[0m")) //nolint:errcheck
		p.term.Resize(newCols, newRows)
		return
	}

	if len(p.rawBuf) == 0 {
		// No history yet (brand-new pane); plain resize is sufficient.
		p.term.Resize(newCols, newRows)
		return
	}

	// Fast path: when only the height changed (cols unchanged), we don't
	// need to re-wrap text.  Just redistribute rows between the scrollback
	// ring and the live terminal grid.
	if oldCols == newCols {
		p.resizeHeightOnly(newCols, oldRows, newRows)
		return
	}

	// Replay raw bytes into a tall scratch terminal so nothing scrolls off
	// during replay and we can read back all rows.
	//
	// Alt-screen sessions (vim, htop, etc.) are stripped from the replay
	// buffer — their absolute-position content doesn't replay meaningfully,
	// and stripping avoids allocating a large alt-screen buffer in the
	// scratch terminal.  Pre-vim shell history is preserved.
	replay := stripAltScreen(p.rawBuf)

	// Hard line breaks consume rows independently of line wrapping. Count
	// both, capped at the history limit, so short lines survive replay.
	estimatedLines := len(replay)/max(newCols, 1) + bytes.Count(replay, []byte{'\n'}) + newRows
	replayH := min(estimatedLines, p.scrollbackLines+newRows)
	if replayH < newRows {
		replayH = newRows
	}

	cw, ch := p.term.CellPixels()
	scratch := vt10x.New(vt10x.WithSize(newCols, replayH), vt10x.WithCellPixels(cw, ch), vt10x.WithGraphicsState(p.term.GraphicsState()), vt10x.WithGraphicsViewportRows(newRows))
	// Prepend a full SGR reset so trimmed attribute state doesn't bleed.
	scratch.Write(append([]byte("\x1b[0m"), replay...)) //nolint:errcheck

	contentRows := findContentRows(scratch, newCols, replayH)

	// Split: rows [0, firstVisible) → scrollback; [firstVisible, contentRows) → live terminal.
	firstVisible := contentRows - newRows
	if firstVisible < 0 {
		firstVisible = 0
	}

	// Always rebuild the scrollback ring from the replay on a column-width
	// change.  Any rows currently in the ring are at oldCols width; keeping
	// them at newCols would produce garbled wrapping when the user scrolls.
	//
	// When firstVisible > 0 the ring is populated from the replay rows.
	// When firstVisible == 0 (all rawBuf content fits in the new terminal)
	// the ring is reset to empty — the live terminal holds everything that
	// rawBuf covers, so there are no rows to archive.
	//
	// This means any scrollback that rawBuf's rolling window no longer covers
	// is discarded on a column-width resize.  That is an accepted limitation;
	// the alternative (keeping stale-width rows) produces visible corruption.

	// Before rebuilding, capture the character content of the viewport's
	// centre row so we can find the same line in the new ring after reflow.
	// The centre row is chosen because it is the most stable landmark: the
	// top row can disappear when the ring shrinks, and the bottom row is
	// already at the live-view boundary.
	const anchorMinLen = 6
	var anchorChars []rune
	anchorOldRingIdx := -1 // ring index (0=oldest) of the anchor row before rebuild
	if p.sbOff > 0 {
		midRingIdx := p.sb.count - p.sbOff + oldRows/2
		if midRingIdx >= 0 && midRingIdx < p.sb.count {
			anchorChars = rowChars(p.sb.get(midRingIdx))
			anchorOldRingIdx = midRingIdx
		}
	}

	oldSbOff := p.sbOff
	oldSbCount := p.sb.count
	p.sb = sbRing{maxLines: p.scrollbackLines}
	for r := 0; r < firstVisible; r++ {
		row := captureRow(scratch, r, newCols)
		for c := range row {
			row[c] = p.term.ImportGlyph(row[c], scratch)
		}
		p.sb.push(row)
	}

	// Reposition the viewport so the same content line appears in the
	// centre of the pane after reflow.
	//
	// Strategy:
	//   1. If we have a valid anchor (centre row had ≥ anchorMinLen visible
	//      characters), search the scratch terminal for a row whose character
	//      content matches the anchor (prefix match handles wrap changes in
	//      both directions).  Use the proportional estimate as a tiebreaker
	//      when the same content appears multiple times.  Set sbOff to put the
	//      matched row at newRows/2 from the top of the viewport.
	//   2. If no anchor, no match, or user was in live view: fall back to
	//      proportional mapping of the viewport's top row.
	p.sbOff = reflowSbOff(
		oldSbOff, oldSbCount, oldRows,
		p.sb.count, newRows,
		anchorChars, anchorOldRingIdx, anchorMinLen,
		scratch, newCols,
	)

	// Replace only the visible grid, retaining the live terminal state.
	visibleRows := make([][]vt10x.Glyph, newRows)
	for r := 0; r < newRows; r++ {
		srcRow := firstVisible + r
		if srcRow < contentRows {
			visibleRows[r] = captureRow(scratch, srcRow, newCols)
		} else {
			visibleRows[r] = make([]vt10x.Glyph, newCols) // blank padding
		}
	}

	// Translate the replay cursor into the visible slice, retaining the
	// live terminal's modes, attributes, colours, and callbacks.
	scratchCur := scratch.Cursor()
	visCurRow := scratchCur.Y - firstVisible
	scratchCur.Y = max(0, min(visCurRow, newRows-1))
	p.term.ReplaceScreen(newCols, newRows, visibleRows, scratchCur, scratch)

	L.Debug("resizeAndReflow: raw replay done", "pane", p.id,
		"old", fmt.Sprintf("%dx%d", oldCols, oldRows),
		"new", fmt.Sprintf("%dx%d", newCols, newRows),
		"content_rows", contentRows, "sb_rows", firstVisible)
}

// resizeHeightOnly handles the common case where only the row count changed
// (columns stayed the same).  No text re-wrapping is needed — we just
// redistribute existing rows between the scrollback ring and the live grid.
// This avoids the expensive rawBuf replay that resizeAndReflow does for
// column-width changes.
//
// Must be called with p.mu held.
func (p *Pane) resizeHeightOnly(cols, oldRows, newRows int) {
	if newRows > oldRows {
		// Terminal grew taller: pull rows from scrollback into the live grid.
		// Capture the current live grid, resize vt10x, then inject the
		// combined rows (pulled scrollback + old live content).
		extra := newRows - oldRows
		pull := extra
		if pull > p.sb.count {
			pull = p.sb.count
		}

		// Collect rows to inject: pulled scrollback + existing live rows.
		combined := make([][]vt10x.Glyph, newRows)
		for i := 0; i < pull; i++ {
			combined[i] = p.sb.get(p.sb.count - pull + i)
		}
		for r := 0; r < oldRows; r++ {
			combined[pull+r] = captureRow(p.term, r, cols)
		}
		// Blank padding for remaining rows.
		for r := pull + oldRows; r < newRows; r++ {
			combined[r] = make([]vt10x.Glyph, cols)
		}

		// Shrink scrollback by the pulled amount.
		if pull > 0 {
			newSB := sbRing{maxLines: p.scrollbackLines}
			for i := 0; i < p.sb.count-pull; i++ {
				newSB.push(p.sb.get(i))
			}
			p.sb = newSB
		}

		// Move the cursor down by the number of rows pulled from history.
		origCur := p.term.Cursor()
		combCurRow := pull + origCur.Y

		// Preserve the scroll position.  The newest `pull` scrollback rows
		// moved into the live terminal, so the user's distance from the live
		// view bottom decreases by `pull`.  Clamp to zero (live view) when
		// the user was looking at rows that are now in the live grid.
		if p.sbOff > pull {
			p.sbOff -= pull
		} else {
			p.sbOff = 0
		}

		origCur.Y = max(0, min(combCurRow, newRows-1))
		p.term.ReplaceScreen(cols, newRows, combined, origCur, p.term)
	} else {
		// Terminal shrank: push excess live rows into scrollback.
		// First find the actual content extent — trailing blank rows should
		// be discarded rather than causing content to scroll off the top.
		contentRows := findContentRows(p.term, cols, oldRows)
		excess := contentRows - newRows
		if excess < 0 {
			excess = 0
		}
		oldSbOff := p.sbOff
		for r := 0; r < excess; r++ {
			p.sb.push(captureRow(p.term, r, cols))
		}
		// Capture the visible rows (starting from where content begins
		// to fit in the new height).
		remaining := make([][]vt10x.Glyph, newRows)
		for r := 0; r < newRows; r++ {
			srcRow := excess + r
			if srcRow < oldRows {
				remaining[r] = captureRow(p.term, srcRow, cols)
			} else {
				remaining[r] = make([]vt10x.Glyph, cols)
			}
		}
		// Move the cursor up by the number of rows archived into history.
		origCur := p.term.Cursor()
		remCurRow := origCur.Y - excess

		// Preserve the scroll position.  `excess` rows were pushed from the
		// top of the live terminal into scrollback; the user's distance from
		// the live view bottom increases by the same amount.
		if oldSbOff > 0 {
			p.sbOff = min(oldSbOff+excess, p.sb.count)
		} // else: live view stays live view (sbOff 0 → 0)

		origCur.Y = max(0, min(remCurRow, newRows-1))
		p.term.ReplaceScreen(cols, newRows, remaining, origCur, p.term)
	}

	L.Debug("resizeHeightOnly: done", "pane", p.id,
		"cols", cols, "oldRows", oldRows, "newRows", newRows,
		"sb_count", p.sb.count)
}

// utf8Boundary returns the largest index i (0 ≤ i ≤ len(b)) such that b[:i]
// contains only complete UTF-8 sequences.  b[i:] is the trailing incomplete
// sequence (if any) that should be carried over to the next read.
//
// Only the last 3 bytes need to be inspected because the longest UTF-8
// sequence is 4 bytes and a complete sequence at the end is always valid.
func utf8Boundary(b []byte) int {
	n := len(b)
	if n == 0 || b[n-1] < 0x80 {
		return n // ends with ASCII — always complete
	}
	// Walk backwards up to 3 bytes looking for the start byte.
	for i := 1; i <= 3 && i <= n; i++ {
		c := b[n-i]
		if c < 0x80 {
			return n // hit an ASCII byte; everything after is invalid but
			// that can't happen in a well-formed stream — treat as complete.
		}
		if utf8.RuneStart(c) {
			// Found start byte.  A start byte at n-i needs seqLen bytes.
			var seqLen int
			switch {
			case c < 0xE0:
				seqLen = 2
			case c < 0xF0:
				seqLen = 3
			default:
				seqLen = 4
			}
			if i < seqLen {
				return n - i // incomplete — split here
			}
			return n // sequence is complete
		}
		// continuation byte (0x80..0xBF) — keep scanning
	}
	return n
}

// scanDECRQM scans data for DECRQM private-mode queries (\x1b[?N$p) and calls
// fn with each parsed mode number. Multiple queries can appear in one chunk.
func scanDECRQM(data []byte, fn func(n int)) {
	const prefix = "\x1b[?"
	i := 0
	for i < len(data) {
		idx := bytes.Index(data[i:], []byte(prefix))
		if idx < 0 {
			break
		}
		i += idx + len(prefix)
		// Parse decimal digits up to the suffix.
		j := i
		for j < len(data) && data[j] >= '0' && data[j] <= '9' {
			j++
		}
		if j > i && j+1 < len(data) && data[j] == '$' && data[j+1] == 'p' {
			n := 0
			for _, b := range data[i:j] {
				n = n*10 + int(b-'0')
			}
			fn(n)
		}
		i = j
	}
}

// findContentRows scans backwards from the bottom of a scratch terminal to
// find the last non-blank row.  Returns the number of rows with content
// (i.e. the first blank-only row index from the top).
func findContentRows(t vt10x.Terminal, cols, totalRows int) int {
	cur := t.Cursor()
	contentRows := cur.Y + 1
	for r := totalRows - 1; r >= contentRows; r-- {
		blank := true
		for c := 0; c < cols; c++ {
			g := t.Cell(c, r)
			if g.Char != 0 && g.Char != ' ' {
				blank = false
				break
			}
		}
		if !blank {
			contentRows = r + 1
			break
		}
	}
	return contentRows
}

// adjustAfterScrollbackPush updates sbOff and selection coordinates after
// `pushed` rows have been added to the scrollback ring.  oldCount and
// oldSbOff are the values before the push.  Must be called with p.mu held.
func (p *Pane) adjustAfterScrollbackPush(pushed, oldCount, oldSbOff int) {
	if p.sbOff > 0 {
		p.sbOff += pushed
		if p.sbOff > p.sb.count {
			p.sbOff = p.sb.count
		}
	}
	if p.selActive {
		oldVT := oldCount - oldSbOff
		newVT := p.sb.count - p.sbOff
		if d := newVT - oldVT; d != 0 {
			p.selAnchor.row += d
			p.selCursor.row += d
		}
	}
}

// close shuts down the PTY and sends SIGHUP to the shell so it exits cleanly.
// Safe to call multiple times.
func (p *Pane) close() {
	L.Debug("pane close", "pane", p.id)
	p.closePTX()
	if p.cmd.Process != nil {
		p.cmd.Process.Signal(syscall.SIGHUP) //nolint:errcheck
	}
}

// cwd returns the working directory of the pane's foreground process (or the
// shell itself).  Returns "" if the information is unavailable or the path
// doesn't exist on the host (e.g. a container-internal path).
func (p *Pane) cwd() string {
	if p.cmd.Process == nil {
		return ""
	}
	pid := p.cmd.Process.Pid
	// Try the foreground process first, fall back to the shell itself.
	if pgid := termFgPGID(pid); pgid > 0 {
		if d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pgid)); err == nil {
			if info, err := os.Stat(d); err == nil && info.IsDir() {
				return d
			}
		}
	}
	d, err := os.Readlink(fmt.Sprintf("/proc/%d/cwd", pid))
	if err != nil {
		return ""
	}
	if info, err := os.Stat(d); err == nil && info.IsDir() {
		return d
	}
	return ""
}

// closePTX closes the PTY master exactly once.  Closing the master causes the
// kernel to send HUP to the shell's controlling terminal.
func (p *Pane) closePTX() {
	p.ptmxOnce.Do(func() { p.ptmx.Close() }) //nolint:errcheck // PTY master close on shutdown
}

// ---------------------------------------------------------------------------
// Text selection helpers (all require p.mu to be held by the caller)
// ---------------------------------------------------------------------------

// selNorm returns the selection endpoints in top-left → bottom-right order.
func (p *Pane) selNorm() (start, end selPos) {
	a, c := p.selAnchor, p.selCursor
	if a.row < c.row || (a.row == c.row && a.col <= c.col) {
		return a, c
	}
	return c, a
}

// selContainsUnlocked reports whether virtual cell (vRow, col) falls within
// the current selection.  Requires p.mu held.
func (p *Pane) selContainsUnlocked(vRow, col int) bool {
	if !p.selActive {
		return false
	}
	start, end := p.selNorm()
	if vRow < start.row || vRow > end.row {
		return false
	}
	if start.row == end.row {
		return col >= start.col && col <= end.col
	}
	if vRow == start.row {
		return col >= start.col
	}
	if vRow == end.row {
		return col <= end.col
	}
	return true
}

// selText extracts the selected text from the virtual grid (scrollback + live).
// Lines are newline-separated; trailing spaces on each line are trimmed.
// Soft-wrapped rows (attrWrap on the last cell) are joined without a newline
// so that copy-paste preserves the original logical lines.
// Requires p.mu held.
func (p *Pane) selText() string {
	if !p.selActive {
		return ""
	}
	start, end := p.selNorm()
	if start == end {
		return ""
	}
	cols, rows := p.term.Size()
	sbCount := p.sb.count
	var buf strings.Builder
	var prevCells []vt10x.Glyph
	for vRow := start.row; vRow <= end.row; vRow++ {
		var cells []vt10x.Glyph
		if vRow < sbCount {
			cells = p.sb.get(vRow)
		} else if tr := vRow - sbCount; tr >= 0 && tr < rows {
			cells = captureRow(p.term, tr, cols)
		}
		if vRow > start.row {
			// Only insert \n if the previous row ended with a hard break.
			// Soft-wrapped rows have attrWrap on their last cell.
			softWrap := false
			if len(prevCells) > 0 {
				softWrap = prevCells[len(prevCells)-1].Mode&vtAttrWrap != 0
			}
			if !softWrap {
				buf.WriteByte('\n')
			}
		}
		fromCol, toCol := 0, cols-1
		if vRow == start.row {
			fromCol = start.col
		}
		if vRow == end.row {
			toCol = end.col
		}
		var line strings.Builder
		for c := fromCol; c <= toCol && c < cols; c++ {
			if cells != nil && c < len(cells) && cells[c].Width < 0 {
				if cells[c].Width == -1 && c == fromCol && c > 0 {
					line.WriteString(cells[c-1].Text())
				}
				continue
			}
			ch := rune(' ')
			if cells != nil && c < len(cells) {
				if g := cells[c].Char; g != 0 {
					ch = g
				}
			}
			line.WriteRune(ch)
			if cells != nil && c < len(cells) {
				line.WriteString(cells[c].Combining)
			}
		}
		// Only trim trailing spaces on hard-break rows; soft-wrapped rows
		// are full-width by definition.
		s := line.String()
		if len(cells) == 0 || cells[len(cells)-1].Mode&vtAttrWrap == 0 {
			s = strings.TrimRight(s, " ")
		}
		buf.WriteString(s)
		prevCells = cells
	}
	return buf.String()
}

// handleKittyKeyboard processes kitty keyboard protocol sequences in the
// byte stream emitted by pane applications, strips them before vt10x sees
// them (to prevent DECRC misinterpretation), and responds to queries.
//
// Three sequence types (all use the 'u' final byte):
//
//	\x1b [ ? u         - query: respond with \x1b[?<flags>u (top of stack or 0)
//	\x1b [ > <n> u     - push: save current flags and activate <n>  (spec)
//	\x1b [ = <n> ; <mode> u - replace/add/remove current flag bits
//	\x1b [ < <N> u     - pop:  pop N stack levels (N omitted → 1)
//
// Must be called with p.mu held (captureAndWrite contract).
// Writing to p.ptmx (the PTY) does not require p.mu.
func (p *Pane) handleKittyKeyboard(chunk []byte) []byte {
	// We walk through chunk looking for ESC [ followed by a kitty intro byte
	// (?, =, <). copied tracks how far we've flushed into out; out is nil
	// until we actually strip something (avoids allocation when nothing matches).
	var out []byte
	copied := 0 // index up to which chunk has been appended to out

	i := 0
	for i < len(chunk) {
		esc := bytes.IndexByte(chunk[i:], 0x1b)
		if esc < 0 {
			break // no more ESC [ — remainder is flushed below
		}
		abs := i + esc // absolute position of ESC in chunk
		if abs+1 >= len(chunk) {
			break
		}
		if chunk[abs+1] != '[' {
			end := abs + 2
			switch chunk[abs+1] {
			case ']':
				end = oscSequenceEnd(chunk, abs)
			case 'P', '_', '^', 'X':
				end = dcsSequenceEnd(chunk, abs)
			}
			if end < 0 {
				break
			}
			i = end
			continue
		}
		j := abs + 2 // first byte after '['
		if j >= len(chunk) {
			break // truncated sequence at end of chunk — pass through
		}

		intro := chunk[j]
		stripped := false
		newI := abs // default: don't skip (fall through to next iteration)

		switch intro {
		case '?':
			// \x1b[?u  (exactly 4 bytes: ESC [ ? u)
			if j+1 < len(chunk) && chunk[j+1] == 'u' {
				flags := 0
				if len(p.kittyStack) > 0 {
					flags = p.kittyStack[len(p.kittyStack)-1]
				}
				fmt.Fprintf(p.ptmx, "\x1b[?%du", flags) //nolint:errcheck // reply write to PTY; nothing actionable
				newI = j + 2
				stripped = true
			}
		case '>', '=':
			// \x1b[><flags>u   — push flags onto the stack (spec).
			// \x1b[=<flags>;<mode>u — set current flags (mode 1=all, 2=set,
			//   3=reset); the trailing ";<mode>" param means the scan must skip
			//   ';' as well as digits, otherwise the sequence is not recognised
			//   and leaks to vt10x, which misreads the final 'u' as DECRC
			//   (restore cursor) and corrupts cursor-relative drawing.
			k := j + 1
			for k < len(chunk) && ((chunk[k] >= '0' && chunk[k] <= '9') || chunk[k] == ';') {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				flags := 0
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &flags) //nolint:errcheck // best-effort parse of the first param; flags defaults to 0
				mode := 1
				if sep := bytes.IndexByte(chunk[j+1:k], ';'); sep >= 0 {
					fmt.Sscanf(string(chunk[j+2+sep:k]), "%d", &mode) //nolint:errcheck // omitted mode defaults to replacement
				}
				if intro == '=' && len(p.kittyStack) == 0 {
					p.kittyStack = append(p.kittyStack, 0)
				}
				if intro == '=' && len(p.kittyStack) > 0 {
					top := &p.kittyStack[len(p.kittyStack)-1]
					switch mode {
					case 1:
						*top = flags
					case 2:
						*top |= flags
					case 3:
						*top &^= flags
					}
				} else {
					if len(p.kittyStack) >= 32 {
						p.kittyStack = p.kittyStack[1:]
					}
					p.kittyStack = append(p.kittyStack, flags)
				}
				if p.cmd != nil && p.cmd.Process != nil {
					p.kittyOwnerPGID = termFgPGID(p.cmd.Process.Pid)
				}
				newI = k + 1
				stripped = true
			}
		case '<':
			// \x1b[<N>u  (N is optional digits, default 1)
			k := j + 1
			for k < len(chunk) && chunk[k] >= '0' && chunk[k] <= '9' {
				k++
			}
			if k < len(chunk) && chunk[k] == 'u' {
				count := 1
				fmt.Sscanf(string(chunk[j+1:k]), "%d", &count) //nolint:errcheck // best-effort parse; count defaults to 1
				if count < 1 {
					count = 1
				}
				if count >= len(p.kittyStack) {
					p.kittyStack = p.kittyStack[:0]
				} else {
					p.kittyStack = p.kittyStack[:len(p.kittyStack)-count]
				}
				newI = k + 1
				stripped = true
			}
		}

		if stripped {
			// Flush bytes before this sequence into out.
			if out == nil {
				out = make([]byte, 0, len(chunk))
			}
			out = append(out, chunk[copied:abs]...)
			copied = newI
			i = newI
		} else {
			// Not a kitty sequence; skip past ESC [ and continue.
			i = abs + 2
		}
	}

	if out == nil {
		return chunk // nothing stripped
	}
	// Flush remaining bytes.
	out = append(out, chunk[copied:]...)
	return out
}

// xParseColor converts an RGB hex string "rrggbb" (6 hex digits) to the
// XParseColor format "rgb:<rrrr>/<gggg>/<bbbb>" used in OSC 10/11 responses.
// Each 8-bit component is doubled to 16-bit by repeating the byte.
func xParseColor(rrggbb string) string {
	if len(rrggbb) != 6 {
		return "rgb:0000/0000/0000"
	}
	return fmt.Sprintf("rgb:%s%s/%s%s/%s%s",
		rrggbb[0:2], rrggbb[0:2],
		rrggbb[2:4], rrggbb[2:4],
		rrggbb[4:6], rrggbb[4:6])
}

const paneTermName = "xterm-256color"

// xtgettcapResponse builds the DCS response for a single XTGETTCAP capability
// query.  hexCap is the hex-encoded capability name as sent by the app.
// The response is "found" (DCS 1+r...) for capabilities we implement and
// "not found" (DCS 0+r...) for everything else.
func xtgettcapResponse(hexCap string, flags int, mode vt10x.ModeFlag) string {
	capBytes, err := hex.DecodeString(hexCap)
	if err != nil || len(capBytes) == 0 {
		return ""
	}
	// Capabilities we actively support — values are standard terminfo strings.
	// Smulx: extended underline style (SGR 4:N)
	// Setulc: underline colour (SGR 58:2::R:G:B)
	// Su: synonym for extended underline support
	caps := map[string]string{
		"Smulx":  "\x1b[4:%p1%dm",
		"Setulc": "\x1b[58:2::%p1%d:%p2%d:%p3%dm",
		"Su":     "\x1b[4:%p1%dm",
		"TN":     paneTermName,
		"name":   paneTermName,
		"Co":     "256",
		"colors": "256",
		"RGB":    "8",
	}
	name := string(capBytes)
	val, ok := caps[name]
	if !ok {
		if ev := capabilityKey(name); ev != nil {
			val = string(keyToBytesMode(ev, flags, mode))
			ok = val != ""
		}
	}
	if ok {
		hexVal := hex.EncodeToString([]byte(val))
		return "\x1bP1+r" + hexCap + "=" + hexVal + "\x1b\\"
	}
	return "\x1bP0+r" + hexCap + "\x1b\\"
}

// capabilityKey maps terminfo/termcap names to the same events used for input.
// Replies therefore follow the pane's negotiated modes, not the outer terminal.
func capabilityKey(name string) *tcell.EventKey {
	for _, entry := range []struct {
		info, cap string
		key       tcell.Key
	}{
		{"kcuu1", "ku", tcell.KeyUp}, {"kcud1", "kd", tcell.KeyDown},
		{"kcub1", "kl", tcell.KeyLeft}, {"kcuf1", "kr", tcell.KeyRight},
		{"khome", "kh", tcell.KeyHome}, {"kend", "@7", tcell.KeyEnd},
		{"kich1", "kI", tcell.KeyInsert}, {"kdch1", "kD", tcell.KeyDelete},
		{"kpp", "kP", tcell.KeyPgUp}, {"knp", "kN", tcell.KeyPgDn},
		{"kbs", "kb", tcell.KeyBackspace2}, {"kcbt", "kB", tcell.KeyBacktab},
	} {
		if name == entry.info || name == entry.cap {
			return tcell.NewEventKey(entry.key, 0, 0)
		}
	}
	if name == "kent" || name == "@8" {
		return tcell.NewEventKey(tcell.KeyEnter, 0, tcell.ModKeypad)
	}
	for n := 1; n <= 24; n++ {
		capName := ""
		switch {
		case n < 10:
			capName = fmt.Sprintf("k%d", n)
		case n == 10:
			capName = "k;"
		case n < 20:
			capName = "F" + string(rune('1'+n-11))
		default:
			capName = "F" + string(rune('A'+n-20))
		}
		if name == fmt.Sprintf("kf%d", n) || name == capName {
			return tcell.NewEventKey(tcell.KeyF1+tcell.Key(n-1), 0, 0)
		}
	}
	for _, entry := range []struct {
		name string
		key  tcell.Key
	}{
		{"kUP", tcell.KeyUp}, {"kDN", tcell.KeyDown},
		{"kLFT", tcell.KeyLeft}, {"kRIT", tcell.KeyRight},
		{"kHOM", tcell.KeyHome}, {"kEND", tcell.KeyEnd},
		{"kIC", tcell.KeyInsert}, {"kDC", tcell.KeyDelete},
		{"kPRV", tcell.KeyPgUp}, {"kNXT", tcell.KeyPgDn},
	} {
		modifier := 2
		if name != entry.name {
			if len(name) != len(entry.name)+1 || !strings.HasPrefix(name, entry.name) || name[len(name)-1] < '2' || name[len(name)-1] > '8' {
				continue
			}
			modifier = int(name[len(name)-1] - '0')
		}
		var mod tcell.ModMask
		if (modifier-1)&1 != 0 {
			mod |= tcell.ModShift
		}
		if (modifier-1)&2 != 0 {
			mod |= tcell.ModAlt
		}
		if (modifier-1)&4 != 0 {
			mod |= tcell.ModCtrl
		}
		return tcell.NewEventKey(entry.key, 0, mod)
	}
	return nil
}
