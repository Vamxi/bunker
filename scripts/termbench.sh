#!/bin/bash
# termbench.sh - how fast terminals take program output. Each terminal runs
# a script that times `cat` of each workload; the time is bound by how fast
# the terminal reads, parses, and draws the output, since a terminal that
# falls behind stops reading and `cat` waits. The terminal's peak memory is
# read at the end. Windows open on the current desktop, one at a time.
#
#   scripts/termbench.sh [terminal...]
#
# Terminals: bunker (target/release/bunker), ptyxis, foot, kitty, tilix,
# bunk-gui (KITTY, TILIX, and BUNK_GUI name other binaries, such as
# unpacked ones). The workloads are
# the emulator benchmark's (crates/vt/examples/workloads.rs), each repeated
# COPIES times (default 8); every one runs RUNS times (default 3) and the
# fastest run counts. Results are milliseconds, lower is better.
#
# Grids are SIZE (default 100x30) where the terminal allows it: foot takes
# it as a flag, as do kitty (with its own config ignored), Tilix, and
# bunk-gui, and
# Ptyxis's default and saved window sizes are set for the run and restored
# after. bunker opens at 100x30 on its own.
set -euo pipefail

root=$(cd "$(dirname "$0")/.." && pwd)
copies=${COPIES:-8}
runs=${RUNS:-3}
size=${SIZE:-100x30}
terminals=("$@")
[ ${#terminals[@]} -eq 0 ] && terminals=(bunker ptyxis foot)
workloads=(seq prose colors unicode tui)

work=$(mktemp -d)
ptyxis_keys=(window-size default-columns default-rows)
declare -A ptyxis_saved=()
cleanup() {
	for k in "${!ptyxis_saved[@]}"; do gsettings set org.gnome.Ptyxis "$k" "${ptyxis_saved[$k]}"; done
	rm -rf "$work"
}
trap cleanup EXIT
if printf '%s\n' "${terminals[@]}" | grep -qx ptyxis; then
	for k in "${ptyxis_keys[@]}"; do ptyxis_saved[$k]=$(gsettings get org.gnome.Ptyxis "$k"); done
	gsettings set org.gnome.Ptyxis window-size "(uint32 ${size%x*}, uint32 ${size#*x})"
	gsettings set org.gnome.Ptyxis default-columns "${size%x*}"
	gsettings set org.gnome.Ptyxis default-rows "${size#*x}"
fi
cargo run --release -q --manifest-path "$root/Cargo.toml" -p bunker-vt --example workloads -- "$work" "$copies"

# The script each terminal runs: times every workload, writes one line per
# workload, then waits until the runner has read the terminal's memory.
cat >"$work/inner.sh" <<'EOF'
name=$1 work=$2 runs=$3
shift 3
sleep 1 # let the window reach its size
size=$(stty size | awk '{ print $2 "x" $1 }')
for w in "$@"; do
	best=
	for _ in $(seq "$runs"); do
		start=$(date +%s%N)
		cat "$work/$w.bin"
		ms=$((($(date +%s%N) - start) / 1000000))
		if [ -z "$best" ] || [ "$ms" -lt "$best" ]; then best=$ms; fi
	done
	echo "$name $w $best $size" >>"$work/results"
done
touch "$work/$name.done"
while [ ! -e "$work/$name.exit" ]; do sleep 0.1; done
EOF

inner=(bash "$work/inner.sh")
for t in "${terminals[@]}"; do
	args=("$t" "$work" "$runs" "${workloads[@]}")
	case $t in
	bunker) cmd=("$root/target/release/bunker" -- "${inner[@]}" "${args[@]}") ;;
	ptyxis) cmd=(ptyxis -s -x "${inner[*]} ${args[*]}") ;;
	foot) cmd=(foot -W "$size" "${inner[@]}" "${args[@]}") ;;
	kitty) cmd=("${KITTY:-kitty}" --config NONE -o remember_window_size=no
		-o "initial_window_width=${size%x*}c" -o "initial_window_height=${size#*x}c"
		"${inner[@]}" "${args[@]}") ;;
	tilix) cmd=("${TILIX:-tilix}" --new-process --geometry="$size" -e "${inner[*]} ${args[*]}") ;;
	bunk-gui) cmd=("${BUNK_GUI:-bunk-gui}" --size "$size" --command "${inner[*]} ${args[*]}") ;;
	*)
		echo "unknown terminal $t" >&2
		continue
		;;
	esac
	echo "==> $t" >&2
	"${cmd[@]}" >/dev/null 2>&1 &
	pid=$!
	for _ in $(seq 3000); do
		[ -e "$work/$t.done" ] && break
		sleep 0.1
	done
	peak=$(awk '/VmHWM/ {printf "%.0f", $2 / 1024}' "/proc/$pid/status" 2>/dev/null || echo "?")
	echo "$t peak_mb $peak" >>"$work/results"
	touch "$work/$t.exit"
	sleep 1
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
done

echo
printf '%-11s' "ms"
for w in "${workloads[@]}"; do
	printf '%9s' "$w"
done
printf '%9s %9s %8s\n' "total" "peak MB" "grid"
printf '%-11s' ""
for w in "${workloads[@]}"; do
	awk -v b="$(stat -c %s "$work/$w.bin")" 'BEGIN { printf "%7.1fMB", b / 1e6 }'
done
echo
for t in "${terminals[@]}"; do
	printf '%-11s' "$t"
	grid= total=0
	for w in "${workloads[@]}"; do
		line=$(grep "^$t $w " "$work/results" || true)
		if [ -z "$line" ]; then
			printf '%9s' "-"
			continue
		fi
		read -r _ _ ms grid <<<"$line"
		total=$((total + ms))
		printf '%9s' "$ms"
	done
	peak=$(awk -v t="$t" '$1 == t && $2 == "peak_mb" { print $3 }' "$work/results")
	printf '%9s %9s %8s\n' "$total" "${peak:--}" "${grid:--}"
done
