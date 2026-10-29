# AGENTS.md

> See [AGENTS.universal.md](./AGENTS.universal.md) for universal conventions.
> Refresh: `make standards`

---

## Overview

bunker is a Linux-only GTK 4 terminal written in Rust. Tabs sit in a
sidebar (or a bar) that shows what each one is doing: a circle with its
colour tag and state (a coding agent working or waiting, a command running
or finished, a bell), the directory, git branch, SSH host, and progress.
Text is shaped with Pango and drawn as GSK nodes, so GTK renders it on the
GPU. It is used daily with Claude Code, Copilot CLI, btop, vim, and other
TUI apps.

Until 0.7 bunker was written in Go on top of
[bunk](https://github.com/jsnjack/bunk); that code is in the git history.
Splits, zoom, and pane navigation have not been rebuilt yet.

---

## Architecture

```
Cargo.toml              workspace: crates/vt, crates/bunker
crates/vt/              bunker-vt: the emulator core, no I/O, no GTK
  parser.rs             byte state machine; SWAR scan for printable runs, a
                        fast path for whole control sequences
  term.rs               Terminal: cursor, modes, scrolling, editing, events,
                        image placement
  csi.rs                CSI dispatch, SGR, mode reports, kitty keyboard stack
  osc.rs                OSC (title, cwd, links, colours, 52, 133, notify), DCS
  graphics.rs           SIXEL, kitty graphics, iTerm2 images: decoding, cache,
                        placement sizes, limits
  grid.rs               Row (cells, grapheme tails, images, id), Grid, Scrollback
  cell.rs               8-byte Cell, Style, StyleTable (interned, compacted)
  reflow.rs             resize: rewraps history from cells, no replay
  search.rs             find in history and screen, across soft wraps
  select.rs             text of ranges, word and line bounds
  input.rs              key, mouse, paste, and focus encoding
  colors.rs             Rgb, Theme, Palette with OSC overrides
  unicode.rs            widths and grapheme joining (tables from build.rs)
  tests.rs              behaviour tests: bytes in, screen and replies out
  build.rs              width/join tables generated from unicode-segmentation
  examples/             workloads.rs (benchmark data), throughput.rs (MB/s)
crates/bunker/          the GTK 4 app (gtk4-rs)
  main.rs               flags, logging, config, then a command or a window
  cli.rs                bunker config / themes / install-desktop; icons
  session.rs            PTY reader/writer/watcher threads around a Terminal
  pty.rs                openpt, spawn with setsid + TIOCSCTTY, resize, tcgetpgrp
  procinfo.rs           cwd, command, git branch, ssh host, coding agent
  config.rs             the config file: parse, and single-key saves that keep
                        comments (toml_edit), written atomically at mode 0600
  theme.rs              bundled Ptyxis palettes (themes/ptyxis)
  log.rs                --debug / --trace logging
  ui/mod.rs             application, config file watch and live apply
  ui/window.rs          window, tab list with live tab info, rename, sidebar
                        resize, alerts and desktop notifications
  ui/strip.rs           tab list layout: dragging a tab, the others sliding
  ui/indicator.rs       tab circle: colour ring, state inside, joining lines
  ui/view.rs            TermView widget: drawing, keys, IME, mouse, selection,
                        search highlights, images, passthrough
  ui/render.rs          row drawing: Pango shaping snapped to cells, GSK nodes
  ui/glyphs.rs          box drawing, blocks, braille drawn as shapes
  ui/search.rs          the Ctrl+F bar
  ui/keys.rs            GDK keys → vt keys; configurable shortcuts ([keys])
  ui/prefs.rs           Preferences window; each control saves one key
  ui/themes.rs          theme picker: drawn previews, nine per page
  ui/images.rs          PNG/JPEG/GIF and zlib through gdk-pixbuf and GIO
  ui/style.rs           theme-derived CSS
  ui/debug.rs           BUNKER_INPUT / BUNKER_SCREENSHOT hooks
themes/ptyxis/          the bundled palettes
assets/icon/            app icon in every hicolor size (built into the binary)
scripts/                headless-gui.sh, termbench.sh, terminal_features/
```

---

## Key Flows

1. **Startup** — `main()` parses flags; a subcommand runs in `cli.rs`,
   otherwise `ui::run` starts a `GtkApplication`. Activation opens a
   window with one tab: a `Session` (PTY and threads) and a `TermView`.
2. **Output** — the reader thread reads up to 64 KiB, locks the
   `Terminal`, parses, takes its replies (sent through the writer thread),
   and wakes the UI with `MainContext::invoke`, at most once per frame.
3. **Drawing** — `View::draw` copies the rows that changed under the lock,
   then shapes them after releasing it. Row drawings are cached by row
   identity, so scrolled rows are reused. Images are textures cached by
   image id.
4. **Input** — keys go through window shortcuts, then the input method,
   then `bunker_vt::encode_key` with the terminal's modes.
5. **Resize** — the view computes the grid from its allocation; the
   terminal rewraps history and keeps the cursor's line as it was (see
   Design Decisions), then the PTY gets the new size.
