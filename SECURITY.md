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
| Query replies | Replies are generated from terminal state. The window title is never reported back (no CSI 21t); DECRQSS never echoes the request; XTGETTCAP echoes only a name that decoded as hex. | `replies` in `crates/vt/src/tests.rs` |
| Paste | Bracketed paste drops every ESC and C1 CSI inside the pasted text, so clipboard content cannot end the bracket early and run the rest as commands. | `paste` in `crates/vt/src/input.rs` |
| Clipboard (OSC 52) | Programs may **set** the clipboard, as in most terminals. Clipboard **reads** are never answered. | `replies`, `events` |
| Links | Ctrl+click only; http(s), mailto, and local `file` links. `file` links to executables or `.desktop` launchers are refused. | `safe_links` in `ui/view.rs` |
| Titles | Control characters are dropped and invalid UTF-8 replaced; length is capped. Labels show text, never markup. | `title_and_cwd` |
| Graphics | Kitty file and shared-memory transfers are rejected; iTerm2 downloads are never written to disk; transfer, image, and cache sizes are capped. | `crates/vt/src/graphics.rs` |
| Escape parsing | Bounded buffers for OSC/DCS/APC, parameters, grapheme length, and history. | `crates/vt/src/parser.rs`, `arbitrary_input_never_panics` |
| Files | Config is written atomically with mode 0600, in a directory created 0700. `--trace` writes `/tmp/bunker.log` only if it is a regular file owned by you (never through a symlink), at mode 0600; `--debug` goes to stderr. | `saved_config_is_private`, `log.rs` |
| Processes | Programs start without a shell (`exec` with arguments). | — |

## Accepted on purpose

- **OSC 52 writes.** A program can replace your clipboard. Pasting is
  bracketed and escape-free (above), so a shell treats it as text.
- **Trace logs** (`--trace`) record terminal output, which can include
  secrets printed on screen. They are off by default and readable only by you.
- **Debug hooks** (`BUNKER_INPUT`, `BUNKER_SCREENSHOT`) type into tabs and
  write where the environment says; they only act when those variables
  are set.

## Checks

`make check` (rustfmt, the test suite, a release build, clippy with
warnings as errors) runs before releases.
