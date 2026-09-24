package vt10x

import (
	"math/rand/v2"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// parseReference is the original string-splitting CSI parameter parser,
// kept as the specification for the allocation-free parse.
func (c *csiEscape) parseReference() {
	c.mode = c.buf[len(c.buf)-1]
	if len(c.buf) == 1 {
		return
	}
	s := string(c.buf)
	c.args = c.args[:0]
	if s[0] == '?' {
		c.priv = true
		s = s[1:]
	}
	s = s[:len(s)-1]
	ss := strings.Split(s, ";")
	for _, p := range ss {
		// Handle sub-parameters separated by ':' (e.g. "4:3" for curly underline,
		// "38:2:R:G:B" for RGB colour).  Encode as main*10000+sub so setAttr can
		// distinguish them from plain parameters without a separate data structure.
		// Only the FIRST sub-parameter is encoded; deeper sub-params (e.g. the
		// R:G:B in 38:2:R:G:B) are passed as separate ';'-equivalent args below.
		if idx := strings.IndexByte(p, ':'); idx >= 0 {
			main, err := strconv.Atoi(p[:idx])
			if err != nil {
				break
			}
			rest := p[idx+1:]
			// For multi-sub-param forms (e.g. "38:2:255:0:128"), flatten all
			// sub-params into individual args so the existing colour-parsing
			// logic in setAttr can handle them identically to the ';' form.
			subParts := strings.Split(rest, ":")
			sub, err := strconv.Atoi(subParts[0])
			if err != nil {
				sub = 0
			}
			c.args = append(c.args, main*10000+sub+1) // +1: distinguishes 4:0 (→40001) from plain 4 (→4)
			for _, sp := range subParts[1:] {
				v, err := strconv.Atoi(sp)
				if err != nil {
					break
				}
				c.args = append(c.args, v)
			}
		} else {
			i, err := strconv.Atoi(p)
			if err != nil {
				break
			}
			c.args = append(c.args, i)
		}
	}
}

func TestCSIParseMatchesReference(t *testing.T) {
	cases := []string{
		"m", "0m", "1;31m", ";5H", "5;H", "?1049h", "?25l", "38;2;255;0;128m", "38:2:255:0:128m",
		"4:3m", "4:m", ":3m", "58:5:196m", "1;;2m", "+5m", "-3m", "99999999999999999999m",
		"9223372036854775807m", "9223372036854775808m", "-9223372036854775808m", "1:2:x:3m", "a;1m",
		"48;5;;3m", "::m", ";m", "?m", "1;2;3;4;5;6;7;8;9;10;11;12m",
	}
	rng := rand.New(rand.NewPCG(1, 2))
	alphabet := "0123456789;:?+-x"
	for range 20000 {
		n := rng.IntN(14)
		var b strings.Builder
		for range n {
			b.WriteByte(alphabet[rng.IntN(len(alphabet))])
		}
		b.WriteByte("mHJKhl"[rng.IntN(6)])
		cases = append(cases, b.String())
	}
	for _, in := range cases {
		var got, want csiEscape
		got.buf, want.buf = []byte(in), []byte(in)
		got.parse()
		want.parseReference()
		if got.mode != want.mode || got.priv != want.priv || !slices.Equal(got.args, want.args) {
			t.Fatalf("%q: got %+v, want %+v", in, got, want)
		}
	}
}

func TestCSIParseDoesNotAllocate(t *testing.T) {
	c := csiEscape{buf: []byte("38;2;120;200;40m"), args: make([]int, 0, 16)}
	if n := testing.AllocsPerRun(100, func() { c.args = c.args[:0]; c.parse() }); n != 0 {
		t.Fatalf("CSI parse allocates %v times", n)
	}
}
