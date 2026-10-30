# Terminal Feature Audit

What bunker's emulator (`crates/vt`) implements, as of 0.7 (the Rust
rewrite). Behaviour is covered by `crates/vt/src/tests.rs` unless noted.

## Legend
- **OK**: handled
- **PARTIAL**: works, with the limits noted
- **MISSING**: not implemented

---

## 1. SGR Text Attributes

| SGR | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0 | Reset | OK | Keeps the current OSC 8 link |
| 1 / 2 / 22 | Bold, dim, normal | OK | Dim draws the foreground blended toward the background |
| 3 / 23 | Italic | OK | |
| 4, 4:0–4:5 / 24 | Underline: single, double, curly, dotted, dashed | OK | |
| 5–6 / 25 | Blink | PARTIAL | Stored and reported; drawn steady |
| 7 / 27 | Reverse | OK | Also DECSCNM (mode 5) for the whole screen |
| 8 / 28 | Hidden | OK | |
| 9 / 29 | Strikethrough | OK | |
| 21 | Double underline | OK | |
| 53 / 55 | Overline | OK | |
| 30–37, 90–97, 39 | Foreground | OK | |
| 40–47, 100–107, 49 | Background | OK | |
| 38/48 ;5;N and :5:N | 256 colours | OK | Semicolon and colon forms |
| 38/48 ;2;R;G;B and :2::R:G:B | True colour | OK | |
| 58 / 59 | Underline colour | OK | 256 and RGB |

---

## 2. OSC Sequences

| OSC | Feature | Status | Notes |
|-----|---------|--------|-------|
| 0 / 2 | Window title | OK | Shown on the tab; control characters dropped, length capped |
| 4 / 104 | Palette colours, set, query, and reset | OK | |
| 7 | Working directory | OK | Used for new tabs and the tab's directory |
| 8 | Hyperlinks | OK | Ctrl+click; survive reflow |
| 9 | Notification (ConEmu) and 9;4 progress | OK | Progress shows on the tab |
| 10 / 11 / 12, 110 / 111 / 112 | Foreground, background, cursor colour | OK | Set, query, reset |
| 52 | Clipboard | PARTIAL | Writes only; reads are never answered (SECURITY.md) |
| 99 | Kitty notifications | OK | Title and body, base64, chunked |
| 133 | Semantic prompts | OK | Marks command start and end, so long commands and their exit status reach the tab |
| 777 | `notify` notifications | OK | |
| 1337 | iTerm2 inline images | OK | See Graphics |

---

## 3. Modes (SM/RM, DECSET/DECRST)

| Mode | Feature | Status | Notes |
|------|---------|--------|-------|
| 4 | Insert (IRM) | OK | |
| 20 | Newline (LNM) | OK | |
| ?1 | Application cursor keys | OK | |
| ?5 | Reverse video (DECSCNM) | OK | |
| ?6 | Origin mode | OK | |
| ?7 | Autowrap | OK | |
| ?9, ?1000, ?1002, ?1003 | Mouse: X10, click, drag, motion | OK | Owned by the process group that set them; dropped once it leaves the foreground |
| ?1004 | Focus events | OK | Same ownership rule |
| ?1006 | SGR mouse | OK | |
| ?1007 | Alternate scroll | OK | Wheel sends arrows on the alt screen |
| ?12 | Cursor blink | OK | Tracked and reported. Not taken as a blink request: terminfo's cnorm sends DECRST 12 to every program, which would override the desktop's setting |
| ?25 | Cursor visible | OK | |
| ?47, ?1047, ?1048, ?1049 | Alt screen, save cursor | OK | |
| ?66 | Application keypad | OK | |
| ?2004 | Bracketed paste | OK | ESC and C1 inside the paste are dropped |
| ?2026 | Synchronized updates | OK | Drawing waits, with a timeout for abandoned updates |
| ?2027 | Grapheme clustering | OK | On by default |
| ?2031 | Colour scheme reports | OK | Reports light/dark changes (CSI ? 997 n); CSI ? 996 n queries |

---

## 4. Queries and Replies

| Query | Feature | Status | Notes |
|-------|---------|--------|-------|
| DA1 (CSI c) | Primary attributes | OK | `CSI ? 62;4;22 c`: VT220 with SIXEL and ANSI colour |
| DA2 (CSI > c) | Secondary attributes | OK | `CSI > 0;279;0 c` |
| DA3 (CSI = c) | Tertiary attributes | OK | No serial number |
| DSR 5, CPR 6 (CSI n, CSI ? 6 n) | Status, cursor position | OK | CPR respects origin mode |
| DECRQM (CSI ? Ps $ p, CSI Ps $ p) | Mode reports | OK | Unknown modes report 0 |
| XTVERSION (CSI > q) | Terminal version | OK | `VTE(8203)`, and `VTE_VERSION=8203` in the environment, since programs pick features by terminal name |
| XTGETTCAP (DCS + q) | Terminfo query | PARTIAL | TN/name, Co/colors (256), RGB, Smulx, Setulc, Su. Key capabilities are not reported; unknown names get "not found" |
| DECRQSS (DCS $ q) | Request setting | OK | SGR, margins, cursor style; others are refused |
| CSI 14 / 16 / 18 t | Window, cell, and grid size | OK | Pixel sizes come from the real cell size |
| CSI 22 / 23 t | Push and pop title | OK | The title itself is never reported back |
| Kitty keyboard (CSI ? u) | Flags query | OK | |

