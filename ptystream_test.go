package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestPTYStreamBoundedRecovery(t *testing.T) {
	for _, tc := range []struct{ name, start, end string }{
		{"OSC BEL", "\x1b]0;", "\x07"},
		{"OSC ST", "\x1b]0;", "\x1b\\"},
		{"DCS", "\x1bP", "\x1b\\"},
		{"APC", "\x1b_", "\x1b\\"},
		{"CSI", "\x1b[", "m"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, step := range []int{1, 4096, 2 * oscMaxBuf} {
				var stream ptyStream
				input := []byte("before" + tc.start + strings.Repeat("1", oscMaxBuf+1) + tc.end + "after界")
				var got []byte
				for start := 0; start < len(input); start += step {
					got = append(got, stream.scan(input[start:min(start+step, len(input))])...)
					if len(stream.pending) > oscMaxBuf || len(stream.utf8) > 3 {
						t.Fatal("unbounded pending input")
					}
				}
				if string(got) != "beforeafter界" {
					t.Fatalf("chunk size %d: got %q", step, got)
				}
			}
		})
	}
}

func TestPTYStreamEveryBoundary(t *testing.T) {
	for _, input := range []string{"abc界😀", "A\x1b]8;;https://example.org/界\x1b\\B", "\x1b[?1049h\x1b[?2004h", "\x1bP+q536d756c78\x1b\\", "\x1b(0q\x1b(B"} {
		t.Run(input, func(t *testing.T) {
			for split := 0; split <= len(input); split++ {
				var stream ptyStream
				got := append([]byte(nil), stream.scan([]byte(input[:split]))...)
				got = append(got, stream.scan([]byte(input[split:]))...)
				if !bytes.Equal(got, []byte(input)) {
					t.Fatalf("split %d: got %q", split, got)
				}
			}
		})
	}
}

func TestPTYStreamCancellation(t *testing.T) {
	for _, cancel := range []byte{0x18, 0x1a} {
		var stream ptyStream
		stream.scan([]byte("\x1b]0;unfinished"))
		got := stream.scan(append([]byte{cancel}, []byte("recovered")...))
		if string(got) != "recovered" {
			t.Fatalf("cancel %x: got %q", cancel, got)
		}
	}
}
