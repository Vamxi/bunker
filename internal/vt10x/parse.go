package vt10x

func isControlCode(c rune) bool {
	return c < 0x20 || c == 0177
}

func (t *State) parse(c rune) {
	t.logRune(c)
	if isControlCode(c) {
		if t.handleControlCodes(c) || t.cur.Attr.Mode&attrGfx == 0 {
			return
		}
	}
	// TODO: update selection; see st.c:2450
	if t.extendGrapheme(c) {
		return
	}

	width := runeCellWidth(c)
	if width > t.cols {
		c, width = '\uFFFD', 1
	}
	if t.mode&ModeWrap != 0 && (t.cur.State&cursorWrapNext != 0 || t.cur.X+width > t.cols) {
		if t.cur.State&cursorWrapNext == 0 {
			for x := t.cur.X; x < t.cols; x++ {
				t.eraseWideAt(x, t.cur.Y)
				t.eraseCell(x, t.cur.Y)
				t.lines[t.cur.Y][x].Width = -2
			}
			t.markDirty(t.cur.Y)
		}
		t.lines[t.cur.Y][t.cols-1].Mode |= attrWrap
		t.newline(true)
	}
	if t.cur.X+width > t.cols {
		c, width = '\uFFFD', 1
	}

	if t.mode&ModeInsert != 0 && t.cur.X+1 < t.cols {
		// TODO: move shiz, look at st.c:2458
		t.logln("insert mode not implemented")
	}

	t.setChar(c, &t.cur.Attr, t.cur.X, t.cur.Y)
	t.clusterX, t.clusterY = t.cur.X, t.cur.Y
	if t.cur.X+width < t.cols {
		t.moveTo(t.cur.X+width, t.cur.Y)
	} else {
		t.cur.X = t.cols - 1
		t.cur.State |= cursorWrapNext
	}
	t.clusterOpen = true
	t.asciiReady = c >= 0x20 && c < 0x7f
}

// printASCIIRun prints a run of printable ASCII bytes directly, skipping the
// per-rune decode, state dispatch, logging, and grapheme checks that parse
// performs. It is only valid right after parse printed a plain ASCII
// character (asciiReady): the parser is in ground state and the open
// cluster is ASCII, so no byte in the run can extend it. It stops at the
// first byte needing the general path (controls, non-ASCII, a pending wrap)
// and returns how many bytes it consumed.
func (t *State) printASCIIRun(b []byte) int {
	if t.cur.Attr.Mode&attrGfx != 0 || t.mode&ModeInsert != 0 {
		return 0
	}
	n := 0
	for ; n < len(b); n++ {
		c := b[n]
		if c < 0x20 || c > 0x7e || t.cur.State&cursorWrapNext != 0 {
			break
		}
		x, y := t.cur.X, t.cur.Y
		t.setChar(rune(c), &t.cur.Attr, x, y)
		t.clusterX, t.clusterY = x, y
		if x+1 < t.cols {
			t.moveTo(x+1, y)
		} else {
			t.cur.X = t.cols - 1
			t.cur.State |= cursorWrapNext
		}
	}
	return n
}

func (t *State) parseEsc(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	next := t.parse
	t.logRune(c)
	switch c {
	case '[':
		next = t.parseEscCSI
	case '#':
		next = t.parseEscTest
	case 'P', // DCS - Device Control String
		'_', // APC - Application Program Command
		'^', // PM - Privacy Message
		']', // OSC - Operating System Command
		'k': // old title set compatibility
		t.str.reset()
		t.str.typ = c
		next = t.parseEscStr
	case '(': // set primary charset G0
		next = t.parseEscAltCharset
	case ')', // set secondary charset G1 (ignored)
		'*', // set tertiary charset G2 (ignored)
		'+': // set quaternary charset G3 (ignored)
	case 'D': // IND - linefeed
		if t.cur.Y == t.bottom {
			t.scrollUp(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y+1)
		}
	case 'E': // NEL - next line
		t.newline(true)
	case 'H': // HTS - horizontal tab stop
		t.tabs[t.cur.X] = true
	case 'M': // RI - reverse index
		if t.cur.Y == t.top {
			t.scrollDown(t.top, 1)
		} else {
			t.moveTo(t.cur.X, t.cur.Y-1)
		}
	case 'Z': // DECID - identify terminal
		// TODO: write to our writer our id
	case 'c': // RIS - reset to initial state
		t.reset()
		if t.sbClearCb != nil {
			t.sbClearCb()
		}
	case '=': // DECPAM - application keypad
		t.mode |= ModeAppKeypad
	case '>': // DECPNM - normal keypad
		t.mode &^= ModeAppKeypad
	case '7': // DECSC - save cursor
		t.saveCursor()
	case '8': // DECRC - restore cursor
		t.restoreCursor()
	case '\\': // ST - stop
	default:
		t.logf("unknown ESC sequence '%c'\n", c)
	}
	t.state = next
}

