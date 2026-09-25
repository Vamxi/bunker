# AGENTS.md

> See [AGENTS.universal.md](./AGENTS.universal.md) and [AGENTS.go.md](./AGENTS.go.md) for universal conventions.
> Refresh: `make standards`

---

## Overview

bunker is a Linux-only GTK4 terminal written in Go, built on
[jsnjack/bunk](https://github.com/jsnjack/bunk). Every tab runs bunk's
multiplexer (PTY-backed panes, the vendored vt10x emulator, BSP splits,
scrollback, search, badges) and draws it with GtkSnapshot, so GTK renders it
on the GPU. `bunker tui` still runs the multiplexer inside another terminal.
It is used daily with Claude Code, Copilot CLI, btop, vim, and other TUI apps.

---

## Architecture

```
main.go             Entry point — calls Execute()
cmd.go              cobra root (GUI) and "tui" commands; run() for the TUI
cmd_config.go       "bunker config" subcommand tree
gui.go              GTK application and window, actions, config watch/reload/apply
gui_tabs.go         Tabs: one App + termView per tab in a GtkStack; tab strip, menu
gui_view.go         termView widget: fonts, sizing, frame capture, release on close
gui_panes.go        Split-tree drawing: per-pane frames, separators, badges, search bar
gui_glyphs.go       Procedural box drawing, blocks, braille
gui_input.go        GDK keys → tcell events → keyToBytesMode; mouse; clipboard
gui_ime.go          Input methods (dead keys, Compose, CJK, emoji picker)
gui_links.go        Hyperlinks: OSC 8 and plain URLs, wrapped across rows
gui_settings.go     Preferences window (writes keys through tomledit.go)
gui_style.go        Static CSS and theme-derived window chrome
gui_debug.go        BUNKER_SCREENSHOT / BUNKER_KEYS / BUNKER_CPUPROFILE hooks
desktop.go          Embedded icon, `install-desktop`, `themes` commands
themes.go           Bundled Ptyxis palettes (themes/ptyxis)
tomledit.go         Comment-preserving single-key TOML edits, atomic config writes
app.go              App (one tab's pane model), event loop, key handling
input_modes.go      Application cursor/keypad encoding and focus forwarding
pane.go             Pane: PTY spawn, readPTY, captureAndWrite, query replies
ptystream.go        Bounded UTF-8/control framing before PTY parsing
osc.go              OSC pre-scanner, passthrough of OSC 7/52/133
graphics.go         Image colour blending, virtual pixels, raw-history trimming
scrollback.go       sbRing ring buffer of scrolled-off rows
reflow.go           Reflow helpers, scroll anchoring, stripAltScreen()
layout.go           BSP tree: Node, split/remove/resize math
render.go           TUI rendering through tcell
mouse.go            Mouse events → PTY byte sequences
status.go           Status badges (scroll, container, SSH, flash messages)
search.go           In-pane text search
config.go           TOML config, theme registry, keybinding resolution
hostcolors.go       Host terminal colour probing (TUI)
clipboard.go        OSC 52 clipboard passthrough
cellaspect.go       Cell pixel aspect ratio detection
logger.go           slog setup for --debug / --trace

internal/vt10x/     VT100/ANSI emulator (fork of github.com/hinshun/vt10x)
  state.go          State, Cursor, Glyph, rows with used counts, setAttr, setMode
  parse.go          Parser state machine and the ASCII fast path
  csi.go            CSI dispatcher
  str.go            OSC, DCS, APC strings
  grapheme.go       Incremental grapheme assembly
  uniinfo.go        Per-rune width and grapheme table filled from uniseg
  graphics.go       Image-to-cell painting, Kitty placement deletion
  status.go         DECRQSS setting serialization
  vt.go             Terminal / View interfaces, options, New()
  vt_posix.go       terminal type, Write (POSIX)
  color.go          Color type and named colours
internal/graphics/  Bounded SIXEL, Kitty, and iTerm2 static image decoders
internal/benchdata/ Benchmark workloads (seq, prose, colors, unicode, tui)

third_party/tcell/  tcell v2.13.9 with bunk's patches (BUNK_PATCHES.md)
third_party/gotk4/  gotk4 v0.4.1 with subclass fixes (BUNKER_PATCHES.md)
themes/ptyxis/      The 20 bundled palettes
assets/icon/        App icon in every hicolor size
scripts/            headless-gui.sh (private compositor for GUI tests)
```

Local extensions to vt10x:
- SGR 2/8/9/21/53/58 and 4:N underline styles; DECSCUSR cursor shapes.
- DECSET 2004 bracketed paste, 2026 synchronized updates, 12 cursor blink,
  2027 grapheme widths (on by default).
- `QueryPrivateMode(n)` answers DECRQM for every tracked private mode; unknown
  modes return status 0, not 4. DECRQSS reports SGR, scroll margins, and
  cursor style, and a negative response for anything else.
- `\x1b[?4m` no longer misfires as SGR 4.
- Coordinates are display cells: wide characters have a lead cell and a
  continuation. `Glyph.Combining` holds the rest of a grapheme, up to 1 KiB.
- `ReplaceScreen` keeps modes, attributes, callbacks, and hyperlinks through
  reflow.
- Rows record how many leading cells are used; the rest are blank, so
  clearing and scrolling cost a line's length, not the width.
- Character widths and grapheme starts come from `uniInfo`, a table filled
  from uniseg on first use.

---

## Key Flows

1. **Startup** — `main()` → `Execute()` → `runGUI()` loads the config, sets
   up slog, and runs a `GtkApplication`; activation builds the window and the
   first tab (`newTab`: an `App`, a `termView`, and the first `Pane`).
   `bunker tui` goes to `run()` instead, which drives the same `App` through a
   tcell screen.
2. **Pane output** — `readPTY` reads up to 32 KiB, frames it (`ptyStream`),
   passes OSC 7/52/133 on (`osc.go`), then `captureAndWrite` splits out
   terminal queries, answers them in stream order, and feeds the rest to
   vt10x. Rows leaving the top go straight into the scrollback ring
   (`onScrollSwap`). The view is asked to redraw through `glib.IdleAdd`.
3. **Drawing** — on each frame `termView.snapshot` copies every visible pane
   under its lock (`paneFrame.capture`), then draws rows, cursor, selection,
   links, search matches, badges, and separators.
4. **Input** — GDK key presses become tcell events and go through
   `App.handleKey`, the same path as the TUI; the input method handles text
   entry first. Mouse events go through `App.handleMouse`.
5. **Resize and reflow** — the view computes the grid from its allocation;
   `Pane.resizeAndReflow()` replays the raw output (`rawBuf`) into a scratch
   terminal at the new width and installs the result with `ReplaceScreen`.
6. **Config** — the config file is the source of truth. Preferences writes one
   key (`guiWin.setKey`), the directory monitor notices, `reload` parses the
   file, and `apply` updates every window.

**Lock ordering:** always `app.mu` before `Pane.mu`. Never acquire `app.mu`
while holding `Pane.mu`.

---

## Build & Run

```bash
make check      # fmt → vet → test → build → lint
make test       # race-enabled tests, GUI tests on a headless compositor, then
                #   the exhaustive tests in full without -race
make test-gui   # GUI integration tests only
make bench      # benchmarks, compared with bench/baseline.txt
make build      # bin/bunker (linux/amd64, cgo + gtk4-devel)
make run        # build and run with --trace
make install    # ~/.local/bin/bunker plus the desktop entry
make rpm        # bin/bunker-VERSION-1.x86_64.rpm (needs nfpm)
make release VERSION=x.y.z   # tests, RPM, upload, publish CHANGELOG notes
```

TESTING.md lists every suite, the GUI harness, benchmarks, allocation guards,
and test recipes. Add a `TestGUI_…` for each user-visible feature. Use
`make test` rather than `go test -race` (TESTING.md explains checkptr).

Manual checks for rendering and input changes use bunk's
`terminal_features.sh` (`text`, `colors`, `cursor`, `integration`, `queries`,
`osc`, `graphics` sections).

---

## Configuration

Config file: `~/.config/bunker/config.toml` (`--config` / `-c` overrides it).
`bunker config init` writes a documented default. A malformed file or a
missing explicit `--config` path fails at startup; a missing default file uses
built-in defaults. In the GUI, an invalid edit keeps the current settings and
shows a banner.

- `theme` — `system` (default: GNOME's palette, light or dark with the
  desktop) or one of `bunker themes`.
- `font` — Pango font description; `[window] padding`.
- `[tabs]` — `position` (left, right, top, bottom), `width`, `autohide`,
  `collapsed`.
- `scrollback` (lines, default 10 000) and `scrollback_mb` per pane.
- `[keys]` — action → key overrides; `[ui]` — border and scrollbar colours.
- `log_file` (the `--trace` file, default `/tmp/bunker.log`), `log_level`
  (without flags, default `warn`), `cell_aspect`.

`config.go` is the reference for every field and its default.

---

## Design Decisions

- GTK runs on the main thread only; goroutines reach it through
  `glib.IdleAdd`.
- The GUI reuses bunk's `App` (one per tab) without a tcell screen, so keys
  and mouse behave the same in both frontends. Screen-dependent code is
  behind hooks (`sizeFn`, `onEmpty`); new `App` code must not assume a screen.
- The config file is the source of truth, and `apply` is idempotent because
  every write also triggers the directory monitor.
- Panes get `cols+1` columns: `Pane` reserves its last column for the TUI
  scrollbar, and the GUI draws its scrollbar in the padding.
- Query replies belong to the pane. They are generated in stream order in
  both screens, including SSH panes, and never delegated to the host.
- Graphics are ordinary cells: SIXEL, Kitty, and iTerm2 images become
  half-block glyphs that clip, scroll, and reflow with the grid. No image
  escape reaches the host; Kitty file and shared-memory transfers are
  rejected; transfers, images, and caches are size-capped.
- Rows track their used length so scrolling short lines is cheap; every cell
  write goes through `occupy`, and the scroll-swap callback carries used
  counts to and from the scrollback ring.
- Unicode widths and grapheme starts come from a table filled from uniseg
  itself, so the answers are uniseg's; only runes that may join a cluster go
  through full segmentation.
- The PTY pre-scanners jump between ESC bytes with `bytes.IndexByte`; their
  byte-at-a-time originals are kept in `scanref_test.go` as references.
- Keyboard passthrough (Ctrl+F12) is per pane and also hands window shortcuts
  to the program; the PASS badge takes priority over other badges.
- Kitty keyboard state belongs to the foreground process group that
  negotiated it.
- `--debug` logs to stderr and `--trace` to `/tmp/bunker.log`, as the
  standards say. Trace logs contain terminal output and /tmp is shared, so
  the file is used only if it is a regular file this user owns, never
  through a symlink or a FIFO, at mode 0600 (see SECURITY.md).
- Errors nobody can act on (a write to a program that has exited) are
  logged at trace level through `Pane.feed`, `Pane.writePTY`, and
  `State.reply` rather than ignored.
- Releases take an explicit `VERSION` and require a matching CHANGELOG.md
  section, which becomes the release notes. A release carries one RPM
  (`nfpm.yaml`): grm installs it with dnf, which brings the launcher, icons,
  and GTK in one package. `make rpm` stages the launcher and icons with
  `bunker install-desktop --data-dir … --exec /usr/bin/bunker`, so source
  installs and the RPM ship the same files; `TestNFPMShipsWhatInstallDesktopStages`
  keeps nfpm.yaml in step.
- Only linux/amd64 is built: GTK is linked through cgo.
- Every pane exports `BUNK=1`, so `bunk` and `bunker tui` refuse to nest
  inside bunker, and shell rc files can skip auto-starting bunk.

---

## Gotchas

- gotk4 keeps a Go-subclassed widget (`termView`) alive until GLib disposes
  it, and the Wayland input method holds its widget. Closing a tab clears the
  IM client, disposes the view and the row, and drops the tab's panes;
  `TestGUI_NoLeaks` guards it.
- Never retain `gsk.RenderNode` values: gotk4 wraps them with GObject
  refcounting although they are not GObjects.
- `third_party/gotk4` is a separate module (`./...` does not test it), and a
  change under `core/glib` recompiles GTK, which takes about 9 minutes.
- Cursor shape and cell content must be read under the same `Pane.mu`
  acquisition; `TestReadPTYSingleLock_ClaudeCursorRace` guards it.
- In the TUI, bunk owns the terminal: never log to stderr; diagnostics go
  through slog to the log file.
- Fix emulator bugs in `internal/vt10x` rather than working around them.
- `TERMINAL_AUDIT.md` records which terminal features are implemented,
  partial, or missing; update it when a feature changes.

---

## Known Issues

- Coloured output is about 1.4× slower than Ptyxis and Ghostty: bunk's
  pre-scanners still read every byte before the emulator does.
- `bunker tui` duplicates `bunk` itself.
