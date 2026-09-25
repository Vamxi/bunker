# Testing bunker

Everything that guards bunker against regressions, and how to run it.

```bash
make test        # all tests, race detector on; GUI tests run when a display is available
make test-gui    # only the GUI integration tests (fast, verbose)
make bench       # benchmarks, compared with bench/baseline.txt
make check       # fmt, vet, test, build, lint: the gate before a release
```

## Suites

| Suite | Where | Guards |
|---|---|---|
| Emulator | `internal/vt10x/*_test.go` | escape sequences, modes, wide/combining text, scroll regions, graphics, CSI parameter parsing |
| Fast-path equivalence | `internal/vt10x/throughput_test.go`, `csi_parse_test.go` | the ASCII fast path and the allocation-free CSI parser produce exactly what the general parser does, over thousands of random inputs |
| Exhaustive | `uniinfo_test.go`, `used_test.go`, `scanfast_test.go` (references in `scanref_test.go`) | the Unicode table matches uniseg for every code point; rows never hold content past their used count; the fast PTY pre-scanners match their byte-at-a-time originals on random escape-heavy streams. Sampled under `-race`, run in full by `make test` |
| Pane, scrollback, reflow, search, TUI | root `*_test.go` (from bunk) | PTY bridge, scrollback ring, resize reflow, selection, search, mouse, keys, rendering in `bunker tui` |
| Config | `config_test.go`, `tomledit_test.go`, `scrollswap_test.go`, `bunkconfig_test.go` | loading and validation, comment-preserving edits, atomic writes, scrollback caps; bunk's own config (`testdata/bunk-config.toml`) works when copied into bunker |
| GUI logic | `gui_input_test.go`, `gui_tabs_test.go`, `gui_links_test.go` | GDK key → terminal bytes, box-drawing table, tab labels, rename rules, separators, URL detection, link scheme allowlist |
| GUI integration | `gui_integration_test.go` (harness in `guitest_harness_test.go`) | real windows on the test display, driven like a user: see below |
| Allocation guards | `allocs_test.go`, `TestWriteDoesNotAllocate`, `TestCSIParseDoesNotAllocate` | hot paths allocate nothing in steady state |
| Size guard | `internal/vt10x/glyph_test.go` | a cell stays 32 bytes |
| Build gate | `makefile_test.go` | `make check` runs tests before building, even with `make -j` |
| Packaging | `desktop_test.go` | the RPM (`nfpm.yaml`) ships exactly the launcher and icons `install-desktop` stages |

### GUI integration tests

Each test opens a real bunker window with a temporary config and drives it
through the same entry points as a user: the view's key handler, the config
file on disk, tab actions.

| Test | Covers |
|---|---|
| `TestGUI_RendersOutputAndThemedChrome` | output reaches a drawn frame; header bar and terminal pixels are the theme background |
| `TestGUI_Tabs` | Ctrl+Shift+T / Ctrl+Shift+W, wrap-around switching, renumbering after close |
| `TestGUI_TabStripLayout` | sidebar width, collapse to one character, top bar (centred titles, no toggle), autohide |
| `TestGUI_RenameTab` | typed names stick, untouched text does not, rename from collapsed expands and re-collapses, Reset Name |
| `TestGUI_ConfigLiveReload` | an editor-style save (rename over) applies live; an invalid file keeps settings and shows the banner |
| `TestGUI_Preferences` | controls show the config, the Keyboard page shows bindings, changes write single keys and keep comments |
| `TestGUI_SplitsZoomAndPaneExit` | F1 splits, separators, Alt+arrow focus, F12 zoom in and out, pane exit, last pane closes the tab |
| `TestGUI_Search` | Ctrl+F, typed query, highlighted matches in the frame, Esc |
| `TestGUI_HeaderAndTabInfo` | cwd in the header subtitle, pane count on the tab |
| `TestGUI_PassthroughBypassesWindowShortcuts` | Ctrl+F12 passthrough also hands window shortcuts to the program |
| `TestGUI_TabMenu` | right-click menu opens; Close Other Tabs |
| `TestGUI_Hyperlinks` | OSC 8 and plain URLs found under the pointer, URLs wrapped across rows, hover underline (pixel check), Ctrl+click opens, unsafe schemes refused, plain click still selects |
| `TestGUI_IME` | input-method text reaches the program and the search bar; composition drawn at the cursor, cursor location reported |
| `TestGUI_NoLeaks` | 15 cycles of tab + splits + output + close give back every goroutine, fd, child process, and heap byte; no zombies |
| `TestGUI_PasteWhatBunkerCopied` | Ctrl+V after bunker copied (text, search bar, image) never freezes the window |
| `TestGUI_ContextSplitIsAsync` | Alt+F1 resolves the context in the background and splits on the GTK thread |
| `TestGUI_WaylandIMStress` | rapid window/tab/entry churn on the Wayland input method, in a child process (this used to crash GTK) |