func (t *State) parseEscCSI(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	if t.csi.put(byte(c)) {
		t.state = t.parse
		t.handleCSI()
	}
}

func (t *State) parseEscStr(c rune) {
	t.logRune(c)
	switch c {
	case 0x18, 0x1a:
		t.state = t.parse
		t.str.reset()
		if t.graphics != nil {
			t.graphics.Abort()
		}
	case '\033':
		t.state = t.parseEscStrEnd
	case '\a': // backwards compatiblity to xterm
		t.state = t.parse
		t.handleSTR()
	default:
		t.str.put(c)
	}
}

func (t *State) parseEscStrEnd(c rune) {
	if t.str.graphics {
		// Graphics payloads are opaque until ST/BEL. In particular, an
		// embedded ESC [ must not turn corrupt image data into pane text.
		switch c {
		case '\\', '\a':
			t.state = t.parse
			t.handleSTR()
		case 0x18, 0x1a:
			t.handleControlCodes(c)
		case '\x1b':
			t.str.put('\x1b')
		default:
			t.str.put('\x1b')
			t.str.put(c)
			t.state = t.parseEscStr
		}
		return
	}
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	t.state = t.parse
	if c == '\\' {
		t.handleSTR()
	}
}

func (t *State) parseEscAltCharset(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	t.logRune(c)
	switch c {
	case '0': // line drawing set
		t.cur.Attr.Mode |= attrGfx
	case 'B': // USASCII
		t.cur.Attr.Mode &^= attrGfx
	case 'A', // UK (ignored)
		'<', // multinational (ignored)
		'5', // Finnish (ignored)
		'C', // Finnish (ignored)
		'K': // German (ignored)
	default:
		t.logf("unknown alt. charset '%c'\n", c)
	}
	t.state = t.parse
}

func (t *State) parseEscTest(c rune) {
	if t.handleControlCodes(c) {
		return
	}
	// DEC screen alignment test
	if c == '8' {
		for y := 0; y < t.rows; y++ {
			for x := 0; x < t.cols; x++ {
				t.setChar('E', &t.cur.Attr, x, y)
			}
		}
	}
	t.state = t.parse
}

func (t *State) handleControlCodes(c rune) bool {
	if !isControlCode(c) {
		return false
	}
	t.clusterOpen = false
	switch c {
	// HT
	case '\t':
		t.putTab(true)
	// BS
	case '\b':
		t.moveTo(t.cur.X-1, t.cur.Y)
	// CR
	case '\r':
		t.moveTo(0, t.cur.Y)
	// LF, VT, LF
	case '\f', '\v', '\n':
		// go to first col if mode is set
		t.newline(t.mode&ModeCRLF != 0)
	// BEL
	case '\a':
		// TODO: emit sound
		// TODO: window alert if not focused
	// ESC
	case 033:
		t.csi.reset()
		t.state = t.parseEsc
	// SO, SI
	case 016, 017:
		// different charsets not supported. apps should use the correct
		// alt charset escapes, probably for line drawing
	// SUB, CAN
	case 032, 030:
		t.csi.reset()
		t.str.reset()
		t.state = t.parse
		if t.graphics != nil {
			t.graphics.Abort()
		}
	// ignore ENQ, NUL, XON, XOFF, DEL
	case 005, 000, 021, 023, 0177:
	default:
		return false
	}
	return true
}
