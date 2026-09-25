package vt10x

import (
	"bunker/internal/graphics"
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"
)

// STR sequences are similar to CSI sequences, but have string arguments (and
// as far as I can tell, don't really have a name; STR is the name I took from
// suckless which I imagine comes from rxvt or xterm).
type strEscape struct {
	typ               rune
	buf               []byte
	args              []string
	graphics, discard bool
}

func (s *strEscape) reset() {
	s.typ = 0
	s.buf = s.buf[:0]
	s.args = nil
	s.graphics, s.discard = false, false
}

func (s *strEscape) put(c rune) {
	if s.discard {
		return
	}
	limit := 64 << 10
	if s.graphics {
		limit = graphics.MaxSequenceBytes
	}
	if len(s.buf)+utf8.RuneLen(c) > limit {
		s.buf = nil
		s.discard = true
		return
	}
	s.buf = utf8.AppendRune(s.buf, c)
	if !s.graphics && len(s.buf) <= 80 {
		s.graphics = graphics.IsCommand(s.typ, s.buf)
	}
}

func (s *strEscape) parse() {
	s.args = strings.Split(string(s.buf), ";")
}

func (s *strEscape) arg(i, def int) int {
	if i >= len(s.args) || i < 0 {
		return def
	}
	i, err := strconv.Atoi(s.args[i])
	if err != nil {
		return def
	}
	return i
}

func (s *strEscape) argString(i int, def string) string {
	if i >= len(s.args) || i < 0 {
		return def
	}
	return s.args[i]
}

func (t *State) handleSTR() {
	s := &t.str
	defer func() {
		if cap(s.buf) > 64<<10 {
			s.buf = nil
		}
	}()
	if s.discard {
		if t.graphics != nil {
			t.graphics.Abort()
		}
		return
	}
	if s.graphics {
		t.handleGraphics(s.typ, s.buf)
		return
	}
	s.parse()

	switch s.typ {
	case ']': // OSC - operating system command
		var p *string
		switch d := s.arg(0, 0); d {
		case 0, 1, 2:
			title := s.argString(1, "")
			if title != "" {
				t.setTitle(title)
			}
		case 10, 11, 12:
			if len(s.args) < 2 {
				break
			}

			c := s.argString(1, "")
			p := &c
			var (
				colorNum  int
				colorName string
			)
			switch d {
			case 10:
				colorNum = int(DefaultFG)
				colorName = "foreground"
			case 11:
				colorNum = int(DefaultBG)
				colorName = "background"
			default:
				colorNum = int(DefaultCursor)
				colorName = "cursor"
			}

			if *p == "?" {
				t.oscColorResponse(colorNum, d)
			} else if err := t.setColorName(colorNum, p); err != nil {
				t.logf("invalid %s color: %s\n", colorName, maybe(p))
			}
			// TODO: redraw on successful colour set
		case 110, 111, 112: // reset dynamic fg/bg/cursor color to default
			var colorNum int
			switch d {
			case 110:
				colorNum = int(DefaultFG)
			case 111:
				colorNum = int(DefaultBG)
			default:
				colorNum = int(DefaultCursor)
			}
			if err := t.setColorName(colorNum, nil); err != nil {
				t.logf("failed to reset OSC color %d\n", d)
			}
		case 8: // hyperlink: OSC 8 ; <params> ; <URL> ST
			// params is an optional id=val list (currently ignored — tcell
			// doesn't surface IDs separately enough to matter for grouping).
			// URL empty = close the active hyperlink. Anything else opens
			// a new one. URLs may legitimately contain ';' so we re-join
			// the trailing args with ';' to undo s.parse()'s split.
			var url string
			if len(s.args) >= 3 {
				url = strings.Join(s.args[2:], ";")
			}
			t.cur.Attr.Link = t.internLink(url)
		case 4: // color set
			if len(s.args) < 3 {
				break
			}

			c := s.argString(2, "")
			p = &c
			fallthrough
		case 104: // color reset
			j := -1
			if len(s.args) > 1 {
				j = s.arg(1, 0)
			}
			if p != nil && *p == "?" { // report
				t.osc4ColorResponse(j)
			} else if err := t.setColorName(j, p); err != nil {
				if d != 104 || len(s.args) > 1 {
					t.logf("invalid color j=%d, p=%s\n", j, maybe(p))
				}
			}
			// TODO: redraw on successful colour set
		default:
			t.logf("unknown OSC command %d\n", d)
			// TODO: s.dump()
		}
	case 'k': // old title set compatibility
		title := s.argString(0, "")
		if title != "" {
			t.setTitle(title)
		}
	default:
		// TODO: Ignore these codes instead of complain?
		// 'P': // DSC - device control string
		// '_': // APC - application program command
		// '^': // PM - privacy message

		t.logf("unhandled STR sequence '%c'\n", s.typ)
		// t.str.dump()
	}
}

