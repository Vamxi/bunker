# bunker

A light, fast GTK4 terminal for Linux. No Electron, no web view: one Go
binary that draws through GTK's GPU renderer (Vulkan/GL).

> **Built on [bunk](https://github.com/jsnjack/bunk) by
> [jsnjack](https://github.com/jsnjack).** bunker is a fork of bunk: the
> PTY handling, the VT emulator, scrollback, reflow, selection, and the
> compatibility work that keeps Claude Code, Copilot CLI, vim, and btop
> rendering correctly all come from bunk. bunker puts a native window around
> it.
>
> **If you want these features inside the terminal you already use, use
> [bunk](https://github.com/jsnjack/bunk) instead.** It adds split panes,
> context-aware splits, search, and scrollback to any terminal.

## Status

Early. What works today:

- **bunk's multiplexer, native in every tab:** split panes (F1, direction
  chosen in pixels), context-aware splits into the same container / SSH host
  / sudo (Alt+F1), zoom (F12), pane focus (Alt+arrows), incremental search
  (Ctrl+F), keyboard passthrough (Ctrl+F12), and bunk's status badges, all
  drawn by bunker on the GPU and configured with bunk's `[keys]`
- The header bar shows the focused pane's directory and context; tab rows
  show the context and how many panes a tab has
- GPU rendering through GtkSnapshot/GSK; draws only on the display's frame clock
- Truecolor, bold/italic/dim, underline styles (single, double, curly, dotted,
  dashed) with underline colour, strikethrough, overline
- Wide characters, emoji sequences (ZWJ, flags, skin tones, keycaps), combining marks
- Box drawing, block elements, and braille drawn procedurally, so TUI borders
  are seamless at any font size
- Keyboard through bunk's encoder: kitty keyboard protocol, application
  cursor/keypad modes, modifiers
- Mouse selection, double-click word, wheel scrollback, mouse reporting to apps
- Input methods: dead keys and Compose, CJK input with the candidate popup at
  the cursor, GNOME's emoji picker, Ctrl+Shift+U
- Clipboard, bracketed paste, font zoom, scrollback
- Tabs as a sidebar (left/right) or a bar (top/bottom), coloured from the
  terminal theme, with titles from the running program
- Preferences window (Ctrl+,) that edits the config file in place; edits from
  any editor apply live

Planned layers: theme importers (Ghostty format first), editable key
bindings.

## Install

bunker needs GTK 4 at runtime (every GNOME desktop already has it).

```bash
grm install Vamxi/bunker
```

## Keys

| Key | Action |
|---|---|
| `F1` / `Alt+F1` | Split the pane / split into the same container, SSH host, or sudo |
| `F12` | Zoom the pane |
| `Alt+←↑→↓` | Move between panes |
| `Ctrl+F` | Search (Enter / Ctrl+N next, Ctrl+P previous, Esc exit) |
| `Ctrl+C` / `Ctrl+V` | Copy the selection (else sent to the program) / paste |
| `Ctrl+F12` | Keyboard passthrough for the pane (PASS badge) |
| `Ctrl+Shift+T` / `Ctrl+Shift+W` | New tab (in the current directory) / close tab |
| `Ctrl+PgUp` / `Ctrl+PgDn` | Previous / next tab (middle-click a tab to close it) |
| Right-click a tab | Rename, reset name, close, close other tabs |
| Sidebar button (header bar) | Collapse the tab sidebar to one character per tab (number, or the first letter of a custom name); `[tabs] collapsed` sets the default |
| `Ctrl+,` | Preferences |
| `Ctrl+Shift+C` / `Ctrl+Shift+V` | Copy / paste (`Shift+Insert` also pastes) |
| `Ctrl+Shift+=` / `Ctrl+Shift+-` / `Ctrl+Shift+0` | Font bigger / smaller / reset |
| `Shift+PgUp` / `Shift+PgDn` | Scroll history |
| Mouse drag / double-click | Select text / select word (Shift overrides app mouse mode) |

Everything else goes to the application.

## Usage

```bash
bunker                     # your login shell in a new window
bunker -- btop             # run a program instead of the shell
bunker tui                 # bunk's multiplexer in the current terminal
BUNKER_FONT="JetBrains Mono 12" bunker
```

## Configuration

`~/.config/bunker/config.toml` (`bunker config init` writes a documented
default). Preferences edits it in place and keeps your comments; saving it
from an editor applies immediately, and an invalid file keeps the previous
settings and says why.

```toml
theme = "nord"                  # default, solarized-dark, dracula, nord
font  = "JetBrains Mono 12"     # Pango font description
scrollback    = 10000           # lines per terminal
scrollback_mb = 32              # optional memory cap; the smaller limit wins

[window]
padding = 8

[tabs]
position = "left"               # left | right | top | bottom
width    = 220                  # sidebar width
autohide = false                # hide the strip with a single tab
collapsed = false               # start the sidebar collapsed
```

## Performance

Measured on one Linux laptop, `seq 1 2000000` inside the window:

| | bunker |
|---|---|
| 2M lines | ~0.8 s |
| RSS, idle, one window | ~98 MB (≈36 MB of it is the Vulkan driver) |
| Idle CPU | ~0.4% |

The emulator changes behind these numbers (compact 32-byte cells, copy-free
scrollback, rotation-based scrolling, an ASCII fast path) live in the shared
core and benefit `bunker tui` as well.

## Development

```bash
sudo dnf install gtk4-devel     # build dependency
make build                      # tests (race) then bin/bunker
make check                      # fmt, vet, test, build, lint
```

The first build compiles the GTK bindings and takes several minutes; later
builds are cached. `third_party/gotk4` carries two fixes to gotk4's subclass
support (see `BUNKER_PATCHES.md` there); `third_party/tcell` is bunk's
patched tcell.

Tests, benchmarks, and regression guards are described in
[TESTING.md](TESTING.md).

Debug helpers: `--debug` / `--trace` log to `/tmp/bunk.log`;
`BUNKER_SCREENSHOT=out.png` renders the window to a PNG through GSK and
exits; `BUNKER_CPUPROFILE=cpu.prof` writes a CPU profile.

## Credits

bunker exists because of [bunk](https://github.com/jsnjack/bunk) by
[jsnjack](https://github.com/jsnjack), whose full history
this repository keeps. The vendored VT emulator descends from
[hinshun/vt10x](https://github.com/hinshun/vt10x); GTK bindings are
[gotk4](https://github.com/diamondburned/gotk4).
