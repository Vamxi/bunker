package main

import (
	"bytes"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/gdamore/tcell/v2"
	"golang.org/x/sys/unix"
	"golang.org/x/term"
)

type hostOSCColors struct {
	fg     string
	bg     string
	cursor string
}

func oscCursorColor(s string) tcell.Color {
	s = normalizeOSCColorReply(s)
	if s == "" {
		return tcell.ColorReset
	}
	parts := strings.Split(strings.TrimPrefix(s, "rgb:"), "/")
	var rgb [3]int32
	for i, part := range parts {
		n, err := strconv.ParseUint(part, 16, 16)
		if err != nil {
			return tcell.ColorReset
		}
		rgb[i] = int32(n * 255 / ((1 << uint(4*len(part))) - 1))
	}
	return tcell.NewRGBColor(rgb[0], rgb[1], rgb[2])
}

// defaultOSCColors resolves the fallback colours bunk exposes to pane apps for
// OSC 10/11/12 queries. Built-in themes provide explicit fg/bg colours, while
// the "terminal" theme probes the host terminal at startup and uses those
// values when available.
func defaultOSCColors(theme resolvedTheme, host hostOSCColors) (fg, bg, cursor string) {
	fg = tcellColorToXParse(theme.fg)
	if fg == "" {
		fg = host.fg
	}

	bg = tcellColorToXParse(theme.bg)
	if bg == "" {
		bg = host.bg
	}

	// bunk's built-in themes do not model a dedicated cursor colour, so the
	// existing fallback is the theme foreground. In terminal-inherit mode the
	// foreground is unknown, so only use a host-probed cursor colour.
	cursor = tcellColorToXParse(theme.fg)
	if cursor == "" {
		cursor = host.cursor
	}
	return fg, bg, cursor
}

func needsHostOSCColorProbe(theme resolvedTheme) bool {
	fg, bg, cursor := defaultOSCColors(theme, hostOSCColors{})
	return fg == "" || bg == "" || cursor == ""
}

// probeHostOSCColors queries the outer terminal for its default foreground,
// background, and cursor colours before tcell takes ownership of stdin.
// Unsupported or malformed replies are treated as unknown and left empty.
func probeHostOSCColors() hostOSCColors {
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		L.Debug("probeHostOSCColors: open /dev/tty", "err", err)
		return hostOSCColors{}
	}
	defer tty.Close() //nolint:errcheck // /dev/tty close on shutdown path

	oldState, err := term.MakeRaw(int(tty.Fd()))
	if err != nil {
		L.Debug("probeHostOSCColors: raw mode", "err", err)
		return hostOSCColors{}
	}
	defer term.Restore(int(tty.Fd()), oldState) //nolint:errcheck

	queries := []int{10, 11, 12}
	for _, num := range queries {
		if _, err := fmt.Fprintf(tty, "\x1b]%d;?\x1b\\", num); err != nil {
			L.Debug("probeHostOSCColors: write query", "osc_num", num, "err", err)
			return hostOSCColors{}
		}
	}

	replies := readHostOSCReplies(int(tty.Fd()), queries, 50*time.Millisecond, 50*time.Millisecond)
	colors := hostOSCColors{
		fg:     replies[10],
		bg:     replies[11],
		cursor: replies[12],
	}
	L.Debug("probeHostOSCColors: done", "fg", colors.fg, "bg", colors.bg, "cursor", colors.cursor)
	return colors
}

// readHostOSCReplies reads OSC colour replies with an adaptive timeout.
// It waits up to initialTimeout for the first reply.  If no reply arrives the
// terminal likely doesn't support these queries and we bail early.  Once the
// first reply lands the deadline is extended by extendedTimeout to collect
// remaining replies (which typically arrive within microseconds of the first).
func readHostOSCReplies(fd int, queries []int, initialTimeout, extendedTimeout time.Duration) map[int]string {
	found := make(map[int]string, len(queries))
	if len(queries) == 0 {
		return found
	}

	deadline := time.Now().Add(initialTimeout)
	extended := false
	var buf []byte
	tmp := make([]byte, 256)

	for len(found) < len(queries) && time.Now().Before(deadline) {
		wait := time.Until(deadline)
		if wait <= 0 {
			break
		}
		ms := int(wait / time.Millisecond)
		if ms < 1 {
			ms = 1
		}

		pollFds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		n, err := unix.Poll(pollFds, ms)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			L.Debug("readHostOSCReplies: poll", "err", err)
			break
		}
		if n == 0 {
			break
		}

		nr, err := unix.Read(fd, tmp)
		if err == unix.EINTR || err == unix.EAGAIN {
			continue
		}
		if err != nil {
			L.Debug("readHostOSCReplies: read", "err", err)
			break
		}
		if nr == 0 {
			break
		}

		prevFound := len(found)
		buf = append(buf, tmp[:nr]...)
		for _, num := range queries {
			if _, ok := found[num]; ok {
				continue
			}
			if color, ok := parseOSCColorReply(buf, num); ok {
				found[num] = color
			}
		}
		if !extended && len(found) > prevFound {
			deadline = time.Now().Add(extendedTimeout)
			extended = true
		}
	}
	return found
}

// parseOSCColorReply extracts a complete OSC num colour reply from buf.
// It accepts BEL and ST terminators. The returned bool is true only when a
// complete reply for the requested OSC number was found in the buffer.
func parseOSCColorReply(buf []byte, num int) (string, bool) {
	prefix := []byte(fmt.Sprintf("\x1b]%d;", num))
	start := bytes.Index(buf, prefix)
	if start < 0 {
		return "", false
	}

	body := buf[start+len(prefix):]
	end := len(body)
	complete := false
	if bel := bytes.IndexByte(body, '\a'); bel >= 0 {
		end = bel
		complete = true
	}
	if st := bytes.Index(body, []byte("\x1b\\")); st >= 0 && (!complete || st < end) {
		end = st
		complete = true
	}
	if !complete {
		return "", false
	}

	return normalizeOSCColorReply(string(body[:end])), true
}

func normalizeOSCColorReply(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	if s == "" {
		return ""
	}

	if strings.HasPrefix(s, "rgb:") {
		parts := strings.Split(strings.TrimPrefix(s, "rgb:"), "/")
		if len(parts) != 3 {
			return ""
		}
		for _, part := range parts {
			if len(part) < 1 || len(part) > 4 || !isHexString(part) {
				return ""
			}
		}
		return "rgb:" + parts[0] + "/" + parts[1] + "/" + parts[2]
	}

	if strings.HasPrefix(s, "#") {
		hex := strings.TrimPrefix(s, "#")
		if len(hex) != 6 || !isHexString(hex) {
			return ""
		}
		return xParseColor(hex)
	}

	return ""
}

func isHexString(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'f':
		case r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}
