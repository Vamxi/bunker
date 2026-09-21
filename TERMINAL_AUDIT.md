# Bunk Terminal Feature Audit

Date: 2026-03-12 (updated 2026-09-21)

## Legend
- **OK** — fully handled
- **FIXED** — implemented; notes retain the regression history
- **PARTIAL** — works but incomplete
- **MISSING** — not implemented, will break or degrade apps that use it
- **N/A** — not feasible in a multiplexer (inherent limitation)

---

## 1. SGR Text Attributes

| SGR Code | Feature | Status | Impact |
|----------|---------|--------|--------|
| 0 | Reset | OK | |
| 1 | Bold | OK | |
| 2 | Dim/Faint | OK | vt10x vendored; AttrDim bit added. RGB-colour terminals ignore ti.Dim, so bunk blends FG 50% toward BG itself |
| 3 | Italic | OK | |
| 4 | Underline | OK | |
| 4:0-4:5 | Underline styles (curly/double/dotted/dashed) | OK | CSI sub-param parser fixed; all styles stored in Glyph.Mode bits and mapped to tcell.UnderlineStyleXxx |
| 5-6 | Blink | OK | |
| 7 | Reverse | **FIXED** | Was broken for default-color cells — vtColor mapped both DefaultFG and DefaultBG to the positional `def` param, undoing vt10x's FG/BG swap. Claude/Copilot cursor (reverse-video space) was invisible |
| 8 | Hidden/Invisible | OK | AttrInvisible bit added; rendered as space character |
| 9 | Strikethrough | OK | AttrStrikethrough bit added; tcell StrikeThrough(true) applied |
| 21 | Double underline | OK | Same style as SGR 4:2; does not clear bold/dim |
| 53 | Overline | OK | Stored through reflow and emitted as SGR 53 by the pinned tcell fork; SGR 55/0 clear it. Host must support overline |
| 58;5;N | Colored underline (256) | OK | attrHasULColor flag + UL Color field; vtColor mapping; tcell style.Underline(color) |
| 58;2;R;G;B | Colored underline (RGB) | OK | Same |
| 30-37, 90-97 | ANSI FG colors | OK | |
| 40-47, 100-107 | ANSI BG colors | OK | |
| 38;5;N / 48;5;N | 256 colors | OK | |
| 38;2;R;G;B / 48;2;R;G;B | True color (24-bit) | OK | |

Root cause (resolved): vt10x vendored to `internal/vt10x`; SGR 2/8/9 attr bits added and wired.

Apps affected: `git diff`, `ls --color`, neovim with LSP, glow, bat, delta, lazygit

---

## 2. OSC Sequences

| OSC | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0/1/2 | Window title | OK | vt10x handles; bunk supplements with process/cwd |
| 4 | Set palette color | OK | vt10x handles |
| 7 | CWD notification | OK | Forwarded to host |
| 8 | Hyperlinks | OK | Stored on glyphs and emitted inline with text; hyperlink identities survive reflow |
| 10/11/12 | Query/set fg/bg/cursor color | OK (host-dependent) | In-stream replies in both screen modes, including SSH/mosh. FG/BG changes repaint existing cells; cursor colour follows the active pane via tcell. Terminal-theme defaults are probed at startup; a genuinely unknown default is not fabricated |
| 52 | Clipboard | OK | Forwarded to host |
| 104 | Reset palette color | OK | vt10x handles |
| 110/111/112 | Reset fg/bg/cursor color | OK | Clears dynamic overrides, repaints FG/BG, and restores the pane's cursor-colour baseline (or the host default when unknown) |
| 133 | Shell integration/prompt marking | OK | Forwarded to host so semantic prompt integration and jump-to-prompt can work when the outer terminal supports it |

Host limitation: an outer terminal that does not disclose a default colour cannot
be queried accurately for that value. Explicit pane colour overrides remain queryable.

---

## 3. DEC Private Modes (DECSET/DECRST)

