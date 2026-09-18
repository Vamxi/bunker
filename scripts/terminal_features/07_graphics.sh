#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

section "Pane-local static graphics"
info "Images are rendered as coloured half-block cells; native host graphics are not required."
expect "Each image stays inside its pane. Split/resize and scroll back to check clipping and persistence."

section "SIXEL: red above blue"
printf '%s' "${DCS}0;1q#1;2;100;0;0!64~-!64~-!64~-!64~-#2;2;0;0;100!64~-!64~-!64~-!64~${ST}"
printf '\r\n'

section "Kitty: red above blue, scaled to 8 by 3 cells"
printf '%s' "${ESC}_Ga=T,f=24,s=1,v=2,c=8,r=3,q=2;/wAAAAD/${ST}"
printf '\r\n'

section "iTerm2: red GIF, scaled to 8 by 3 cells"
printf '%s' "${OSC}1337;File=inline=1;width=8;height=3;preserveAspectRatio=0:R0lGODdhAQABAIAAAP8AAAAAACwAAAAAAQABAAACAkQBADs=${BEL}"
printf '\r\n'
note "Only static in-band images are supported. Animation, native layers, files, and shared memory are not."
pause_for_input "  Resize the pane, inspect scrollback, then press Enter. "
script_done
