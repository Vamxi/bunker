package main

import (
	"bytes"
	"image/color"
	"math"
	"unicode/utf8"

	"bunk/internal/graphics"

	"github.com/creack/pty"
	"github.com/gdamore/tcell/v2"
)

func imageColor(pixel color.NRGBA, background tcell.Color) tcell.Color {
	r, g, b := background.RGB()
	if r < 0 {
		r, g, b = 0, 0, 0
	}
	a := int32(pixel.A)
	return tcell.NewRGBColor((int32(pixel.R)*a+r*(255-a)+127)/255, (int32(pixel.G)*a+g*(255-a)+127)/255, (int32(pixel.B)*a+b*(255-a)+127)/255)
}

func virtualCellPixels(aspect []float64) (int, int) {
	width, height := 8, 16
	if len(aspect) != 0 && !math.IsNaN(aspect[0]) && !math.IsInf(aspect[0], 0) && aspect[0] > 0 {
		height = int(math.Round(8 * min(aspect[0], 8)))
	}
	return width, max(2, height)
}

func paneWinsize(cols, rows, cellWidth, cellHeight int) *pty.Winsize {
	return &pty.Winsize{Cols: uint16(cols), Rows: uint16(rows), X: uint16(min(65535, cols*cellWidth)), Y: uint16(min(65535, rows*cellHeight))}
}

// Graphics controls can dwarf the normal text history budget. Retain a bounded
// replay window, and never start replay inside a control or a UTF-8 codepoint.
func (p *Pane) trimRawHistory() {
	rawMax := p.scrollbackLines * 200
	if p.hasGraphics {
		rawMax = max(rawMax, 2*graphics.MaxSequenceBytes)
	}
	if len(p.rawBuf) <= rawMax {
		return
	}
	excess := len(p.rawBuf) - rawMax
	target := excess
	if nl := bytes.IndexByte(p.rawBuf[excess:], '\n'); nl >= 0 {
		target += nl + 1
	}
	cut := controlBoundary(p.rawBuf, target)
	p.rawBuf = p.rawBuf[cut:]
}

func controlBoundary(data []byte, target int) int {
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

func containsGraphics(data []byte) bool {
	for i := 0; i+2 < len(data); i++ {
		if data[i] == 0x1b && graphics.IsCommand(rune(data[i+1]), data[i+2:min(i+82, len(data))]) {
			return true
		}
	}
	return false
}
