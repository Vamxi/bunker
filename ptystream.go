package main

import (
	"bytes"

	"bunker/internal/graphics"
)

type ptyParseState uint8

const (
	ptyText ptyParseState = iota
	ptyEscape
	ptyCSI
	ptyString
	ptyStringEscape
	ptyCharset
)

// ptyStream frames controls before query handling. Oversized controls are
// discarded through their terminator, without retaining or rescanning them.
type ptyStream struct {
	state    ptyParseState
	pending  []byte
	utf8     []byte
	discard  bool
	graphics bool
}

func (s *ptyStream) scan(chunk []byte) []byte {
	out := make([]byte, 0, len(s.utf8)+len(chunk))
	out = append(out, s.utf8...)
	s.utf8 = s.utf8[:0]
	for i := 0; i < len(chunk); i++ {
		if s.state == ptyText {
			// Text passes through as is: copy up to the next ESC at once.
			esc := bytes.IndexByte(chunk[i:], 0x1b)
			if esc < 0 {
				out = append(out, chunk[i:]...)
				break
			}
			out = append(out, chunk[i:i+esc]...)
			i += esc
			s.state = ptyEscape
			s.pending = append(s.pending[:0], 0x1b)
			continue
		}
		b := chunk[i]
		if b == 0x18 || b == 0x1a { // CAN/SUB cancel an unfinished control.
			if s.graphics {
				out = append(out, b)
			} // abort multipart uploads in vt10x too
			s.reset()
			continue
		}
		if !s.discard {
			limit := oscMaxBuf
			if s.graphics {
				limit = graphics.MaxSequenceBytes + 4 // introducer and ST
			}
			if len(s.pending) >= limit {
				s.pending = s.pending[:0]
				s.discard = true
			} else {
				s.pending = append(s.pending, b)
				// Only string controls carry graphics (IsCommand is false for
				// every other introducer); CSI, the bulk of escapes, skip it.
				if !s.graphics && s.state == ptyString && len(s.pending) >= 3 && len(s.pending) <= 82 {
					s.graphics = graphics.IsCommand(rune(s.pending[1]), s.pending[2:])
				}
			}
		}
		finished := false
		switch s.state {
		case ptyEscape:
			switch b {
			case '[':
				s.state = ptyCSI
			case ']', 'P', '_', '^', 'k':
				s.state = ptyString
			case '(', ')', '*', '+', '#':
				s.state = ptyCharset
			case 0x1b:
				s.pending = append(s.pending[:0], b)
			default:
				finished = true
			}
		case ptyCSI:
			if b == 0x1b {
				s.reset()
				s.state = ptyEscape
				s.pending = append(s.pending, b)
			} else {
				finished = b >= 0x40 && b <= 0x7e
			}
		case ptyString:
			if b == 0x1b {
				s.state = ptyStringEscape
			}
			finished = b == 0x07
		case ptyStringEscape:
			finished = b == '\\' || b == 0x07
			if b != 0x1b {
				s.state = ptyString
			}
		case ptyCharset:
			finished = true
		}
		if finished {
			if !s.discard {
				out = append(out, s.pending...)
			} else if s.graphics {
				out = append(out, 0x18)
			}
			s.reset()
		}
	}
	split := utf8Boundary(out)
	s.utf8 = append(s.utf8, out[split:]...)
	return out[:split]
}

func (s *ptyStream) reset() {
	s.state = ptyText
	s.pending = s.pending[:0]
	if cap(s.pending) > oscMaxBuf {
		s.pending = nil
	}
	s.discard = false
	s.graphics = false
}
