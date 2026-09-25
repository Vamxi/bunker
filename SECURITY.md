# Security

Report vulnerabilities privately through GitHub's
[security advisories](https://github.com/Vamxi/bunker/security/advisories/new)
rather than public issues.

## Threat model

A terminal displays untrusted bytes: `cat` of a downloaded file, a remote
server over SSH, a compromised build log. Output must never become input,
run code, read your data, or reach files outside what you asked for.

## What bunker does

| Area | Behaviour | Guarded by |
|---|---|---|
| Query replies | Replies are generated from terminal state. The window title is never reported back (no CSI 21t); DECRQSS never echoes the request; XTGETTCAP echoes only a name that decoded as hex. | `capabilities_test.go`, `internal/vt10x/audit_test.go` |
| Paste | Bracketed paste drops every ESC and C1 CSI inside the pasted text, so clipboard content cannot end the bracket early and run the rest as commands. | `TestPasteCannotEscapeBracketedMode` |
| Clipboard (OSC 52) | Programs may **set** the clipboard, as in most terminals. In the window, clipboard **reads** are never answered; `bunker tui` passes them to your outer terminal, whose policy applies. | `osc_test.go` |
| Links | Ctrl+click only; http(s), mailto, and local `file` links. `file` links to executables or `.desktop` launchers are refused. | `TestSafeLink`, `TestSafeLocalFile`, `TestGUI_Hyperlinks` |
| Titles | Control characters and invalid UTF-8 are stripped; length is capped. Labels show text, never markup. | `render_test.go` |
| Graphics | Kitty file and shared-memory transfers are rejected; transfer, image, and cache sizes are capped. | `graphics_test.go`, `internal/graphics` |
| Escape parsing | Bounded buffers for OSC/DCS/APC, grapheme length, and history. | `ptystream_test.go`, `internal/vt10x` |
| Files | Config is written atomically with mode 0600. `--debug`/`--trace` logs go to `~/.local/state/bunker/` (0600, symlinks not followed). | `tomledit_test.go` |
| Processes | Programs start without a shell (`exec` with arguments). | — |

## Accepted on purpose

- **OSC 52 writes.** A program can replace your clipboard. Pasting is
  bracketed and escape-free (above), so a shell treats it as text.
- **Context splits (Alt+F1)** re-run the focused pane's own command line
  (ssh, sudo, container exec) from `/proc`: your processes, your privileges.
- **Trace logs** (`--trace`) record terminal output, which can include
  secrets printed on screen. They are off by default and private.
- **Debug hooks** (`BUNKER_SCREENSHOT`, `BUNKER_CPUPROFILE`, …) write where
  the environment says; they only act when those variables are set.

## Checks

`govulncheck ./...` (dependencies), `staticcheck ./...`, and the race-enabled
test suite (`make test`, see TESTING.md) run before releases.
