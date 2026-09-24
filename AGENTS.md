# AGENTS.md

> See [AGENTS.universal.md](./AGENTS.universal.md) and [AGENTS.go.md](./AGENTS.go.md) for universal conventions.
> Refresh: `make standards`

---

## Overview

bunker is a Linux-only GTK4 terminal written in Go, forked from
[jsnjack/bunk](https://github.com/jsnjack/bunk). `bunker` opens a GTK window
that draws a bunk pane through GtkSnapshot (GPU); `bunker tui` still runs
bunk's multiplexer inside an existing terminal. Both share the core: real
PTY-backed panes, the vendored vt10x emulator, the BSP layout, scrollback.
It is used daily with Copilot CLI, Claude Code, btop, vim, and other TUI apps.

---

## Architecture

```
main.go             Entry point — calls Execute()
cmd.go              cobra root (GUI) and "tui" commands, run() for the TUI
gui.go              GTK application, window, actions, config watch/reload/apply
gui_tabs.go         Tabs: one App + termView per tab in a GtkStack; tab strip
gui_settings.go     Preferences window (writes keys via tomledit.go)
tomledit.go         Comment-preserving single-key TOML edits, atomic config writes
gui_view.go         termView widget: frame capture under Pane.mu, snapshot drawing
gui_glyphs.go       Procedural box drawing, blocks, braille
gui_input.go        GDK keys → tcell events → keyToBytesMode; mouse; clipboard
gui_debug.go        BUNKER_SCREENSHOT (C helper) and BUNKER_CPUPROFILE hooks
cmd_config.go       "bunk config" subcommand tree
app.go              App struct, event loop, key handling (keyToBytes), triggerRedraw()
input_modes.go      Application cursor/keypad encoding and host focus forwarding
pane.go             Pane struct, PTY spawn, readPTY, captureAndWrite, scrollback capture
ptystream.go        Bounded streaming UTF-8/control framing before PTY parsing
render.go           render(), renderPane(), vtColor(), border/scrollbar drawing
graphics.go         Image colour blending, virtual pixels, control-safe history trimming
layout.go           BSP tree: Node, split/remove/resize math
scrollback.go       sbRing ring buffer, scroll-detection algorithm
reflow.go           Reflow helpers, scroll anchoring, stripAltScreen()
osc.go              OSC pre-scanner, passthrough of OSC 7/52/133 to host
mouse.go            Mouse events → PTY byte sequences
status.go           Status badges (scroll count, container, SSH, flash messages)
search.go           In-pane text search
config.go           TOML config, theme registry, keybinding resolution
hostcolors.go       Host terminal OSC colour probing
clipboard.go        OSC 52 clipboard passthrough
logger.go           Structured logging (slog)
cellaspect.go       Cell pixel aspect ratio detection

internal/vt10x/     Vendored VT100/ANSI emulator (fork of github.com/hinshun/vt10x)
  state.go          State struct, Cursor, Glyph, ModeFlag constants, setAttr, setMode
  csi.go            CSI escape sequence dispatcher (handleCSI)
  str.go            String escape handler (OSC, DCS, APC)
  parse.go          Byte-by-byte parser state machine (put)
  vt.go             Terminal / View interfaces, New() constructor
  vt_posix.go       terminal concrete type, Write, Parse (POSIX)
  color.go          Color type, named colour constants
  grapheme.go       Incremental grapheme assembly and bounded cell text
  status.go         DECRQSS setting serialization
  graphics.go       Image-to-cell painting, Kitty placement deletion

internal/graphics/ Bounded SIXEL, Kitty, and iTerm2 static image decoders

third_party/tcell/  Pinned tcell v2.13.9 with overline/keypad/keycap-width patches
                    (local go.mod replacement; see BUNK_PATCHES.md)
third_party/gotk4/  gotk4 v0.4.1 with subclass-override fixes (BUNKER_PATCHES.md)

assets/             Demo assets (gif)
scripts/            Manual regression helper scripts
```

Key local extensions to vendored vt10x:
- SGR 2/8/9/21/53/58 and 4:N underline styles
- `Cursor.Shape` (DECSCUSR)
- `ModeSetPaste` (DECSET 2004), `ModeSync` (DECSET 2026)
- `QueryPrivateMode(n)` — returns DECRQM status byte for any tracked private mode
- Private-parameter SGR guard (`\x1b[?4m` no longer misfires as SGR 4)
- Display-cell widths and wide-glyph continuations; `ReplaceScreen` preserves
  terminal modes, attributes, callbacks, and hyperlink identities during reflow
- DEC 12 cursor blink; DEC 2027 grapheme widths (enabled by default).
  `Glyph.Combining` stores an immutable grapheme suffix, bounded to 1 KiB/cell.
- DECRQSS reports SGR, scroll margins, and cursor style; unsupported settings
  return a negative response. Unknown DECRQM modes return status 0, not 4.

---

## Key Flows

1. **Startup** — `main()` → `Execute()` → `run()` loads config, initialises
   slog, queries cell aspect, probes host OSC colours, initialises tcell,
   spawns the first `Pane` and enters `App.eventLoop()`.
2. **Pane I/O** — three goroutines per pane: `readPTY` (PTY → vt10x →
   redraw), `waitForExit`, `trackFgProcess`. One shared `renderLoop` drains
   `app.redraw` (buffered 1).
3. **Resize / reflow** — `App.handleResize()` coalesces host resizes and
   updates the BSP tree. `Pane.resizeAndReflow()` replays `rawBuf` into a
   scratch grid, then replaces the live grid without resetting terminal state.
4. **OSC passthrough** — bounded `ptyStream` framing precedes `osc.go` scanning.
   OSC 7/52/133 reach the host through `app.oscBuf`; OSC 8 links are stored on
   glyphs and emitted with their rendered text.

**Lock ordering:** always `app.mu` before `Pane.mu`. Never acquire `app.mu`
while holding `Pane.mu`.

---

## GUI notes

- GTK calls stay on the main thread; goroutines reach it only via
  `glib.IdleAdd` (see `termView.requestDraw`).
- The GUI reuses `App` as the pane model (one per tab) with no tcell screen:
  never call code paths that touch `app.screen` from GUI code.
- The config file is the source of truth. GUI changes go through
  `guiWin.setKey` → file → `reload` → `apply`; `apply` must stay idempotent
  because every write also triggers the directory monitor.
- Panes get `cols+1` columns because Pane reserves its last column for the
  TUI scrollbar; the GUI draws its scrollbar in the padding instead.
- Never retain `gsk.RenderNode` values: gotk4 wraps them with GObject
  refcounting although they are not GObjects. Pango layouts are cached.
- `third_party/gotk4` is a separate module: `./...` does not test it, and a
  change under `core/glib` recompiles GTK (~9 min).

## Build & Run

```bash
make check    # full validation gate (fmt → vet → race tests → build → lint)
make test     # tests only (race-enabled)
make build    # native linux/amd64 bin/bunker (cgo + gtk4-devel)
make run      # builds local binary and runs it with --trace
```

Tests run with `-race` and are required to pass before any binary is built.
`make test` includes the locally patched tcell module and its terminfo tests.
The gate is serialized even with `make -j`; the project-specific test-before-
build rule takes precedence over the generic gate ordering.

Manual regression scripts (run for changes that touch the listed areas):
- `bash terminal_features.sh text` — SGR/style changes in `render.go` or `internal/vt10x/state.go`
- `bash terminal_features.sh colors` — ANSI/256/RGB/underline-colour changes
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh cursor` — cursor-shape, width, emoji, reflow
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh integration` — hyperlink, bracketed-paste, OSC 133
- `bash terminal_features.sh queries` + `bash terminal_features.sh osc` — OSC, DECRQM, capability queries, kitty-keyboard
- `TERMINAL_FEATURES_AUTO=1 bash terminal_features.sh graphics` — image decoding, clipping, resize, scrollback

---

## Configuration

Config file: `~/.config/bunk/config.toml` (override with `--config`). Generate
a documented default with `bunk config init`.
Malformed/unreadable files and missing explicit `--config` paths fail at startup;
a missing default file uses built-in defaults.

Key fields agents may need to know about:
- `theme` — built-in name (`terminal`, `default`, `solarized-dark`, `dracula`, `nord`) or custom palette
- `scrollback` — per-pane scrollback line cap (default 10 000)
- `log_file` — destination for `--debug` / `--trace` output (default `/tmp/bunk.log`)
- `cell_aspect` — fallback height/width ratio when the host terminal doesn't answer the pixel-size query
- `[keybindings]` — overrides for split / zoom / quit / search / copy / paste
- `[ui]` — hex colour overrides for borders and scrollbar

`config.go` is the authoritative reference for every field and its default.

---

## Design Decisions

- **Keyboard passthrough is per pane.** Ctrl+F12 (`[keys].passthrough`) toggles
  forwarding of all other keys through the normal terminal encoder. The toggle
  remains reserved and exits bunk search. Mouse handling is unchanged. The PASS
  badge shares the status layout, takes highest priority, and invalidates the
  render overlay when toggled.

- **PTY is one column narrower than the pane.** The rightmost column
  (`p.x + p.w - 1`) is reserved for the scrollbar; the PTY never writes there.
- **Cursor shape and cell content must be read under the same lock.** Both
  live inside `p.term`; `render()` must read them within the same `p.mu`
  acquisition so `readPTY` can't sneak in a write between the two. Guarded by
  `TestReadPTYSingleLock_ClaudeCursorRace`.
- **vt10x coordinates are display cells.** Wide characters occupy a lead cell
  (`Glyph.Width == 2`) and a continuation (`-1`); `-2` marks padding before a
  wide-character wrap. Rendering, search, and selection skip continuations.
  Combining marks, variation selectors, and multi-codepoint emoji stay together
  in the lead cell through render, copy, search, and reflow. Mode 2027 can switch
  back to per-codepoint widths for legacy applications.
- **Query replies belong to the pane.** They are generated in stream order in
  both primary and alternate screens, including SSH/mosh panes; they are never
  delegated to the outer terminal. Unknown host colour defaults remain unknown.
  XTGETTCAP key replies use the normal input encoder with the current pane modes
  and Kitty flags; its terminal-name reply matches the TERM exported to panes.
- **Graphics are ordinary cells.** Static SIXEL, Kitty, and iTerm2 images become
  half-block glyphs with immutable `Glyph.Image` samples. They clip, erase, scroll,
  and reflow with the grid; source bitmaps are not retained by scrollback. No image
  escape is forwarded to the host. Kitty file/shared-memory access is rejected.
  PTYs and CSI 14/16/18 replies use virtual pixels (8 wide, height from cell aspect).
  Transfers are capped at 8 MiB, decoded images at 4 million pixels / 4096 per
  axis, and Kitty caches at 32 MiB / 32 images. Graphics-bearing raw history gets
  a 16 MiB minimum budget and is never trimmed inside a control sequence.
- **Cursor colour follows the active pane.** OSC 12 overrides and OSC 112 resets
  are emitted through tcell; shutdown restores the host cursor colour.
- **Synchronized updates are per pane.** Other panes continue repainting;
  an abandoned update is released after one second.
- **Kitty keyboard state belongs to its negotiating foreground process group.**
  Polling clears it only after that owner leaves the foreground.
- **Terminal cleanup runs synchronously in `main` after the event loop.**
  Background goroutines can't be relied on to finish their cleanup before
  the process exits.
- **Nested sessions are refused.** `BUNK=1` is exported into every pane's
  environment; the binary exits at startup if it sees it set.

---

## Gotchas

- TUI owns stderr — do not write logs there. All diagnostic output goes
  through `slog` to the trace file (`/tmp/bunk.log`). Errors that the user
  needs to see surface through the UI.
- Vendored vt10x has local patches. Prefer fixing bugs there over working
  around them in the main code.
- `TERMINAL_AUDIT.md` is the authoritative reference for which terminal
  features are implemented, partial, or missing. When investigating a
  rendering or input bug, consult it first; when fixing a feature, update it
  to reflect the new status.

---

## Test recipes

**Minimal Pane (no PTY):**
```go
term := vt10x.New(vt10x.WithSize(cols, rows))
p := &Pane{
    term:            term,
    cmd:             &exec.Cmd{},  // non-nil prevents cwd() nil-deref in emitTitle
    x: 0, y: 0, w: cols, h: rows,
    scrollbackLines: 100,
    sb:              sbRing{maxLines: 100},
}
```

**Simulation screen:**
```go
scr := tcell.NewSimulationScreen("UTF-8")
scr.Init()
defer scr.Fini()
scr.SetSize(w, h)
// scr.GetContent(x, y) → (mainc rune, combc []rune, style, width)
```

**Minimal App for calling render():**
```go
app := &App{
    screen: scr,
    root:   &Node{pane: p},
    active: p,
    oscBuf: newOSCBuffer(),
    theme:  testTheme(),
}
```
