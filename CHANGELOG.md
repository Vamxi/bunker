# Changelog

What each release brings. `make release VERSION=x.y.z` publishes the
matching section as the GitHub release notes.

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
