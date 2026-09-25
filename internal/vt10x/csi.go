package vt10x

import (
	"bytes"
	"fmt"
)

// CSI (Control Sequence Introducer)
// ESC+[
type csiEscape struct {
	buf  []byte
	args []int
	mode byte
	priv bool
}

func (c *csiEscape) reset() {
	c.buf = c.buf[:0]
	c.args = c.args[:0]
	c.mode = 0
	c.priv = false
}

func (c *csiEscape) put(b byte) bool {
	c.buf = append(c.buf, b)
	if b >= 0x40 && b <= 0x7E || len(c.buf) >= 256 {
		c.parse()
		return true
	}
	return false
}

func (c *csiEscape) parse() {
	c.mode = c.buf[len(c.buf)-1]
	if len(c.buf) == 1 {
		return
	}
	s := c.buf
	c.args = c.args[:0]
	if s[0] == '?' {
		c.priv = true
		s = s[1:]
	}
	s = s[:len(s)-1]
	// Parsed in place over the bytes: this runs for every SGR, and building
	// strings to split cost several allocations per colour change. Parsing
	// stops at the first parameter strconv.Atoi would reject, as it always
	// has (see TestCSIParseMatchesReference).
	for {
		p := s
		if k := bytes.IndexByte(s, ';'); k >= 0 {
			p, s = s[:k], s[k+1:]
		} else {
			s = nil
		}
		// Handle sub-parameters separated by ':' (e.g. "4:3" for curly underline,
		// "38:2:R:G:B" for RGB colour).  Encode as main*10000+sub so setAttr can
		// distinguish them from plain parameters without a separate data structure.
		// Only the FIRST sub-parameter is encoded; deeper sub-params (e.g. the
		// R:G:B in 38:2:R:G:B) are passed as separate ';'-equivalent args below.
		if idx := bytes.IndexByte(p, ':'); idx >= 0 {
			main, ok := atoi(p[:idx])
			if !ok {
				break
			}
			// For multi-sub-param forms (e.g. "38:2:255:0:128"), flatten all
			// sub-params into individual args so the existing colour-parsing
			// logic in setAttr can handle them identically to the ';' form.
			rest := p[idx+1:]
			first := rest
			if k := bytes.IndexByte(rest, ':'); k >= 0 {
				first, rest = rest[:k], rest[k+1:]
			} else {
				rest = nil
			}
			sub, ok := atoi(first)
			if !ok {
				sub = 0
			}
			c.args = append(c.args, main*10000+sub+1) // +1: distinguishes 4:0 (→40001) from plain 4 (→4)
			for rest != nil {
				sp := rest
				if k := bytes.IndexByte(rest, ':'); k >= 0 {
					sp, rest = rest[:k], rest[k+1:]
				} else {
					rest = nil
				}
				v, ok := atoi(sp)
				if !ok {
					break
				}
				c.args = append(c.args, v)
			}
		} else {
			i, ok := atoi(p)
			if !ok {
				break
			}
			c.args = append(c.args, i)
		}
		if s == nil {
			break
		}
	}
}

// atoi is strconv.Atoi over bytes without allocating: optional sign, then
// one or more decimal digits, within int range.
func atoi(b []byte) (int, bool) {
	neg := false
	if len(b) > 0 && (b[0] == '+' || b[0] == '-') {
		neg = b[0] == '-'
		b = b[1:]
	}
	if len(b) == 0 {
		return 0, false
	}
	const cutoff = uint64(1) << 63
	var n uint64
	for _, c := range b {
		if c < '0' || c > '9' {
			return 0, false
		}
		n = n*10 + uint64(c-'0')
		if n > cutoff {
			return 0, false
		}
	}
	if !neg && n == cutoff {
		return 0, false
	}
	if neg {
		return int(-n), true
	}
	return int(n), true
}

func (c *csiEscape) arg(i, def int) int {
	if i >= len(c.args) || i < 0 {
		return def
	}
	return c.args[i]
}

// maxarg takes the maximum of arg(i, def) and def
func (c *csiEscape) maxarg(i, def int) int {
	return max(c.arg(i, def), def)
}