| Mode | Feature | Status | Notes |
|------|---------|--------|-------|
| 1 | DECCKM (cursor keys app mode) | OK | Unmodified arrows/Home/End use SS3 in application mode; modified keys retain CSI modifier encoding |
| 7 | DECAWM (auto-wrap) | OK | |
| 12 | Cursor blink | OK | Toggles blinking while retaining block/underline/bar shape; queryable with DECRQM |
| 25 | DECTCEM (cursor visible) | OK | |
| 47/1047 | Alt screen | OK | |
| 1000-1003 | Mouse modes | OK | Release events are forwarded in 1000/1002 as well as 1003 |
| 1004 | Focus events | OK | Pane navigation and outer-terminal focus changes are forwarded when enabled |
| 1006 | SGR mouse | OK | |
| 1049 | Alt screen + save cursor | OK | |
| 2004 | Bracketed paste | OK | |
| 2026 | Synchronized updates | OK | Suppresses only the updating pane; a one-second timeout releases abandoned updates |
| 2027 | Grapheme clustering | OK | Enabled by default; incremental Unicode grapheme assembly with bounded cell storage and legacy per-codepoint mode on reset |

---

## 4. Terminal Capability Queries/Responses

| Query | Feature | Status | Notes |
|-------|---------|--------|-------|
| DA1 (CSI c) | Primary device attributes | OK | Responds as VT220 |
| DA2 (CSI > c) | Secondary device attributes | OK | Responds as xterm 279 |
| DA3 (CSI = c) | Tertiary device attributes | OK | DCS !\|00000000 ST (no hardware serial number) |
| CPR (CSI 6 n / CSI ? 6 n) | Cursor position report | OK | Both screen modes; respects origin mode and scroll margins |
| DSR (CSI 5 n) | Device status report | OK | CSI 0 n, including remote panes |
| DECRQM (CSI ? Ps $ p) | Request mode | OK | Tracked modes include 12/2027; unknown modes return 0, not the permanently-reset status 4 |
| XTVERSION (CSI > 0 q) | Terminal version | OK | Responds with DCS >|VTE(8203) ST; VTE_VERSION=8203 set in pane env; used by Claude Code, Neovim, WezTerm for feature detection |
| XTGETTCAP (DCS + q) | Terminfo capability query | OK | Responds to Smulx/Setulc/Su (found with hex-encoded value); all others get "not found"; eliminates startup latency in apps that query capabilities |
| DECRQSS | Request setting | OK | SGR (`m`), scroll margins (`r`), and cursor style (`SP q`); unsupported settings receive DCS 0 $ r ST |

---

## 5. Key Encoding

| Feature | Status | Notes |
|---------|--------|-------|
| Basic ASCII keys | OK | |
| Ctrl+letter | OK | |
| Alt+key (ESC prefix) | OK | |
| Arrow keys | OK | |
| Shift+Tab (BackTab) | **FIXED** | Was sending `\x1b[9;3u` (Alt+Tab) in kitty mode; now correctly `\x1b[9;2u` |
| Kitty keyboard protocol | **FIXED** | Push/pop/query stack; CSI u encoding for Enter, Tab, Backspace, Ctrl+letter. Fixed: stale stack after non-alt-screen KKP app exits without `\x1b[<u` — cleared by `trackFgProcess` on PGID change. Fixed: set-flags form `\x1b[=<flags>;<mode>u` (two params) was not stripped — the `;` broke the single-digit scan, leaking the sequence to vt10x which read the trailing `u` as DECRC, corrupting cursor-relative drawing (e.g. Copilot CLI welcome screen / mascot) |
| F1–F12 | OK | Any key bound to a bunk action (default: F1=split, F12=zoom) is consumed outside passthrough mode; bindings are configurable |
| F13-F24 | OK | Shift+F1-F12; handled both via `KeyF13`–`KeyF24` and via `KeyF1`+`ModShift` modifier path |
| Home/End/PgUp/PgDn/Ins/Del | OK | All modifiers forwarded as `\x1b[<code>;<mod>~` / `\x1b[1;<mod>H/F`; Shift+PgUp/PgDn consumed by default for scrollback outside passthrough mode (config-dependent) |
| Modified arrows (Ctrl+Up etc) | OK | Forwarded as `\x1b[1;<mod>A/B/C/D`; Alt+arrows consumed by default for pane nav outside passthrough mode (config-dependent) |
| Modified Home/End/etc | OK | Ctrl+Home, Shift+End, Ctrl+Delete etc. forwarded with xterm modifier parameter |
| Keypad keys | OK (host-dependent) | Keypad identity survives SS3/Kitty input; digits/operators/Enter use application-keypad SS3 or CSI-u. Hosts sending ordinary digit/Enter bytes cannot identify their physical origin |

