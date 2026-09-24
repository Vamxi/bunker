// Package benchdata generates the terminal workloads bunker's benchmarks
// and allocation tests replay. Each models a kind of output users actually
// produce, so a regression shows up against the traffic that matters:
//
//	Seq      short lines that scroll constantly     (seq, find, logs)
//	Prose    long wrapped lines with a few colours  (compilers, grep)
//	Colors   a colour change on nearly every cell   (ls --color, bat, lolcat)
//	Unicode  CJK, emoji, combining marks            (i18n text, Claude Code)
//	TUI      full-screen cursor-addressed redraws   (btop, vim, htop)
//
// Output is deterministic, so runs are comparable.
package benchdata

import (
	"bytes"
	"strconv"
)

// Seq mimics `seq 1 n`.
func Seq(n int) []byte {
	var b bytes.Buffer
	for i := 1; i <= n; i++ {
		b.WriteString(strconv.Itoa(i))
		b.WriteString("\r\n")
	}
	return b.Bytes()
}

// Prose is n long lines with occasional SGR, wrapping on narrow terminals.
func Prose(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.WriteString("\x1b[32mINFO\x1b[0m the quick brown fox jumps over the lazy dog; ")
		b.WriteString("pack my box with five dozen liquor jugs 0123456789\r\n")
	}
	return b.Bytes()
}

// Colors is n lines where every character changes truecolor foreground.
func Colors(n int) []byte {
	var b bytes.Buffer
	const text = "rainbow-coloured output, one SGR per character"
	for i := 0; i < n; i++ {
		for j, c := range []byte(text) {
			r, g := (i*7+j*11)%256, (i*13+j*5)%256
			b.WriteString("\x1b[38;2;")
			b.WriteString(strconv.Itoa(r))
			b.WriteByte(';')
			b.WriteString(strconv.Itoa(g))
			b.WriteString(";200m")
			b.WriteByte(c)
		}
		b.WriteString("\x1b[0m\r\n")
	}
	return b.Bytes()
}

// Unicode is n lines of wide, emoji, and combining text.
func Unicode(n int) []byte {
	var b bytes.Buffer
	for i := 0; i < n; i++ {
		b.WriteString("日本語のテキスト ✳ Claude Code 👍🏽 👩‍💻 🇳🇱 é ä́ ⣿⡇ ╭──╮\r\n")
	}
	return b.Bytes()
}

// TUI is n full-screen frames of a cols×rows dashboard: cursor positioning,
// colour runs, box drawing, and a braille graph, as btop redraws.
func TUI(n, cols, rows int) []byte {
	var b bytes.Buffer
	for f := 0; f < n; f++ {
		b.WriteString("\x1b[?2026h\x1b[H")
		for y := 1; y <= rows; y++ {
			b.WriteString("\x1b[")
			b.WriteString(strconv.Itoa(y))
			b.WriteString(";1H\x1b[38;5;")
			b.WriteString(strconv.Itoa(16 + (y+f)%200))
			b.WriteString("m│")
			for x := 1; x < cols-1; x++ {
				switch {
				case y == 1 || y == rows:
					b.WriteString("─")
				case x%10 == 0:
					b.WriteString("\x1b[1;33m")
					b.WriteString(strconv.Itoa((x + y + f) % 10))
					b.WriteString("\x1b[22;39m")
				default:
					b.WriteRune(rune(0x2800 + (x*y+f)%256))
				}
			}
			b.WriteString("│")
		}
		b.WriteString("\x1b[0m\x1b[?2026l")
	}
	return b.Bytes()
}
