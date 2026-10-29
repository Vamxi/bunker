#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

section "ANSI Palette"
info "This script focuses on color rendering: ANSI 16-color, 256-color, truecolor, and colored underlines."
expect "Color ramps should be smooth, palette blocks should be distinct, and resets should prevent color bleed into following text."
diagnose "If entire sections go monochrome or show wrong hues, inspect SGR colour parsing (crates/vt/src/csi.rs) and colour resolution (ui/render.rs)."

section "ANSI 8 + Bright 16"
printf "  FG (30-37):  "
for c in 30 31 32 33 34 35 36 37; do
    printf "${CSI}${c}m█${R}"
done
printf "   ${CSI}1m"
for c in 30 31 32 33 34 35 36 37; do
    printf "${CSI}${c}m█${R}"
done
printf "${R}\n"

printf "  BG (40-47):  "
for c in 40 41 42 43 44 45 46 47; do
    printf "${CSI}${c}m  ${R}"
done
printf "\n"

printf "  Bright BG:   "
for c in 100 101 102 103 104 105 106 107; do
    printf "${CSI}${c}m  ${R}"
done
printf "\n"
expect "All palette entries should be visibly different. Bright entries should not collapse to the dark set."
diagnose "If reverse video or default colors look wrong here, inspect render-side default fg/bg mapping."

section "256-Color Palette"
printf "  System 0-15:  "
for i in {0..15}; do
    printf "${CSI}48;5;${i}m  ${R}"
done
printf "\n"

printf "  Color cube:   "
for i in {16..231}; do
    printf "${CSI}48;5;${i}m ${R}"
    if ((( (i - 15) % 36 ) == 0 )); then
        printf "\n                "
    fi
done
printf "\n"

printf "  Greys 232-255:"
for i in {232..255}; do
    printf "${CSI}48;5;${i}m ${R}"
done
printf "\n"
expect "The color cube should show orderly hue changes and the grey ramp should move from dark to light without abrupt jumps."
diagnose "If everything after color 15 disappears or flattens, inspect 48;5 parsing in csi.rs or the palette in crates/vt/src/colors.rs."

section "Truecolor Gradients"
printf "  Red gradient:    "
for i in {0..71}; do
    r=$(( i * 255 / 71 ))
    printf "${CSI}48;2;${r};0;0m ${R}"
done
printf "\n"

printf "  Green gradient:  "
for i in {0..71}; do
    g=$(( i * 255 / 71 ))
    printf "${CSI}48;2;0;${g};0m ${R}"
done
printf "\n"

printf "  Blue gradient:   "
for i in {0..71}; do
    b=$(( i * 255 / 71 ))
    printf "${CSI}48;2;0;0;${b}m ${R}"
done
printf "\n"

printf "  Rainbow:         "
for i in {0..71}; do
    deg=$(( i * 360 / 72 ))
    h=$(( deg / 60 ))
    f=$(( (deg % 60) * 255 / 59 ))
    q=$(( 255 - f ))
    case $h in
        0) r=255; g=$f;  b=0   ;;
        1) r=$q;  g=255; b=0   ;;
        2) r=0;   g=255; b=$f  ;;
        3) r=0;   g=$q;  b=255 ;;
        4) r=$f;  g=0;   b=255 ;;
        *) r=255; g=0;   b=$q  ;;
    esac
    printf "${CSI}48;2;${r};${g};${b}m ${R}"
done
printf "\n"
expect "Gradients should be smooth, with no banding into a small ANSI palette."
diagnose "If gradients posterize or collapse, inspect 48;2 parsing and RGB conversion."

section "Colored Underlines and Mixed Styling"
printf "  %-30s ${CSI}4;58;5;196mRed 256-color underline${R}\n"      "4 + 58;5;196"
printf "  %-30s ${CSI}4;58;5;82mGreen 256-color underline${R}\n"     "4 + 58;5;82"
printf "  %-30s ${CSI}4;58;2;255;80;0mOrange RGB underline${R}\n"    "4 + 58;2;255;80;0"
printf "  %-30s ${CSI}4:3;58;2;255;0;128mCurly+colored underline${R}\n" "4:3 + 58;2;255;0;128"
printf "  %-30s ${CSI}2;38;2;100;200;255mDim + true-color text${R}\n" "2 + 38;2"
expect "Underline color should differ from text color, and dim+truecolor should still look faded rather than full-bright."
diagnose "If underline color is ignored, inspect SGR 58 handling. If dim vanishes on RGB text, inspect render-side dim blending."

script_done
