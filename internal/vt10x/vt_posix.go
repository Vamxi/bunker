//go:build linux || darwin || dragonfly || solaris || openbsd || netbsd || freebsd
// +build linux darwin dragonfly solaris openbsd netbsd freebsd

package vt10x

import (
	"unicode/utf8"
)

type terminal struct {
	*State
}

func newTerminal(info TerminalInfo) *terminal {
	t := &terminal{newState(info.w)}
	t.scrollSwapCb = info.scrollSwapCb
	t.sbClearCb = info.sbClearCb
	t.init(info.cols, info.rows)
	t.graphics, t.graphicsReply = info.graphics, info.graphicsReply
	t.cellWidth, t.cellHeight = info.cellWidth, info.cellHeight
	t.graphicsRows = info.graphicsRows
	return t
}

func (t *terminal) init(cols, rows int) {
	t.numlock = true
	t.state = t.st().parse
	t.cur.Attr.FG = DefaultFG
	t.cur.Attr.BG = DefaultBG
	t.Resize(cols, rows)
	t.reset()
}

// Write parses input and writes terminal changes to state.
func (t *terminal) Write(p []byte) (int, error) {
	t.lock()
	defer t.unlock()
	i := 0
	for i < len(p) {
		if t.asciiReady && !t.noASCIIFastPath {
			if n := t.printASCIIRun(p[i:]); n > 0 {
				i += n
				continue
			}
		}
		c, sz := rune(p[i]), 1
		if c >= utf8.RuneSelf {
			c, sz = utf8.DecodeRune(p[i:])
			if c == utf8.RuneError && sz == 1 {
				if i+1 == len(p) {
					// not enough bytes for a full rune
					return i, nil
				}
				t.logln("invalid utf8 sequence")
				i++
				continue
			}
		}
		i += sz
		t.asciiReady = false
		t.put(c)
	}
	return i, nil
}

func (t *terminal) Resize(cols, rows int) {
	t.lock()
	defer t.unlock()
	t.resize(cols, rows)
}
