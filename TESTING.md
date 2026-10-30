# Testing bunker

Everything that guards bunker against regressions, and how to run it.

```bash
make test        # cargo test --workspace
make check       # fmt, test, build, lint: the gate before a release
make bench       # emulator throughput on the benchmark workloads
make termbench   # the same workloads in real windows, against other terminals
```

## Suites

| Suite | Where | Guards |
|---|---|---|
| Parser | `crates/vt/src/parser.rs` | every sequence kind, input split at every byte, UTF-8 errors, oversized strings, the fast scan and the whole-sequence fast path against the state machine |
| Terminal behaviour | `crates/vt/src/tests.rs` | bytes in, screen and replies out: editing, scroll regions, wide and combining text, SGR, modes, every query reply, events, reflow, search, selection, images; random input never panics or leaves the grid inconsistent |
| Resize | `prompt_survives_resizes`, `live_line_is_cut_not_rewrapped` | a shell's prompt redraw after resizes leaves no pieces of old prompts and keeps the history above it |
| Unicode | `crates/vt/src/unicode.rs` | the generated join table matches segmentation for every code point; the fast join check never misses a join |
| Graphics | `crates/vt/src/graphics.rs`, `tests.rs` | SIXEL, kitty (raw, chunked, cached, replies, deletes, refused file transfers), iTerm2 sizes; images scroll, reflow, and clear with their rows |
| Input encoding | `crates/vt/src/input.rs` | legacy, application, and kitty key encodings; mouse modes and encodings; bracketed paste cannot be escaped |
| Rows and styles | `grid.rs`, `cell.rs` | trimmed scrollback rows, wide-character edges, row identities, style interning and compaction |
| App | `crates/bunker/src/**` | config parsing and comment-keeping saves (mode 0600), `bunker config` values, themes, key syntax and shortcuts, agent and title detection, ssh hosts, git branches, PTY spawn, image codecs, link safety |

## The window

GUI behaviour is checked by hand and with screenshots. The debug hooks in
`crates/bunker/src/ui/debug.rs` type into tabs and save a frame:

```bash
BUNKER_INPUT='ls --color\n|||cd /tmp\n' BUNKER_SCREENSHOT=/tmp/shot.png \
  dbus-run-session -- scripts/headless-gui.sh target/release/bunker
```

`scripts/headless-gui.sh` runs a private headless GNOME compositor, so this
works with the screen locked and never touches the desktop session.

`terminal_features.sh` prints test patterns by section (`text`, `colors`,
`cursor`, `integration`, `queries`, `osc`, `graphics`) to check rendering
and input in a running window.

## Benchmarks

`make bench` writes the workloads (`crates/vt/examples/workloads.rs`: seq,
prose, colours, Unicode, TUI) and reports the emulator's MB/s without a
window. `make termbench` times them end to end in real windows, in ms,
against Ptyxis and foot at the same grid size (kitty, Tilix, and bunk-gui
too, when named: `scripts/termbench.sh bunker kitty`); the windows open on the
desktop one at a time. Results depend on the machine; compare runs from
the same one.