> **Passthrough:** Ctrl+F12 (remappable as `[keys].passthrough`) toggles per-pane
> keyboard passthrough. All keys except the toggle reach the child application
> using its negotiated encoding. PASS shares the badge layout with highest
> priority; mouse controls remain available.

> **Note:** Outside passthrough mode, any key bound to a bunk action in the user's config is intercepted and not forwarded to the PTY. Default consumed keys: Ctrl+F12 (passthrough toggle, also reserved during passthrough), F1 (split), Alt+F1 (split-context), F12 (zoom), Alt+arrows (pane nav), Shift+PgUp/PgDn (scrollback), Ctrl+C (copy/forward), Ctrl+V (paste), Ctrl+Q (quit), Ctrl+F (search), Ctrl+N (search-next). All of these are user-remappable.

---

## 6. Graphics Protocols

| Feature | Status | Notes |
|---------|--------|-------|
| Sixel | OK (cell-rendered subset) | Static raster data, repeats, RGB/HLS palettes, transparency; square source pixels |
| Kitty graphics | OK (cell-rendered subset) | Direct RGB/RGBA/PNG, zlib, chunking, query replies, cached placement, cropping/scaling, cursor policy, ID/coordinate deletion |
| iTerm2 inline images | OK (cell-rendered subset) | Inline PNG/JPEG/GIF first frame; multipart uploads; cell/pixel/percentage sizing and aspect preservation |

Images are downsampled into RGB half-block cells, not native pixels. They clip to
the pane, scroll into history, survive resize, and are overwritten/erased like
text. Transparent samples blend against the cell background; fully transparent
cells leave existing text intact. Deletion removes visible image cells without
restoring covered text or editing archived scrollback. Layering, animation,
Unicode placeholders, relative placements, non-square SIXEL pixel aspects, and
native-pixel fidelity are outside this rendering mode. Unsupported Kitty actions
receive an error (subject to `q`); file/shared-memory transfers are rejected.
iTerm2 downloads are never written to disk. Explicit protocol selection may be
needed in clients that rely on host-brand environment variables.

Bounds: 8 MiB encoded transfer, 4096 pixels per axis, 4 million source pixels,
256 Ki output cells, and a 32 MiB / 32-image Kitty cache. Cached images are evicted
oldest-first; a later placement of an evicted ID returns `ENOENT`. Replay history
is bounded too: images whose upload/placement has aged out cannot be reconstructed.
CSI 14/16/18 and PTY pixel sizes describe the same virtual cell geometry.

---

## 7. Unicode / Character Width

| Feature | Status | Notes |
|---------|--------|-------|
| UTF-8 | OK | Boundary detection prevents split-rune corruption |
| CJK double-width | **FIXED** | vt10x tracks display-cell widths and continuations, including right-edge wrapping and editing. Rendering, cursor positions, copy, search, and reflow use those same coordinates |
| Combining characters | OK | Immutable grapheme suffix retained in lead cells, including copy/search/reflow |
| Emoji (multi-codepoint) | OK | ZWJ families, flags, modifiers, variation selectors, and keycaps; widths and continuation cells agree with tcell. Visual shaping still requires a capable host/font |

---

## 8. Cursor Style (DECSCUSR)

