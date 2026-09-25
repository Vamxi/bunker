#!/bin/sh
# Run a command against a private headless GNOME compositor (mutter) with its
# own D-Bus session, so GUI tests render whether or not the desktop is
# locked, and never touch the user's session. Used by `make test`.
#
#   dbus-run-session -- scripts/headless-gui.sh go test ./...
set -e
export WAYLAND_DISPLAY="wl-bunker-test-$$"
unset DISPLAY
export GDK_DEBUG=no-portals   # no desktop portals in the throwaway session
export GTK_A11Y=none NO_AT_BRIDGE=1
export GIO_USE_VFS=local     # no gvfs daemon either
log="${TMPDIR:-/tmp}/bunker-mutter-$$.log"
mutter --headless --wayland --no-x11 --virtual-monitor 1600x1000 \
	--wayland-display "$WAYLAND_DISPLAY" >"$log" 2>&1 &
compositor=$!
trap 'kill $compositor 2>/dev/null; wait $compositor 2>/dev/null; rm -f "$log"' EXIT
i=0
until [ -S "${XDG_RUNTIME_DIR:?}/$WAYLAND_DISPLAY" ]; do
	i=$((i + 1))
	if [ $i -gt 100 ]; then
		echo "headless mutter did not start:" >&2
		cat "$log" >&2
		exit 1
	fi
	sleep 0.05
done
"$@"
