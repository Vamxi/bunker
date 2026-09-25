# Changelog

What each release brings. `make release VERSION=x.y.z` publishes the
matching section as the GitHub release notes.

## Unreleased

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
