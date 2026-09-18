#!/usr/bin/env bash
# terminal_features.sh -- entrypoint for grouped terminal feature diagnostics.
#
# Usage:
#   bash terminal_features.sh list
#   bash terminal_features.sh all
#   bash terminal_features.sh text
#   bash terminal_features.sh colors
#   bash terminal_features.sh cursor
#   bash terminal_features.sh integration
#   bash terminal_features.sh queries
#   bash terminal_features.sh osc
#   bash terminal_features.sh graphics
#
# Notes:
#   - `all` runs every grouped script in order.
#   - Set TERMINAL_FEATURES_AUTO=1 to auto-advance interactive pauses.
#   - Query-heavy scripts need a real interactive terminal on stdin/stdout.

set -u

ROOT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
SCRIPT_DIR="${ROOT_DIR}/scripts/terminal_features"

usage() {
    cat <<'EOF'
Usage:
  bash terminal_features.sh list
  bash terminal_features.sh all
  bash terminal_features.sh text
  bash terminal_features.sh colors
  bash terminal_features.sh cursor
  bash terminal_features.sh integration
  bash terminal_features.sh queries
  bash terminal_features.sh osc
  bash terminal_features.sh graphics

Groups:
  text         SGR attributes and underline styles
  colors       ANSI, 256-color, truecolor, colored underlines
  cursor       Cursor shapes and Unicode width/alignment
  integration  Hyperlinks, bracketed paste, OSC 133 prompt markers
  queries      DA/DA2/XTVERSION/CPR/XTGETTCAP/DECRQM/OSC color queries/kitty
  osc          Automated PASS/FAIL OSC set/query/reset and DECRQM assertions
  graphics     SIXEL, Kitty, and iTerm2 cell-rendered static images
EOF
}

list_groups() {
    cat <<EOF
Available grouped diagnostics:
  text         ${SCRIPT_DIR}/01_text_attributes.sh
               SGR styling, underline variants, and combined attributes
  colors       ${SCRIPT_DIR}/02_colors.sh
               ANSI palette, 256-color, truecolor, underline colour
  cursor       ${SCRIPT_DIR}/03_cursor_unicode.sh
               DECSCUSR cursor shapes and wide-character alignment
  integration  ${SCRIPT_DIR}/04_host_integration.sh
               OSC 8 hyperlinks, bracketed paste, OSC 133 markers
  queries      ${SCRIPT_DIR}/05_queries.sh
               DA/DA2/XTVERSION/CPR/XTGETTCAP/DECRQM/OSC/kitty queries
  osc          ${SCRIPT_DIR}/06_osc_smoke.sh
               Automated OSC 10/11/12 set/query/reset and DECRQM assertions
  graphics     ${SCRIPT_DIR}/07_graphics.sh
               Static images, pane clipping, resize, and scrollback

Run all groups:
  bash terminal_features.sh all
EOF
}

run_group() {
    local script="$1"
    bash "${SCRIPT_DIR}/${script}"
}

run_all() {
    local script
    for script in "${SCRIPT_DIR}"/[0-9]*.sh; do
        local base
        base="$(basename "$script")"
        # skip query-heavy scripts when not on a TTY
        case "$base" in
            05_*|06_*)
                if [[ ! -t 0 || ! -t 1 ]]; then
                    printf 'Skipping %s: requires an interactive TTY on stdin/stdout.\n' "$base" >&2
                    continue
                fi
                ;;
        esac
        run_group "$base" || return 1
    done
}

case "${1:-all}" in
    list)
        list_groups
        ;;
    all)
        run_all
        ;;
    text|styles)
        run_group 01_text_attributes.sh
        ;;
    colors)
        run_group 02_colors.sh
        ;;
    cursor|unicode)
        run_group 03_cursor_unicode.sh
        ;;
    integration|host)
        run_group 04_host_integration.sh
        ;;
    queries|protocols)
        run_group 05_queries.sh
        ;;
    osc|smoke)
        run_group 06_osc_smoke.sh
        ;;
    graphics|images)
        run_group 07_graphics.sh
        ;;
    *)
        usage >&2
        exit 1
        ;;
esac
