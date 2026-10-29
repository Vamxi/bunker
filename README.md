<p align="center"><img src="assets/icon/bunker-256.png" width="128" alt="bunker"></p>

<h1 align="center">bunker</h1>

<p align="center">A fast GTK 4 terminal for Linux.</p>

## What you get

- **Tabs that show what they are doing**, in a sidebar (resizable) or a
  bar. Each tab has a circle: its ring is the tab's colour tag, and inside
  it shows a coding agent working (a spinning arc) or waiting for you (a
  dot), a command running, a finished or failed long command, or a bell.
  The row shows the directory, git branch, SSH host, what runs, and
  progress. Tabs of one colour next to each other are joined by a line.
  Drag tabs to reorder them; double-click to rename.
- **Fast:** output is parsed on its own thread and drawn on the GPU; a
  resize rewraps history in a fraction of a millisecond.
- **Images:** SIXEL, kitty graphics, and iTerm2 inline images, drawn as
  real pixels.
- **Search** (Ctrl+F) through history, with every match highlighted.
- **Alerts:** a tab lights up when a long command finishes (red if it
  failed) or a program such as Claude Code asks for attention, with a
  desktop notification while bunker is in the background.
- **Themes** with previews, following your desktop's light or dark style
  by default (`bunker themes`).
- **Preferences** (Ctrl+,) that edit a plain config file and keep your
  comments; edits in any editor apply live.
- **Clickable links:** Ctrl+click URLs and `ls --hyperlink` links.
- **Input methods:** dead keys, Compose, CJK, emoji picker.

## Install

bunker is released as an RPM for Fedora 43 and later. It installs the
program, its launcher, and its icons, and pulls in GTK 4:

```bash
grm install Vamxi/bunker
```

The RPM from the [releases page](https://github.com/Vamxi/bunker/releases)
also installs with `sudo dnf install ./bunker-*.rpm`.

To build from source instead: `sudo dnf install gtk4-devel cargo && make install`.

## Keys

| Key | Action |
|---|---|
| `Ctrl+F` | Search |
| `Ctrl+C` / `Ctrl+V` | Copy selection (otherwise sent to the program) / paste |
| `Ctrl+Shift+C` / `Ctrl+Shift+V` | Copy / paste |
| `Shift+PgUp` / `Shift+PgDn` | Scroll history |
| `Ctrl+F12` | Send every key to the program (passthrough) |
| `Ctrl+Shift+T` / `Ctrl+Shift+W` | New tab / close tab |
| `Ctrl+PgUp` / `Ctrl+PgDn` | Previous / next tab |
| `Ctrl+Shift+Plus` / `Minus` / `0` | Bigger, smaller, normal text |
| `Ctrl+,` | Preferences |

Right-click a tab to tag it with a colour, rename it, or close it. Every
shortcut can be changed in Preferences > Keyboard, under `[keys]` in the
config, or from a shell.

## Configuration

`~/.config/bunker/config.toml`. Run `bunker config init` for a documented
default, or use Preferences.

```toml
theme = "system"             # follows the desktop; or dark-pastel, nord, ...
font  = "JetBrains Mono 12"

[tabs]
position = "left"            # left, right, top, bottom
width    = 260

[keys]
new_tab  = "f2"
next_tab = "ctrl+right"
prev_tab = "ctrl+left"
```

The same settings from a shell, for scripts and dotfiles:

```bash
bunker config set tabs.width 260
bunker config set keys.new_tab f2
bunker config set keys.next_tab '<Control>Right'   # GTK spelling works too
bunker config list                                   # every setting and its value
```

A running bunker applies changes at once.

## More

- `bunker -- btop` runs a program instead of your shell
- [CHANGELOG.md](CHANGELOG.md): what each release brings
- [TESTING.md](TESTING.md): tests and benchmarks
- [TERMINAL_AUDIT.md](TERMINAL_AUDIT.md): the escape sequences bunker supports
