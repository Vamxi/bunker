#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

section "Cursor Shapes"
info "This script checks DECSCUSR cursor-shape forwarding and Unicode width handling."
expect "The cursor should visibly change shape for each numbered mode, then return to the default shape."
diagnose "If the cursor never changes, inspect DECSCUSR handling or host-terminal cursor-style emission."

printf "  Cycling through cursor shapes. Watch the cursor carefully.\n\n"
shapes=(
    "1 blinking block"
    "2 steady block"
    "3 blinking underline"
    "4 steady underline"
    "5 blinking bar"
    "6 steady bar"
)
for entry in "${shapes[@]}"; do
    n="${entry%% *}"
    label="${entry#* }"
    printf "  Shape %s -- %-24s  " "$n" "$label"
    printf "${CSI}${n} q"
    pause_for_input "(press Enter)" 0.3
done
printf "${CSI}0 q"
ok "Cursor reset to default"

section "Cursor Blink and Colour"
printf "${CSI}5 q${CSI}?12l${OSC}12;#ff8800${ST}  Steady orange bar "
pause_for_input "(press Enter)" 0.3
printf "${CSI}?12h  Blinking orange bar "
pause_for_input "(press Enter)" 0.3
printf "${OSC}112${ST}${CSI}0 q"

section "Unicode Width and Alignment"
printf "  CJK wide chars:   |中文日本語한국어|\n"
printf "  Emoji sequence:   |😀 🎉 🦀 🔥 ✅ ❌|\n"
printf "  Grapheme clusters: |é|❤️|👍🏽|🇳🇱|1️⃣|👩‍👩‍👧‍👦|\n"
printf "  Box drawing:      ┌─┬─┐  ╔═╦═╗  ├─┼─┤\n"
printf "                    │ │ │  ║ ║ ║  └─┴─┘\n"
printf "  Braille spinner:  ⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏\n"
printf "  Powerline glyphs: \ue0b0\ue0b1\ue0b2\ue0b3 (needs Nerd Font)\n"
expect "The closing | markers should line up; CJK and emoji should occupy two columns without shifting later text left or right."
diagnose "If the trailing markers drift, inspect wide-character width accounting in renderPane or reflow."

script_done