6. **Config** — the config file is the source of truth. Preferences and the
   sidebar drag save one key; Preferences also applies the change at once,
   and the file watch in `ui/mod.rs` applies edits from anywhere.

---

## Build & Run

```bash
make check      # fmt → test → build → lint (rustfmt and clippy needed)
make test       # cargo test --workspace
make build      # target/release/bunker
make bench      # emulator MB/s on the benchmark workloads
make termbench  # the workloads in real windows: bunker, Ptyxis, foot (ms)
make run        # build and run with --trace
make install    # ~/.local/bin/bunker plus the desktop entry
make rpm        # target/bunker-VERSION-1.x86_64.rpm (needs nfpm)
make release VERSION=x.y.z   # tests, RPM, upload, publish CHANGELOG notes
```

rustfmt and clippy come from `sudo dnf install rustfmt clippy`. TESTING.md
lists the tests and how to check the window by hand.

---

## Configuration

Config file: `~/.config/bunker/config.toml` (`--config` / `-c` overrides
it). `bunker config init` writes a documented default; `bunker config
list/get/set` reads and edits one setting, checked. A malformed file fails
at startup; in a running window, an invalid edit keeps the current settings
and logs a warning.

- `theme` — `system` (GNOME's palette, light or dark with the desktop) or
  one of `bunker themes`. `font` — a Pango description. `scrollback`.
- `[window] padding`; `[tabs]` — `position`, `width`, `autohide`.
- `[notify]` — `desktop` (unfocused, always, never), `command_seconds`, `bell`.
- `[keys]` — every shortcut (`ui/keys.rs` ACTIONS), as `ctrl+shift+t`, `f2`,
  or `none`.

`config.rs` is the reference for every field and its default.

---

## Design Decisions

- GTK runs on the main thread only. Nothing there may block: PTY reads and
  writes have their own threads, `/proc` and git are read by the session's
  watcher thread, and config files are written with `gio::spawn_blocking`.
- One `Terminal` per session sits behind a mutex. The reader parses under
  it; drawing copies what changed and shapes after releasing it, so a busy
  program never waits on the UI.
- Rows carry an identity that is renewed (once per parse batch) when the
  row changes. The renderer caches each row's drawing by identity.
- Cell sizes are whole device pixels at the real (fractional) scale, and
  the grid's origin is moved onto a pixel; box-drawing lines snap to device
  pixels. Otherwise touching backgrounds leave hairline seams at 125%.
- On resize, history is rewrapped from the cells. The cursor's line and the
  rows below it are only cut or padded, as xterm does: the program there
  (readline, a TUI on the main screen) redraws them assuming that, and
  rewrapping them makes the redraw leave pieces of old prompts.
- Unicode widths and grapheme joining come from tables `build.rs`
  generates with unicode-segmentation and unicode-width, so the fast path
  agrees with full segmentation (tests check every code point).
- Images belong to the row they start on: they scroll, rewrap, and are
  erased with it. They are drawn as GPU textures over the cells they
  cover; those cells are cleared when an image is placed.
- Mouse/focus reporting and kitty keyboard flags belong to the process group
  that set them (`session.rs`): after an ssh drop the shell gets neither.
- A coding agent's state comes from its title where it says (Claude Code
  starts it with ◐/◑ while working, ✳ while waiting); otherwise from
  output, ignoring output right after typing, mouse reports, or focus.
- Query replies come from terminal state; the title is never reported
  back. XTVERSION answers VTE(8203) and panes export VTE_VERSION, since
  programs enable features by terminal name.
- Desktop notifications use the freedesktop backend (set for bunker, kept
  out of child programs): windows are non-unique, so GNOME could not call
  back a click.
- Dependencies are gtk4, libc, unicode-width, unicode-segmentation, and
  toml_edit; anything more needs agreement first.

---

## Gotchas

- Removing a widget while it is losing focus (the rename entry in its
  focus-leave handler) hangs GTK walking a destroyed widget; remove it from
  an idle callback.
- A `GestureDrag` on a widget that moves (the sidebar handle) reports
  offsets in its moved coordinates; take positions in the window's.
- A widget's `hexpand` propagates up from its children: set it explicitly
  on containers that must not grow (the sidebar).
- A widget's `scale_factor` is an integer; the surface's `scale()` is the
  fractional one cell sizes must use.
- Dev windows started from an agent's shell inherit its environment
  (CLAUDECODE, CLAUDE_CODE_CHILD_SESSION, …); start them without it.
- `TERMINAL_AUDIT.md` records which terminal features are implemented;
  update it when a feature changes.

---

## Known Issues

- Splits, zoom, and pane navigation are not rebuilt yet.
- Cursor blinking, the input method's preedit text, and `[ui]` border and
  scrollbar colours are not implemented.