func (t *State) handleCSI() {
	c := &t.csi
	switch c.mode {
	default:
		goto unknown
	case '@': // ICH - insert <n> blank char
		t.insertBlanks(c.arg(0, 1))
	case 'A': // CUU - cursor <n> up
		t.moveTo(t.cur.X, t.cur.Y-c.maxarg(0, 1))
	case 'B', 'e': // CUD, VPR - cursor <n> down
		t.moveTo(t.cur.X, t.cur.Y+c.maxarg(0, 1))
	case 'c': // DA - device attributes
		// TODO: when c.arg(0, 0) == 0, write the VT102 device-attributes
		// reply back to the host.
	case 'C', 'a': // CUF, HPR - cursor <n> forward
		t.moveTo(t.cur.X+c.maxarg(0, 1), t.cur.Y)
	case 'D': // CUB - cursor <n> backward
		t.moveTo(t.cur.X-c.maxarg(0, 1), t.cur.Y)
	case 'E': // CNL - cursor <n> down and first col
		t.moveTo(0, t.cur.Y+c.arg(0, 1))
	case 'F': // CPL - cursor <n> up and first col
		t.moveTo(0, t.cur.Y-c.arg(0, 1))
	case 'g': // TBC - tabulation clear
		switch c.arg(0, 0) {
		// clear current tab stop
		case 0:
			t.tabs[t.cur.X] = false
		// clear all tabs
		case 3:
			for i := range t.tabs {
				t.tabs[i] = false
			}
		default:
			goto unknown
		}
	case 'G', '`': // CHA, HPA - Move to <col>
		t.moveTo(c.arg(0, 1)-1, t.cur.Y)
	case 'H', 'f': // CUP, HVP - move to <row> <col>
		t.moveAbsTo(c.arg(1, 1)-1, c.arg(0, 1)-1)
	case 'I': // CHT - cursor forward tabulation <n> tab stops
		n := c.arg(0, 1)
		for i := 0; i < n; i++ {
			t.putTab(true)
		}
	case 'J': // ED - clear screen
		// TODO: sel.ob.x = -1
		switch c.arg(0, 0) {
		case 0: // below
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
			if t.cur.Y < t.rows-1 {
				t.clear(0, t.cur.Y+1, t.cols-1, t.rows-1)
			}
		case 1: // above
			if t.cur.Y > 1 {
				t.clear(0, 0, t.cols-1, t.cur.Y-1)
			}
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, 0, t.cols-1, t.rows-1)
		case 3: // saved lines (xterm E3) — scrollback only, screen untouched
			if t.sbClearCb != nil {
				t.sbClearCb()
			}
		default:
			goto unknown
		}
	case 'K': // EL - clear line
		switch c.arg(0, 0) {
		case 0: // right
			t.clear(t.cur.X, t.cur.Y, t.cols-1, t.cur.Y)
		case 1: // left
			t.clear(0, t.cur.Y, t.cur.X, t.cur.Y)
		case 2: // all
			t.clear(0, t.cur.Y, t.cols-1, t.cur.Y)
		}
	case 'S': // SU - scroll <n> lines up
		t.scrollUp(t.top, c.arg(0, 1))
	case 'T': // SD - scroll <n> lines down
		t.scrollDown(t.top, c.arg(0, 1))
	case 'L': // IL - insert <n> blank lines
		t.insertBlankLines(c.arg(0, 1))
	case 'l': // RM - reset mode
		t.setMode(c.priv, false, c.args)
	case 'M': // DL - delete <n> lines
		t.deleteLines(c.arg(0, 1))
	case 'X': // ECH - erase <n> chars
		t.clear(t.cur.X, t.cur.Y, t.cur.X+c.arg(0, 1)-1, t.cur.Y)
	case 'P': // DCH - delete <n> chars
		t.deleteChars(c.arg(0, 1))
	case 'Z': // CBT - cursor backward tabulation <n> tab stops
		n := c.arg(0, 1)
		for i := 0; i < n; i++ {
			t.putTab(false)
		}
	case 'd': // VPA - move to <row>
		t.moveAbsTo(t.cur.X, c.arg(0, 1)-1)
	case 'h': // SM - set terminal mode
		t.setMode(c.priv, true, c.args)
	case 'm': // SGR - terminal attribute (color)
		// Ignore private-parameter SGR (e.g. \x1b[?4m from vim).
		// Private params mean the sequence is a DEC mode control, not an
		// attribute — passing them to setAttr would misfire (e.g. ?4 → underline).
		if !c.priv {
			t.setAttr(c.args)
		}
	case 'n':
		switch c.arg(0, 0) {
		case 5: // DSR - device status report
			t.reply([]byte("\033[0n"))
		case 6: // CPR - cursor position report
			t.reply(fmt.Appendf(nil, "\033[%d;%dR", t.cur.Y+1, t.cur.X+1))
		}
	case 'r': // DECSTBM - set scrolling region
		if c.priv {
			goto unknown
		} else {
			t.setScroll(c.arg(0, 1)-1, c.arg(1, t.rows)-1)
			t.moveAbsTo(0, 0)
		}
	case 's': // DECSC - save cursor position (ANSI.SYS)
		t.saveCursor()
	case 'u': // DECRC - restore cursor position (ANSI.SYS)
		t.restoreCursor()
	case 'q': // DECSCUSR - set cursor shape: ESC [ Ps SP q
		// The intermediate byte SP (0x20) precedes the final 'q'. Check for it
		// in c.buf to distinguish DECSCUSR from any plain CSI ending in 'q'.
		if len(c.buf) >= 2 && c.buf[len(c.buf)-2] == ' ' {
			// c.buf = [Ps ' ' 'q'] — parse Ps from the bytes before the space.
			ps := 0
			for _, b := range c.buf[:len(c.buf)-2] {
				if b >= '0' && b <= '9' {
					ps = ps*10 + int(b-'0')
				}
			}
			t.cur.Shape = ps
		}
	}
	return
unknown: // TODO: get rid of this goto
	t.logf("unknown CSI sequence '%c'\n", c.mode)
	// TODO: c.dump()
}