| Feature | Status | Notes |
|---------|--------|-------|
| \x1b[N q forwarding | OK | Scans PTY output and forwards to host |
| Reset on exit | OK | Resets to default on shutdown |

---

## 9. Erase / Scrollback Management

| Sequence | Feature | Status | Notes |
|----------|---------|--------|-------|
| ED 0/1/2 (CSI J) | Erase in display | OK | Never touches scrollback — Ctrl+L / `clear -x` keep history, matching xterm |
| ED 3 (CSI 3 J) | Erase saved lines (xterm E3, sent by clear(1)) | OK | vt10x fires a scrollback-clear callback (`WithScrollbackClearCallback`); the pane empties sbRing, snaps sbOff to live, drops any active selection, and cuts rawBuf just past the sequence so resize replay can't resurrect the erased history |
| RIS (ESC c) | Full reset (sent by reset(1)) | OK | Same scrollback-clear callback, fired after vt10x state reset; init-time reset() does not fire it |

---

## Remaining scope and limitations

All listed feature rows are implemented within their stated scope; this is not
an exhaustive claim of terminal-protocol conformance.

- **Capability queries:** XTGETTCAP exposes only the three capabilities listed
  above. DECRQSS reports only implemented settings; other requests are rejected.
  Additional replies must describe behaviour bunk actually supports.
- **Graphics extensions:** native-pixel output, animation, layering, Unicode
  placeholders, relative placements, and non-square SIXEL pixel aspects remain
  unsupported. Native-pixel output requires a different rendering approach.
- **Host dependencies:** unknown default colours, physical keypad identity when
  the host sends ordinary keys, and font/emoji shaping cannot be recovered from
  information the host does not provide.
- **Intentional bounds:** transfer, image, cache, and replay limits remain part
  of the design. Kitty file/shared-memory access is intentionally rejected.

## Completed implementation history

### Keyboard passthrough (2026-09-21)

- Added per-pane Ctrl+F12 passthrough with a persistent PASS badge. Other bunk
  keyboard bindings reach the pane while enabled; the toggle remains reserved.
- PASS shares the status layout and takes priority when badges do not fit.


### Reliability fixes (2026-09-17)

- Primary-screen resize retains terminal modes, cursor style, active attributes,
  dynamic colours, hyperlinks, and scrollback-clear callbacks.
- Replay sizing counts hard line breaks as well as wrapping, preserving short-line history.
- PTY framing caps ordinary controls at 64 KiB (recognized graphics at 8 MiB) and discards oversized
  sequences through their terminator; incomplete input no longer grows unbounded.
- Foreground polling retains Kitty negotiation made by the current process;
  only negotiation owned by a departed foreground process is cleared.
- Repeated transient line clears expire independently and cannot leave rendering suppressed.

### Protocol/text fixes (2026-09-18)

- Implemented SGR 21/53 display, cursor blink/colour, DA3, and DECRQSS.
- Corrected DSR, alternate-screen CPR, origin-relative CPR, remote query replies,
  and unknown-mode DECRQM status. Colour replies no longer depend on alt-screen.
- Added application cursor/keypad input and host focus-event forwarding.
- Added bounded grapheme storage across chunk boundaries, wide-cluster wrapping,
  rendering, selection, search, and reflow.
- Corrected Kitty set-flags add/remove operations, capped negotiation depth,
  and prevented string payloads from being interpreted as keyboard negotiation.
- Bundled the existing tcell v2.13.9 with small overline/keypad/keycap patches;
  its regression tests are included in `make test`.

### Graphics fixes (2026-09-18)

- Implemented the agreed portable, cell-rendered static subset of all three
  graphics protocols; no protocol remains entirely missing.
- Added bounded decoding/transfer/cache limits, malformed-input recovery,
  pane-local clipping, alpha blending, placement deletion, and replay support.
- Added `terminal_features.sh graphics`, simulation-screen and PTY-path tests,
  and decoder fuzz coverage. Native graphics and the extensions excluded above
  are not claimed as implemented.

Protocol references: [xterm control sequences](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html),
[Kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/),
[Unicode grapheme segmentation](https://www.unicode.org/reports/tr29/).
Graphics references: [Kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/),
[iTerm2 inline images](https://iterm2.com/documentation-images.html),
[DEC VT300 reference](https://vt100.net/dec/ek-vt3xx-hr-002.pdf).
