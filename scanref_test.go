package main

// Reference copies of the pre-scanners as they were before they learned to
// skip text with SIMD byte searches. The differential tests in
// scanfast_test.go feed both the same streams and require identical results.

import (
	"bytes"
	"unicode/utf8"

	"bunker/internal/graphics"
)

type refPTYStream ptyStream

func (s *refPTYStream) scan(chunk []byte) []byte {
	out := append([]byte(nil), s.utf8...)
	s.utf8 = s.utf8[:0]
	for _, b := range chunk {
		if s.state == ptyText {
			if b != 0x1b {
				out = append(out, b)
				continue
			}
			s.state = ptyEscape
			s.pending = append(s.pending[:0], b)
			continue
		}
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
				if !s.graphics && len(s.pending) >= 3 && len(s.pending) <= 82 {
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

func (s *refPTYStream) reset() {
	s.state = ptyText
	s.pending = s.pending[:0]
	if cap(s.pending) > oscMaxBuf {
		s.pending = nil
	}
	s.discard = false
	s.graphics = false
}

func (s *oscScanner) refScan(chunk []byte, emit func([]byte)) {
	for _, b := range chunk {
		switch s.state {

		case oscIdle:
			if b == 0x1b {
				s.state = oscSeenESC
			}

		case oscSeenESC:
			if b == ']' { // ESC ] = start of OSC
				s.buf = append(s.buf[:0], 0x1b, ']')
				s.overrun = false
				s.state = oscInContent
			} else {
				s.state = oscIdle
			}

		case oscInContent:
			if len(s.buf) < oscMaxBuf {
				s.buf = append(s.buf, b)
			} else {
				s.overrun = true // cap reached; mark and stop accumulating
			}
			switch b {
			case 0x07: // BEL terminator
				s.dispatch(emit)
				s.state = oscIdle
			case 0x1b:
				s.state = oscContentESC
			}

		case oscContentESC:
			if len(s.buf) < oscMaxBuf {
				s.buf = append(s.buf, b)
			}
			if b == '\\' { // ST = ESC \ terminator
				s.dispatch(emit)
				s.state = oscIdle
			} else {
				s.state = oscInContent // spurious ESC inside OSC, keep going
			}
		}
	}
}

func refNextTerminalQuery(data []byte, from int) (terminalQuery, bool) {
	for i := from; i+1 < len(data); i++ {
		if data[i] != 0x1b {
			continue
		}
		switch data[i+1] {
		case '[':
			if q, ok := refParseCSIQuery(data, i); ok {
				return q, true
			}
		case ']':
			end := oscSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if q, ok := parseOSCQuery(data, i, end); ok {
				return q, true
			}
			i = end - 1
		case 'P':
			end := dcsSequenceEnd(data, i)
			if end <= i {
				continue
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP+q")) {
				return terminalQuery{
					start:   i,
					end:     end,
					kind:    terminalQueryXTGETTCAP,
					payload: string(data[i+4 : end-2]),
				}, true
			}
			if bytes.HasPrefix(data[i:end], []byte("\x1bP$q")) {
				return terminalQuery{start: i, end: end, kind: terminalQueryDECRQSS, payload: string(data[i+4 : end-2])}, true
			}
			i = end - 1
		case '_', '^', 'X':
			if end := dcsSequenceEnd(data, i); end > i {
				i = end - 1
			}
		}
	}
	return terminalQuery{}, false
}

func refParseCSIQuery(data []byte, start int) (terminalQuery, bool) {
	rest := data[start:]
	switch {
	case bytes.HasPrefix(rest, []byte("\x1b[14t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 14}, true
	case bytes.HasPrefix(rest, []byte("\x1b[16t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 16}, true
	case bytes.HasPrefix(rest, []byte("\x1b[18t")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQuerySize, mode: 18}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=c")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[=0c")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryDA3}, true
	case bytes.HasPrefix(rest, []byte("\x1b[5n")):
		return terminalQuery{start: start, end: start + 4, kind: terminalQueryDSR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0q")):
		return terminalQuery{start: start, end: start + len("\x1b[>0q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>q")):
		return terminalQuery{start: start, end: start + len("\x1b[>q"), kind: terminalQueryXTVERSION}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>0c")):
		return terminalQuery{start: start, end: start + len("\x1b[>0c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[>c")):
		return terminalQuery{start: start, end: start + len("\x1b[>c"), kind: terminalQueryDA2}, true
	case bytes.HasPrefix(rest, []byte("\x1b[0c")):
		return terminalQuery{start: start, end: start + len("\x1b[0c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[c")):
		return terminalQuery{start: start, end: start + len("\x1b[c"), kind: terminalQueryDA}, true
	case bytes.HasPrefix(rest, []byte("\x1b[6n")):
		return terminalQuery{start: start, end: start + len("\x1b[6n"), kind: terminalQueryCPR}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?6n")):
		return terminalQuery{start: start, end: start + 5, kind: terminalQueryCPR, mode: 1}, true
	case bytes.HasPrefix(rest, []byte("\x1b[?")):
		mode, end, ok := parseDECRQM(data, start)
		if ok {
			return terminalQuery{start: start, end: end, kind: terminalQueryDECRQM, mode: mode}, true
		}
	}
	return terminalQuery{}, false
}

func refControlBoundary(data []byte, target int) int {
	for i := 0; i < len(data); {
		if i >= target {
			return i
		}
		if data[i] != 0x1b {
			_, size := utf8.DecodeRune(data[i:])
			i += size
			continue
		}
		if i+1 == len(data) {
			return i
		}
		switch data[i+1] {
		case 'P', '_', ']', '^', 'k':
			end := oscSequenceEnd(data, i)
			if end < 0 {
				return len(data)
			}
			i = end
		case '[':
			i += 2
			for i < len(data) {
				c := data[i]
				i++
				if c >= 0x40 && c <= 0x7e {
					break
				}
			}
		case '(', ')', '*', '+', '#':
			i = min(len(data), i+3)
		default:
			i += 2
		}
	}
	return len(data)
}

func refIsTransientLineClear(chunk []byte) bool {
	if len(chunk) < 3 || bytes.ContainsAny(chunk, "\n\x1b") {
		return false
	}

	sawClear := false
	for i := 0; i < len(chunk); {
		if chunk[i] != '\r' {
			return false
		}
		i++

		spaces := 0
		for i < len(chunk) && chunk[i] == ' ' {
			i++
			spaces++
		}
		if spaces == 0 || i >= len(chunk) || chunk[i] != '\r' {
			return false
		}
		i++
		sawClear = true
	}
	return sawClear
}

func refIsInPlaceLineUpdate(chunk []byte) bool {
	return len(chunk) > 0 &&
		bytes.Contains(chunk, []byte("\r")) &&
		!bytes.ContainsAny(chunk, "\n\x1b")
}
