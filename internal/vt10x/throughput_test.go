package vt10x

import (
	"bytes"
	"strconv"
	"testing"
	"unicode/utf8"
)

// seqOutput mimics `seq 1 n`: short ASCII lines that scroll constantly.
func seqOutput(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// proseOutput is long wrapped ASCII lines with some SGR colour changes.
func proseOutput(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.WriteString("\x1b[32mINFO\x1b[0m the quick brown fox jumps over the lazy dog; ")
		b.WriteString("pack my box with five dozen liquor jugs 0123456789\r\n")
	}
	return b.Bytes()
}

func benchWrite(b *testing.B, data []byte) {
	// Ring-buffer scrollback, like the pane's sbRing, so only the emulator
	// is measured.
	ring := make([][]Glyph, 1000)
	head := 0
	swap := func(row []Glyph) []Glyph {
		old := ring[head]
		ring[head] = row
		head = (head + 1) % len(ring)
		if len(old) != len(row) {
			return make([]Glyph, len(row))
		}
		return old
	}
	term := New(WithSize(120, 40), WithScrollSwapCallback(swap))
	b.SetBytes(int64(len(data)))
	b.ResetTimer()
	for b.Loop() {
		term.Write(data) //nolint:errcheck
	}
}

func BenchmarkWriteSeq(b *testing.B)   { benchWrite(b, seqOutput(200_000)) }
func BenchmarkWriteProse(b *testing.B) { benchWrite(b, proseOutput(20_000)) }

// TestScrollRegionRotation covers rotateLines through DECSTBM regions,
// multi-line SU/SD (including more rows than the small inline buffer), and
// checks rows outside the region never move.
func TestScrollRegionRotation(t *testing.T) {
	term := New(WithSize(3, 10))
	for i := 0; i < 10; i++ {
		term.Write([]byte("\x1b[" + strconv.Itoa(i+1) + ";1H" + string(rune('a'+i)))) //nolint:errcheck
	}
	rowChars := func() string {
		var b []byte
		for y := 0; y < 10; y++ {
			c := term.Cell(0, y).Char
			if c == ' ' {
				c = '.'
			}
			b = append(b, byte(c))
		}
		return string(b)
	}
	term.Write([]byte("\x1b[2;9r\x1b[6S")) //nolint:errcheck // region rows 2-9, scroll up 6
	if got, want := rowChars(), "ahi......j"; got != want {
		t.Fatalf("after SU 6: %q, want %q", got, want)
	}
	term.Write([]byte("\x1b[2T")) //nolint:errcheck // scroll down 2 inside region
	if got, want := rowChars(), "a..hi....j"; got != want {
		t.Fatalf("after SD 2: %q, want %q", got, want)
	}
}

// TestASCIIFastPathMatchesParser feeds random terminal traffic through the
// fast path and through parse alone, and requires identical grids.
func TestASCIIFastPathMatchesParser(t *testing.T) {
	pieces := []string{
		"a", "hello world ", "0123456789", "~!@#$%^&*()", " ", "\r\n", "\n", "\r", "\t", "\b",
		"\x1b[31m", "\x1b[1;4;44m", "\x1b[0m", "\x1b[7m", "\x1b[2J", "\x1b[H", "\x1b[3;5H", "\x1b[K",
		"\x1b[?7l", "\x1b[?7h", "\x1b(0", "\x1b(B", "\x1b[4h", "\x1b[4l", "\x1b[2;4r", "\x1b[r",
		"é", "é", "日本", "😀", "👍🏽", "‍", "️", "1️⃣", "؀", "\xff",
	}
	rng := uint64(1)
	next := func(n int) int {
		rng ^= rng << 13
		rng ^= rng >> 7
		rng ^= rng << 17
		return int(rng % uint64(n))
	}
	for iter := 0; iter < 5000; iter++ {
		var b bytes.Buffer
		for i := 0; i < 80; i++ {
			b.WriteString(pieces[next(len(pieces))])
		}
		data := b.Bytes()
		cols, rows := 3+next(12), 2+next(5)
		fast := New(WithSize(cols, rows))
		slow := New(WithSize(cols, rows))
		slow.(*terminal).noASCIIFastPath = true
		// Split at random points so fast-path state carries across writes.
		// Split on rune boundaries, as ptyStream framing guarantees.
		for rest := data; len(rest) > 0; {
			n := 1 + next(len(rest))
			for n < len(rest) && !utf8.RuneStart(rest[n]) {
				n++
			}
			fast.Write(rest[:n]) //nolint:errcheck
			rest = rest[n:]
		}
		slow.Write(data) //nolint:errcheck
		for y := 0; y < rows; y++ {
			for x := 0; x < cols; x++ {
				if f, s := fast.Cell(x, y), slow.Cell(x, y); !sameGlyph(f, s) {
					t.Fatalf("iter %d %dx%d cell (%d,%d): fast %+v, slow %+v\ninput %q", iter, cols, rows, x, y, f, s, data)
				}
			}
		}
		if f, s := fast.Cursor(), slow.Cursor(); f.X != s.X || f.Y != s.Y || f.State != s.State {
			t.Fatalf("iter %d cursor: fast %+v, slow %+v\ninput %q", iter, f, s, data)
		}
	}
}

// sameGlyph compares cell content; ext is compared by value, not pointer.
func sameGlyph(a, b Glyph) bool {
	if a.Combining() != b.Combining() || a.Image() != b.Image() {
		return false
	}
	a.ext, b.ext = nil, nil
	return a == b
}
