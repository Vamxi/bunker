#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

section "Text Attributes"
info "This script focuses on SGR styling: bold, dim, italic, reverse, invisible, strikethrough, overline, and underline styles."
expect "Each line should visibly demonstrate the named attribute without corrupting nearby text."
diagnose "If one attribute is wrong while plain colors remain correct, the bug is usually in vt10x SGR parsing or render style mapping."

section "Core SGR Attributes"
printf "  %-22s ${CSI}1mBold text${R}\n"            "SGR 1  bold"
printf "  %-22s ${CSI}2mDim/faint text${R}\n"        "SGR 2  dim"
printf "  %-22s ${CSI}3mItalic text${R}\n"           "SGR 3  italic"
printf "  %-22s ${CSI}5mBlink text${R}\n"            "SGR 5  blink"
printf "  %-22s ${CSI}7mReverse text${R}\n"          "SGR 7  reverse"
printf "  %-22s ${CSI}8m(invisible)${R} <-- should be blank\n" "SGR 8  invisible"
printf "  %-22s ${CSI}9mStrikethrough${R}\n"         "SGR 9  strikethrough"
printf "  %-22s ${CSI}53mOverline text${R}\n"        "SGR 53 overline"
expect "Bold should look stronger, dim should look faded, reverse should swap fg/bg, invisible should hide the word, strikethrough should cut through the middle, and overline should draw above the text."
diagnose "If invisible still shows glyphs, check SGR 8 handling. If reverse fails, check default fg/bg swapping and render color mapping."

section "Underline Variants"
printf "  %-22s ${CSI}4mUnderline text${R}\n"        "SGR 4  underline"
printf "  %-22s ${CSI}4:1mUnderline text${R}\n"      "SGR 4:1 solid"
printf "  %-22s ${CSI}4:2mDouble underline${R}\n"    "SGR 4:2 double"
printf "  %-22s ${CSI}21mDouble underline${R}\n"     "SGR 21 double"
printf "  %-22s ${CSI}4:3mCurly underline${R}\n"     "SGR 4:3 curly"
printf "  %-22s ${CSI}4:4mDotted underline${R}\n"    "SGR 4:4 dotted"
printf "  %-22s ${CSI}4:5mDashed underline${R}\n"    "SGR 4:5 dashed"
expect "All six rows should render underline beneath the text, and the styled variants should be visually distinct where the host terminal can show them."
diagnose "If only plain underline works, inspect SGR 4:N parsing or the underline-style mapping to tcell."

section "Combined Styles"
printf "  %-22s ${CSI}1;3;4mBold+italic+under${R}\n" "SGR combined"
printf "  %-22s ${CSI}2;9mDim + strikethrough${R}\n" "SGR combined"
expect "Combined styles should stack cleanly rather than clobbering one another."
diagnose "If one style clears another, inspect reset/bitmask handling in vt10x attr mode state."

script_done