func (t *State) setColorName(j int, p *string) error {
	if !between(j, 0, 1<<24) && j != int(DefaultFG) && j != int(DefaultBG) && j != int(DefaultCursor) {
		return fmt.Errorf("invalid color value %d", j)
	}

	if p == nil {
		// restore color
		delete(t.colorOverride, Color(j))
	} else {
		// set color
		r, g, b, err := parseColor(*p)
		if err != nil {
			return err
		}
		t.colorOverride[Color(j)] = Color(r<<16 | g<<8 | b)
	}
	// A dynamic-colour change alters how every DefaultBG/FG cell renders, so
	// bump the generation counter that the renderer watches to force a full
	// repaint (see State.colorGen).
	t.colorGen++

	return nil
}

func (t *State) oscColorResponse(j, num int) {
	if j < 0 {
		t.logf("failed to fetch osc color %d\n", j)
		return
	}

	k, ok := t.colorOverride[Color(j)]
	if ok {
		j = int(k)
	}

	r, g, b := rgb(j)
	t.reply(fmt.Appendf(nil, "\033]%d;rgb:%02x%02x/%02x%02x/%02x%02x\007", num, r, r, g, g, b, b))
}

func (t *State) osc4ColorResponse(j int) {
	if j < 0 {
		t.logf("failed to fetch osc4 color %d\n", j)
		return
	}

	k, ok := t.colorOverride[Color(j)]
	if ok {
		j = int(k)
	}

	r, g, b := rgb(j)
	t.reply(fmt.Appendf(nil, "\033]4;%d;rgb:%02x%02x/%02x%02x/%02x%02x\007", j, r, r, g, g, b, b))
}

func rgb(j int) (r, g, b int) {
	return (j >> 16) & 0xff, (j >> 8) & 0xff, j & 0xff
}

var (
	RGBPattern  = regexp.MustCompile(`^([\da-f]{1})\/([\da-f]{1})\/([\da-f]{1})$|^([\da-f]{2})\/([\da-f]{2})\/([\da-f]{2})$|^([\da-f]{3})\/([\da-f]{3})\/([\da-f]{3})$|^([\da-f]{4})\/([\da-f]{4})\/([\da-f]{4})$`)
	HashPattern = regexp.MustCompile(`[\da-f]`)
)

func parseColor(p string) (r, g, b int, err error) {
	if len(p) == 0 {
		err = fmt.Errorf("empty color spec")
		return
	}

	low := strings.ToLower(p)
	if strings.HasPrefix(low, "rgb:") {
		low = low[4:]
		sm := RGBPattern.FindAllStringSubmatch(low, -1)
		if len(sm) != 1 || len(sm[0]) == 0 {
			err = fmt.Errorf("invalid rgb color spec: %s", p)
			return
		}
		m := sm[0]

		var base float64
		if len(m[1]) > 0 {
			base = 15
		} else if len(m[4]) > 0 {
			base = 255
		} else if len(m[7]) > 0 {
			base = 4095
		} else {
			base = 65535
		}

		r64, err := strconv.ParseInt(firstNonEmpty(m[1], m[4], m[7], m[10]), 16, 0)
		if err != nil {
			return r, g, b, err
		}

		g64, err := strconv.ParseInt(firstNonEmpty(m[2], m[5], m[8], m[11]), 16, 0)
		if err != nil {
			return r, g, b, err
		}

		b64, err := strconv.ParseInt(firstNonEmpty(m[3], m[6], m[9], m[12]), 16, 0)
		if err != nil {
			return r, g, b, err
		}

		r = int(math.Round(float64(r64) / base * 255))
		g = int(math.Round(float64(g64) / base * 255))
		b = int(math.Round(float64(b64) / base * 255))
		return r, g, b, nil
	} else if strings.HasPrefix(low, "#") {
		low = low[1:]
		m := HashPattern.FindAllString(low, -1)
		if !oneOf(len(m), []int{3, 6, 9, 12}) {
			err = fmt.Errorf("invalid hash color spec: %s", p)
			return
		}

		adv := len(low) / 3
		for i := 0; i < 3; i++ {
			c, err := strconv.ParseInt(low[adv*i:adv*i+adv], 16, 0)
			if err != nil {
				return r, g, b, err
			}

			var v int64
			switch adv {
			case 1:
				v = c << 4
			case 2:
				v = c
			case 3:
				v = c >> 4
			default:
				v = c >> 8
			}

			switch i {
			case 0:
				r = int(v)
			case 1:
				g = int(v)
			case 2:
				b = int(v)
			}
		}
		return
	} else {
		err = fmt.Errorf("invalid color spec: %s", p)
		return
	}
}

func maybe(p *string) string {
	if p == nil {
		return "<nil>"
	}
	return *p
}

func firstNonEmpty(strs ...string) string {
	if len(strs) == 0 {
		return ""
	}
	for _, str := range strs {
		if len(str) > 0 {
			return str
		}
	}
	return strs[len(strs)-1]
}

func oneOf(v int, is []int) bool {
	for _, i := range is {
		if v == i {
			return true
		}
	}
	return false
}
