# Changelog

What each release brings. `make release VERSION=x.y.z` publishes the
matching section as the GitHub release notes.

## 0.3.0

### Added

- Every shortcut can be changed: the window's own (new tab, next and
  previous tab, clipboard, text size, Preferences) join bunk's pane keys
  under `[keys]`. Preferences > Keyboard records a new shortcut when you
  click one and press the keys, and asks before taking a key another
  action uses. A key set for two actions is shown in a banner.
- `bunker config set`, `get`, and `list` change and read any setting from a
  shell, with the same checks as Preferences. GTK's key spelling
  (`<Control>Right`) is accepted.
- The cursor blinks. `[cursor] blink` is `system` (the desktop setting, or
  what the program asks for), `on`, or `off`, as in Ptyxis.

## 0.2.1

### Fixed

- Pasting with Ctrl+V after copying inside bunker froze the window. bunk's
  copy and paste keys now use the window's own clipboard, like
  Ctrl+Shift+C and Ctrl+Shift+V, and never wait on a clipboard command.
- A split into the same container (Alt+F1) no longer freezes the window
  while podman or docker answers.
- If the window ever stops responding for 5 seconds, bunker logs where it is
  stuck, so the cause can be found.

## 0.2.0

### Added

- bunker is released as an RPM. `grm install Vamxi/bunker` installs it with
  dnf, together with the launcher, the icons, and GTK 4; `bunker
  install-desktop` is no longer needed for released builds.

### Changed

- `--debug` now logs to stderr, and `--trace` writes `/tmp/bunker.log`
  (truncated on every start). The trace file is only used if it is a
  regular file you own. Without flags, only warnings and errors are logged.

### Faster

- Plain output about 40% faster (`seq 1 2000000`: 0.83 s → 0.48 s):
  scrolling clears only the part of a line that was used.
- Coloured and Unicode output about 40% faster (52 MB of coloured text
  with CJK and emoji: 1.81 s → 1.09 s): character widths and grapheme
  checks come from a lookup table, and the byte stream is scanned with fast
  byte searches instead of one byte at a time. Full-screen programs (btop,
  vim) redraw about 4× faster in the emulator.

## 0.1.1

### Fixed

- Closed tabs are freed. Before, every closed tab kept its scrollback in
  memory until bunker quit.
- Links that wrap onto the next rows are one link: hover underlines all of
  it and Ctrl+click opens the whole URL. Works for plain URLs, for URLs that
  programs like Claude Code lay out themselves, and for OSC 8 links.
- The link tooltip says "Ctrl+click to open" instead of printing the whole
  URL across the screen.

## 0.1.0

First release.

- bunk in every tab: split panes, context-aware splits into the same
  container / SSH host / sudo, zoom, search, status badges, keyboard
  passthrough
- Tabs in a collapsible sidebar or a top bar, renamable, with a right-click
  menu
- 20 themes, following the desktop's light/dark setting by default
- GPU rendering through GTK 4
- Preferences (Ctrl+,) that edit a plain config file, and live reload when
  you edit it yourself
- Clickable links (Ctrl+click) for URLs and `ls --hyperlink`
- Input methods: dead keys, Compose, CJK, emoji picker
- `bunker install-desktop` adds bunker to the app grid and dock
