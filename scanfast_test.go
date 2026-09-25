package main

import (
	"bytes"
	"fmt"
	"math/rand/v2"
	"reflect"
	"strings"
	"testing"
)

// scanStream builds an escape-heavy PTY stream: text of every kind, every
// control family the scanners know, queries, truncated and oversized
// sequences, and invalid UTF-8.
func scanStream(r *rand.Rand) []byte {
	var b bytes.Buffer
	for b.Len() < 1+r.IntN(3000) {
		switch r.IntN(24) {
		case 0, 1, 2:
			b.WriteString(strings.Repeat("word ", 1+r.IntN(8)))
		case 3:
			b.WriteString([]string{"界", "😀", "é", "👩\u200d💻", "\u0301"}[r.IntN(5)])
		case 4:
			b.Write([]byte{[]byte{0x80, 0xbf, 0xc3, 0xe2, 0xf0, 0xff}[r.IntN(6)]})
		case 5:
			b.WriteString([]string{"\r\n", "\n", "\r", "\r   \r", "\t", "\b"}[r.IntN(6)])
		case 6, 7, 8:
			fmt.Fprintf(&b, "\x1b[%d;5;%dm", 38+r.IntN(2)*10, r.IntN(256))
		case 9:
			b.WriteString([]string{"\x1b[14t", "\x1b[16t", "\x1b[18t", "\x1b[=c", "\x1b[=0c", "\x1b[5n", "\x1b[>0q", "\x1b[>q", "\x1b[>0c", "\x1b[>c", "\x1b[0c", "\x1b[c", "\x1b[6n", "\x1b[?6n", "\x1b[?2004$p", "\x1b[?25$p", "\x1b[?$p", "\x1b[1t", "\x1b[?1;2c"}[r.IntN(19)])
		case 10:
			b.WriteString([]string{"\x1b[?1049h", "\x1b[?1047h", "\x1b[?47h", "\x1b[?1049l", "\x1b[?1047l", "\x1b[?47l", "\x1b[?2004h", "\x1b[?u", "\x1b[>1u", "\x1b[<u"}[r.IntN(10)])
		case 11:
			fmt.Fprintf(&b, "\x1b]%d;%s%s", []int{0, 7, 8, 10, 11, 52, 133, 1337}[r.IntN(8)], []string{"title", "file:///tmp", ";https://x.io", "?", "c;aGk=", "A", "File=inline=1:AAAA", "FileEnd"}[r.IntN(8)], []string{"\x07", "\x1b\\", ""}[r.IntN(3)])
		case 12:
			b.WriteString([]string{"\x1bP+q544e\x1b\\", "\x1bP$qm\x1b\\", "\x1bP0;1q#0~-\x1b\\", "\x1bPzz\x1b\\", "\x1bP"}[r.IntN(5)])
		case 13:
			b.WriteString([]string{"\x1b_Ga=T,f=24,s=1,v=1;/wAA\x1b\\", "\x1b_other\x1b\\", "\x1b^pm\x1b\\", "\x1bXsos\x1b\\", "\x1bktitle\x1b\\"}[r.IntN(5)])
		case 14:
			b.WriteString([]string{"\x1b(0", "\x1b(B", "\x1b#8", "\x1b7", "\x1b8", "\x1bc", "\x1b\x1b[m", "\x1b"}[r.IntN(8)])
		case 15:
			b.WriteByte([]byte{0x18, 0x1a, 0x07, 0x00}[r.IntN(4)])
		case 16:
			if r.IntN(20) == 0 { // oversized string
				b.WriteString("\x1b]0;" + strings.Repeat("x", oscMaxBuf+r.IntN(50)) + "\x07")
			} else {
				b.WriteString("\x1b[" + strings.Repeat("1;", r.IntN(4)))
			}
		case 17:
			b.WriteString("\x1b]1337;File=" + strings.Repeat("A", r.IntN(200)) + "\x07")
		default:
			fmt.Fprintf(&b, "\x1b[%d%c", r.IntN(100), "ABCDHJKmnrsu"[r.IntN(12)])
		}
	}
	return b.Bytes()
}

// splits cuts data into chunks at random points, the same for both sides.
func splits(r *rand.Rand, data []byte) [][]byte {
	var out [][]byte
	for len(data) > 0 {
		n := min(len(data), 1+r.IntN(64)*r.IntN(64))
		out = append(out, data[:n])
		data = data[n:]
	}
	return out
}

func TestFastScannersMatchReference(t *testing.T) {
	seeds := uint64(2000)
	if raceBuild {
		seeds = 100
	}
	for seed := range seeds {
		r := rand.New(rand.NewPCG(seed, 7))
		data := scanStream(r)
		chunks := splits(r, data)

		var fast ptyStream
		var ref refPTYStream
		var fastOSC, refOSC oscScanner
		var fastEmit, refEmit []string
		for i, chunk := range chunks {
			got, want := fast.scan(chunk), ref.scan(chunk)
			if !bytes.Equal(got, want) || !reflect.DeepEqual(fast, ptyStream(ref)) {
				t.Fatalf("seed %d chunk %d: ptyStream.scan\n got %q state %+v\nwant %q state %+v", seed, i, got, fast, want, ptyStream(ref))
			}
			fastOSC.Scan(chunk, func(seq []byte) { fastEmit = append(fastEmit, string(seq)) })
			refOSC.refScan(chunk, func(seq []byte) { refEmit = append(refEmit, string(seq)) })
			if !reflect.DeepEqual(fastEmit, refEmit) || fastOSC.state != refOSC.state || fastOSC.overrun != refOSC.overrun || !bytes.Equal(fastOSC.buf, refOSC.buf) {
				t.Fatalf("seed %d chunk %d: oscScanner.Scan emitted %q, reference %q", seed, i, fastEmit, refEmit)
			}
			if a, b := isTransientLineClear(chunk), refIsTransientLineClear(chunk); a != b {
				t.Fatalf("seed %d chunk %d: isTransientLineClear %v, reference %v", seed, i, a, b)
			}
			if a, b := isInPlaceLineUpdate(chunk), refIsInPlaceLineUpdate(chunk); a != b {
				t.Fatalf("seed %d chunk %d: isInPlaceLineUpdate %v, reference %v", seed, i, a, b)
			}
			for _, seqs := range [][]string{altScreenEnter, altScreenExit} {
				wantAt, wantSeq := -1, ""
				for _, seq := range seqs {
					if at := bytes.Index(chunk, []byte(seq)); at >= 0 {
						wantAt, wantSeq = at, seq
						break
					}
				}
				if at, seq := indexFirstOf(chunk, seqs); at != wantAt || seq != wantSeq {
					t.Fatalf("seed %d chunk %d: indexFirstOf = %d %q, want %d %q", seed, i, at, seq, wantAt, wantSeq)
				}
			}
		}

		for from := 0; from <= len(data); from += 1 + r.IntN(40) {
			q, ok := nextTerminalQuery(data, from)
			rq, rok := refNextTerminalQuery(data, from)
			if ok != rok || q != rq {
				t.Fatalf("seed %d from %d: nextTerminalQuery = %+v %v, reference %+v %v", seed, from, q, ok, rq, rok)
			}
		}
		for range 20 {
			target := r.IntN(len(data) + 1)
			if a, b := controlBoundary(data, target), refControlBoundary(data, target); a != b {
				t.Fatalf("seed %d target %d: controlBoundary = %d, reference %d", seed, target, a, b)
			}
		}
	}
}
