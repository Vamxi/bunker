#!/usr/bin/env bash
# osc_smoke.sh -- automated OSC/query smoke tests with PASS/FAIL assertions.
#
# Run inside a bunk pane attached to a real terminal:
#   bash scripts/osc_smoke.sh
#
# What it checks:
#   - same-chunk alt-screen + OSC 10/11/12 set/query ordering
#   - OSC 110/111/112 reset semantics against the pane's baseline colours
#   - normal-mode OSC 10/11/12 responses
#   - same-chunk DECRQM state updates

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

PASS=0
FAIL=0

assert_eq() {
    local name="$1" got="$2" want="$3"
    if [[ "$got" == "$want" ]]; then
        PASS=$((PASS + 1))
        printf 'PASS  %s\n' "$name"
    else
        FAIL=$((FAIL + 1))
        printf 'FAIL  %s\n' "$name"
        printf '      got : %s\n' "$(escaped "$got")"
        printf '      want: %s\n' "$(escaped "$want")"
    fi
}

assert_empty() {
    local name="$1" got="$2"
    if [[ -z "$got" ]]; then
        PASS=$((PASS + 1))
        printf 'PASS  %s\n' "$name"
    else
        FAIL=$((FAIL + 1))
        printf 'FAIL  %s\n' "$name"
        printf '      got : %s\n' "$(escaped "$got")"
        printf '      want: <empty>\n'
    fi
}

# query_raw: like query_terminal but returns raw bytes (no cat -v escaping),
# so assert_eq can compare against literal escape sequences.
query_raw() {
    local query="$1"
    local timeout="${2:-0.35}"
    local old_stty response ch

    old_stty=$(stty -g 2>/dev/null) || return 1
    stty -echo -icanon min 0 time 0 2>/dev/null || return 1
    # drain any stale input
    while IFS= read -r -s -t 0.05 -n 1 ch 2>/dev/null; do :; done
    printf '%s' "$query" >&3
    sleep "$timeout"
    response=""
    while IFS= read -r -s -t 0.05 -n 1 ch 2>/dev/null; do
        response+="$ch"
        [[ ${#response} -gt 400 ]] && break
    done
    stty "$old_stty" 2>/dev/null
    printf '%s' "$response"
}

main() {
    require_query_tty

    printf 'bunk OSC smoke test\n\n'

    local baseline10 baseline11 baseline12
    baseline10=$(query_raw "${CSI}?1049h${OSC}10;?${ST}${CSI}?1049l")
    baseline11=$(query_raw "${CSI}?1049h${OSC}11;?${ST}${CSI}?1049l")
    baseline12=$(query_raw "${CSI}?1049h${OSC}12;?${ST}${CSI}?1049l")

    printf 'Baseline responses:\n'
    printf '  OSC 10: %s\n' "$(escaped "$baseline10")"
    printf '  OSC 11: %s\n' "$(escaped "$baseline11")"
    printf '  OSC 12: %s\n\n' "$(escaped "$baseline12")"

    local got

    got=$(query_raw "${CSI}?1049h${OSC}10;rgb:1111/2222/3333${ST}${OSC}10;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 10 set/query in alt-screen" "$got" "${OSC}10;rgb:1111/2222/3333${ST}"

    got=$(query_raw "${CSI}?1049h${OSC}11;rgb:4444/5555/6666${ST}${OSC}11;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 11 set/query in alt-screen" "$got" "${OSC}11;rgb:4444/5555/6666${ST}"

    got=$(query_raw "${CSI}?1049h${OSC}12;rgb:7777/8888/9999${ST}${OSC}12;?${ST}${CSI}?1049l")
    assert_eq "same-chunk OSC 12 set/query in alt-screen" "$got" "${OSC}12;rgb:7777/8888/9999${ST}"

    got=$(query_raw "${CSI}?1049h${OSC}10;rgb:1111/2222/3333${ST}${OSC}110${BEL}${OSC}10;?${ST}${CSI}?1049l")
    assert_eq "OSC 110 reset restores baseline fg response" "$got" "$baseline10"

    got=$(query_raw "${CSI}?1049h${OSC}11;rgb:4444/5555/6666${ST}${OSC}111${BEL}${OSC}11;?${ST}${CSI}?1049l")
    assert_eq "OSC 111 reset restores baseline bg response" "$got" "$baseline11"

    got=$(query_raw "${CSI}?1049h${OSC}12;rgb:7777/8888/9999${ST}${OSC}112${BEL}${OSC}12;?${ST}${CSI}?1049l")
    assert_eq "OSC 112 reset restores baseline cursor response" "$got" "$baseline12"

    got=$(query_raw "${OSC}10;?${ST}")
    assert_eq "normal-mode OSC 10 response" "$got" "$baseline10"

    got=$(query_raw "${OSC}11;?${ST}")
    assert_eq "normal-mode OSC 11 response" "$got" "$baseline11"

    got=$(query_raw "${OSC}12;?${ST}")
    assert_eq "normal-mode OSC 12 response" "$got" "$baseline12"

    got=$(query_raw "${CSI}?2004h${CSI}?2004\$p${CSI}?2004l${CSI}?2004\$p")
    assert_eq "same-chunk DECRQM state updates" "$got" "${CSI}?2004;1\$y${CSI}?2004;2\$y"

    printf '\nSummary: PASS=%d FAIL=%d\n' "$PASS" "$FAIL"
    [[ ${FAIL} -eq 0 ]]
}

main "$@"
