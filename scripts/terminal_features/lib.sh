#!/usr/bin/env bash

set -u

ESC=$'\033'
CSI="${ESC}["
OSC="${ESC}]"
DCS="${ESC}P"
ST="${ESC}\\"
BEL=$'\007'
R="${CSI}0m"

# Save original stdout (the terminal's PTY) to fd 3 so query functions can
# write escape sequences to the terminal even inside $(...) captures.
if [[ -t 1 ]]; then
    exec 3>&1
else
    exec 3>/dev/null
fi

hr() {
    printf '\n%s\n' "$(printf '─%.0s' {1..72})"
}

section() {
    hr
    printf "${CSI}1;36m▶ %s${R}\n" "$1"
}

info() {
    printf "  ${CSI}33m•${R} %s\n" "$1"
}

ok() {
    printf "  ${CSI}32m✔${R} %s\n" "$1"
}

warn() {
    printf "  ${CSI}31m!${R} %s\n" "$1"
}

expect() {
    printf "  Expect: %s\n" "$1"
}

diagnose() {
    printf "  Diagnose: %s\n" "$1"
}

note() {
    printf "  Note: %s\n" "$1"
}

escaped() {
    printf '%s' "$1" | cat -v
}

pause_for_input() {
    local prompt="$1"
    local auto_delay="${2:-0.2}"
    printf "%s" "$prompt"
    if [[ "${TERMINAL_FEATURES_AUTO:-0}" == "1" || ! -t 0 ]]; then
        sleep "${auto_delay}"
        printf "\n"
        return
    fi
    read -r
}

timed_pause() {
    local seconds="${1:-5}"
    if [[ "${TERMINAL_FEATURES_AUTO:-0}" == "1" || ! -t 0 ]]; then
        sleep 0.2
        return
    fi
    read -r -t "${seconds}" || true
}

require_query_tty() {
    if [[ ! -t 0 || ! -t 1 ]]; then
        printf 'This script requires an interactive TTY on stdin/stdout.\n' >&2
        printf 'Run it directly in a terminal, not through a pipe.\n' >&2
        exit 1
    fi
}

query_terminal() {
    local query="$1"
    local timeout="${2:-0.5}"
    local old_stty response ch

    old_stty=$(stty -g 2>/dev/null) || return 1
    stty -echo -icanon min 0 time 0 2>/dev/null || return 1
    printf '%s' "$query" >&3
    sleep "$timeout"
    response=""
    while IFS= read -r -s -t 0.05 -n 1 ch 2>/dev/null; do
        response+="$ch"
        [[ ${#response} -gt 200 ]] && break
    done
    stty "$old_stty" 2>/dev/null
    printf '%s' "$response" | cat -v
}

query_terminal_alt() {
    local query="$1"
    local timeout="${2:-0.5}"
    local old_stty response ch

    old_stty=$(stty -g 2>/dev/null) || return 1
    stty -echo -icanon min 0 time 0 2>/dev/null || return 1
    printf '%s' "${CSI}?1049h" >&3
    sleep 0.05
    printf '%s' "$query" >&3
    sleep "$timeout"
    response=""
    while IFS= read -r -s -t 0.05 -n 1 ch 2>/dev/null; do
        response+="$ch"
        [[ ${#response} -gt 200 ]] && break
    done
    printf '%s' "${CSI}?1049l" >&3
    stty "$old_stty" 2>/dev/null
    printf '%s' "$response" | cat -v
}

script_done() {
    hr
    printf "${CSI}1;32mDone.${R}\n"
}