---

## 5. Key Encoding

| Feature | Status | Notes |
|---------|--------|-------|
| ASCII, Ctrl+letter, Alt as ESC prefix | OK | |
| Arrows, Home/End/PgUp/PgDn/Ins/Del with modifiers | OK | xterm modifier parameters; SS3 in application cursor mode |
| F1–F12, Shift+F1–F12 as F13–F24 | OK | |
| Keypad | OK | Application keypad sends SS3 |
| Shift+Tab | OK | CSI Z, or CSI 9;2 u under kitty flags |
| Kitty keyboard protocol | OK | Push, pop, set (`=flags;mode u`), query; flags belong to the process group that set them |
| Input methods | PARTIAL | Committed text works (dead keys, Compose, CJK, emoji); preedit text is not drawn |

Keys bound to a bunker shortcut (`[keys]`) are taken before the program
sees them. Ctrl+C copies only when text is selected, otherwise it reaches
the program. **Passthrough** (Ctrl+F12) sends every key except the toggle
to the program and shows a PASS badge.

---

## 6. Graphics

| Protocol | Status | Notes |
|----------|--------|-------|
| SIXEL | OK | Repeats, RGB and HLS palettes, transparent background (P2=1) |
| Kitty graphics | PARTIAL | Direct RGB/RGBA/PNG, zlib, chunked transfers, cached images, placements with size in cells, query replies, deletes by image, cell, row, column, range, or all |
| iTerm2 inline images | OK | PNG, JPEG, GIF (first frame); sizes in cells, pixels, percent; aspect preserved |

Images are drawn at full resolution as GPU textures over the cells they
cover. They belong to the row they start on, so they scroll into history,
rewrap on resize, and are erased with it.

Not supported: kitty file and shared-memory transfers (refused on purpose),
Unicode placeholders, relative and virtual placements, z-layering, and
animation. These get an `ENOTSUP` reply.

Limits: 8 MiB per transfer, 4096 pixels per side, 4 million pixels per
image, and a kitty cache of 32 images / 32 MiB (oldest evicted first; a
placement of an evicted image gets `ENOENT`).

---

## 7. Unicode

| Feature | Status | Notes |
|---------|--------|-------|
| UTF-8 | OK | Invalid bytes become U+FFFD; sequences split across reads join |
| Wide characters | OK | Widths from unicode-width, wrapping at the right edge |
| Combining marks, ZWJ sequences, flags, skin tones, variation selectors | OK | Grapheme joining from tables generated with unicode-segmentation; tests check every code point |
| Box drawing, blocks, braille | OK | Drawn as shapes snapped to device pixels, so lines join without seams |

---

## 8. Cursor

| Feature | Status | Notes |
|---------|--------|-------|
| DECSCUSR (CSI Ps SP q) | OK | Block, underline, bar; 1/3/5 ask for blinking, 2/4/6 for steady |
| Blinking | OK | `[cursor] blink`: system (the program's DECSCUSR request, else GNOME's setting), on, or off. GNOME's blink time; only while focused; stops after GNOME's idle timeout |
| DECSC/DECRC, CSI s/u | OK | |

---

## 9. Erase and Scrollback

| Sequence | Feature | Status | Notes |
|----------|---------|--------|-------|
| ED 0/1/2 | Erase in display | OK | Leaves history alone, so Ctrl+L keeps it |
| ED 3 | Erase saved lines (`clear`) | OK | Empties history and drops the selection |
| RIS (ESC c) | Full reset | OK | Also clears history |
| DECSTR (CSI ! p) | Soft reset | OK | |
| Resize | Reflow | OK | History rewraps from cells; the cursor's line and below are cut, as in xterm, so shells redraw prompts cleanly |

---

## Since the Go version

The Go bunker (to 0.6) ran its panes through bunk and vt10x and drew
images as half-block cells. The Rust core replaces both:

- Images are real pixels instead of half-blocks.
- DA1 advertises SIXEL (4).
- XTGETTCAP no longer reports key capabilities (it reported F1–F24 and
  modified navigation keys).
- Splits, zoom, and pane navigation are not rebuilt yet.

References: [xterm control sequences](https://invisible-island.net/xterm/ctlseqs/ctlseqs.html),
[kitty keyboard protocol](https://sw.kovidgoyal.net/kitty/keyboard-protocol/),
[kitty graphics protocol](https://sw.kovidgoyal.net/kitty/graphics-protocol/),
[iTerm2 inline images](https://iterm2.com/documentation-images.html),
[Unicode grapheme segmentation](https://www.unicode.org/reports/tr29/).