`make test` runs them on a private headless GNOME compositor
(`scripts/headless-gui.sh`: mutter with a virtual monitor and its own D-Bus
session), so they pass with the screen locked and never touch your desktop.
Without mutter they use the current display (`WAYLAND_DISPLAY` / `DISPLAY`),
and skip without one; `BUNKER_NO_GUI_TESTS=1` skips them explicitly. A bare
`go test` uses your desktop: a locked screen gives windows no frames, and
the GUI tests then time out. The harness
owns the process's main thread for GTK (`onMain`, `waitMain`) and keeps the
GLib main loop running, so frame clocks, idles, and file monitors behave as
in the app, with the same input method users have. One environment detail:

- `make test` turns off `checkptr` for the gotk4 packages only: `-race`
  enables it, and gotk4's generated marshallers convert uintptrs to pointers.
  A bare `go test -race .` therefore aborts in gotk4; use `make test`.

Adding a feature? Add a `TestGUI_…` next to the others: `newTestWin` opens a
window, `w.key` presses keys, `waitMain` waits on GUI state, `w.screenshot`
returns the rendered window for pixel checks.

## Benchmarks

| Benchmark | Measures |
|---|---|
| `internal/vt10x` `BenchmarkWrite/*` | the emulator alone, per workload |
| `BenchmarkPaneWrite/*` | PTY bytes → emulator → 10k-line scrollback → raw history |
| `BenchmarkReflow` | rewrapping 5k lines when the width changes |
| `BenchmarkSearch` | Ctrl+F over 10k lines |
| `BenchmarkFrameCapture` | copying a 200×60 pane for one frame |
| `BenchmarkTOMLEdit` | one Preferences change |
| `BenchmarkGUIFrame` | a whole frame of a busy 3-pane tab (GUI only) |

Workloads live in `internal/benchdata`: `seq` (short scrolling lines),
`prose` (long wrapped lines), `colors` (a colour change per character),
`unicode` (CJK, emoji, combining marks), and `tui` (full-screen
cursor-addressed frames like btop).

`make bench` runs each six times into `bench/latest.txt` and, when
`bench/baseline.txt` exists, prints a `benchstat` comparison. After an
intended change, `make bench-baseline` and commit the new baseline. Timings
depend on the machine; compare runs from the same one. Allocation counts do
not, which is why the allocation guards are tests.

## Test recipes

Building blocks for tests of the pane model and the TUI renderer.

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

## Manual checks

- `BUNKER_SCREENSHOT=out.png bunker -- btop` renders the window to a PNG;
  combine with `BUNKER_KEYS="f1,f12"` (bunk key names), `BUNKER_TABS` (one
  command per line), and `BUNKER_OPEN=preferences/<page>` or `tab-menu`.
  See `gui_debug.go`.
- `BUNKER_CPUPROFILE=cpu.prof bunker -- seq 1 2000000` profiles a real run.
- bunk's `terminal_features.sh` exercises rendering and queries by eye
  (see AGENTS.md for which section to run per change).
