#!/usr/bin/env bash

set -u

SCRIPT_DIR="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)"
# shellcheck source=lib.sh
. "${SCRIPT_DIR}/lib.sh"

require_query_tty

section "Terminal Queries"
info "This script exercises protocol-level terminal queries and prints raw responses in cat -v form for quick diagnosis."
expect "Each query should return a recognizable response promptly. Missing, delayed, or malformed replies usually indicate parser or passthrough regressions."
diagnose "When a specific query fails, check the subsystem named beside it: vt10x CSI/STR handling, pane query scanner, or OSC passthrough."

section "DA1 - Primary Device Attributes"
printf "  Query:    ^[[c\n"
printf "  Response: "
r=$(query_terminal "${CSI}c")
printf '%s\n' "$r"
expect "Bunk typically reports ^[[?62;1;2;4;6;9;15;22c"
diagnose "If empty, inspect pane-side DA handling."

section "DA2 - Secondary Device Attributes"
printf "  Query:    ^[[>c\n"
printf "  Response: "
r=$(query_terminal "${CSI}>c")
printf '%s\n' "$r"
expect "Bunk typically reports ^[[>0;279;0c"
diagnose "If malformed, inspect DA2 handling in pane query responses."

section "DA3, DSR, and DECRQSS"
printf '  DA3: %s\n' "$(query_terminal "${CSI}=c")"
printf '  DSR: %s\n' "$(query_terminal "${CSI}5n")"
printf '  SGR setting: %s\n' "$(query_terminal "${DCS}\$qm${ST}")"
expect "DA3 returns DCS !|00000000 ST; DSR returns CSI 0n; the SGR setting returns DCS 1\$r ... m ST."

section "XTVERSION"
printf "  Query:    ^[[>0q\n"
printf "  Response: "
r=$(query_terminal "${CSI}>0q")
printf '%s\n' "$r"
expect "Bunk typically reports DCS >|VTE(8203) ST"
diagnose "If missing, inspect XTVERSION detection in pane query scanning."

section "CPR - Cursor Position Report"
printf "  Query:    ^[[6n\n"
printf "  Response: "
r=$(query_terminal "${CSI}6n")
printf '%s\n' "$r"
expect "A row/col reply such as ^[[12;1R"
diagnose "If empty in either screen mode, inspect CPR query handling."

section "XTGETTCAP"
printf "  Query Smulx:  DCS +q 536d756c78 ST\n"
printf "  Response:     "
r=$(query_terminal "${DCS}+q536d756c78${ST}")
printf '%s\n' "$r"
expect "Found: DCS 1+r ... or not found: DCS 0+r ..."
diagnose "If capabilities regress, inspect xtgettcapResponse and DCS query scanning."

printf "  Query Setulc: DCS +q 536574756c63 ST\n"
printf "  Response:     "
r=$(query_terminal "${DCS}+q536574756c63${ST}")
printf '%s\n' "$r"
expect "Found: DCS 1+r ... with the underline-color capability value"
diagnose "If Setulc disappears, inspect underline-color capability wiring."

printf '  Name / indexed colors / RGB depth: %s\n' "$(query_terminal "${DCS}+q544e;436f;524742${ST}")"
expect "TN=xterm-256color, Co=256, RGB=8, with hex-encoded values."
printf '  Application cursor Up: %s\n' "$(query_terminal "${CSI}?1h${DCS}+q6b63757531${ST}${CSI}?1l")"
expect "kcuu1=1b4f41 (ESC O A) while application cursor mode is enabled."

section "DECRQM"
printf "  Query mode 2026: ^[[?2026\$p\n"
printf "  Response:        "
r=$(query_terminal "${CSI}?2026\$p")
printf '%s\n' "$r"
expect "^[[?2026;1\$y when active or ^[[?2026;2\$y when reset"
diagnose "If status is stale, inspect same-stream mode update ordering."

printf "  Query mode 2004: ^[[?2004\$p\n"
printf "  Response:        "
r=$(query_terminal "${CSI}?2004\$p")
printf '%s\n' "$r"
expect "^[[?2004;2\$y when bracketed paste is currently reset"
diagnose "If this never changes, inspect vt10x QueryPrivateMode or pane DECRQM replies."

section "OSC 10/11/12 Color Queries"
printf "  Query OSC 10 in alt-screen: CSI ?1049h, OSC 10;? ST, CSI ?1049l\n"
printf "  Response: "
r=$(query_terminal_alt "${OSC}10;?${ST}")
printf '%s\n' "$r"
expect "OSC 10;rgb:xxxx/xxxx/xxxx ST"
diagnose "If empty only in bunk, inspect theme defaults and OSC query ordering."

printf "  Query OSC 11 in alt-screen: CSI ?1049h, OSC 11;? ST, CSI ?1049l\n"
printf "  Response: "
r=$(query_terminal_alt "${OSC}11;?${ST}")
printf '%s\n' "$r"
expect "OSC 11;rgb:xxxx/xxxx/xxxx ST; empty is acceptable only when defaults are genuinely unknown"
diagnose "Both normal and alternate screens should answer with the same current colour."

printf "  Query OSC 12 in alt-screen: CSI ?1049h, OSC 12;? ST, CSI ?1049l\n"
printf "  Response: "
r=$(query_terminal_alt "${OSC}12;?${ST}")
printf '%s\n' "$r"
expect "OSC 12;rgb:xxxx/xxxx/xxxx ST when the cursor color is known; empty is acceptable otherwise"
diagnose "If set/query/reset semantics break, inspect vt10x OSC 12 and 112 handling."

section "Kitty Keyboard Protocol"
printf "  Query:    ^[[?u\n"
printf "  Response: "
r=$(query_terminal "${CSI}?u")
printf '%s\n' "$r"
expect "^[[NNu where NN is the current kitty keyboard flag bitmask"
diagnose "If the reply is missing or typing later behaves oddly, inspect kitty stack tracking and stripping."

script_done
