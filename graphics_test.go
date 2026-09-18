package main

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/png"
	"io"
	"os"
	"strings"
	"testing"
	"unicode/utf8"

	"bunk/internal/graphics"
	"bunk/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func graphicsPNG(t *testing.T) string {
	t.Helper()
	img := image.NewNRGBA(image.Rect(0, 0, 1, 2))
	img.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	img.SetNRGBA(0, 1, color.NRGBA{B: 255, A: 255})
	var b bytes.Buffer
	if err := png.Encode(&b, img); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func TestGraphicsPaneProtocolsAndClipping(t *testing.T) {
	for _, seq := range []string{
		"\x1b_Ga=T,f=100,c=2,r=1;" + graphicsPNG(t) + "\x1b\\",
		"\x1b]1337;File=inline=1;width=2;height=1;preserveAspectRatio=0:" + graphicsPNG(t) + "\a",
		"\x1bP0;1q#1;2;100;0;0!16~-#2;2;0;0;100!16~\x1b\\",
	} {
		p := regressionPane(3, 4)
		p.x, p.y = 2, 1
		var stream ptyStream
		for _, b := range []byte("\x1b[1;3H" + seq) {
			p.captureAndWrite(stream.scan([]byte{b}))
		}
		scr := tcell.NewSimulationScreen("UTF-8")
		if err := scr.Init(); err != nil {
			t.Fatal(err)
		}
		scr.SetSize(9, 6)
		scr.SetContent(5, 1, 'S', nil, tcell.StyleDefault)
		renderPane(scr, p, testTheme())
		ch, _, style, _ := scr.GetContent(4, 1) //nolint:staticcheck // inspect cell rendering
		fg, bg, _ := style.Decompose()
		if ch != '▀' || !fg.IsRGB() || !bg.IsRGB() {
			t.Fatalf("image not rendered: %q %v %v", ch, fg, bg)
		}
		ch, _, _, _ = scr.GetContent(5, 1) //nolint:staticcheck // sentinel outside pane
		if ch != 'S' {
			t.Fatal("image overwrote scrollbar")
		}
		if p.term.Cell(0, 1).Image != nil {
			t.Fatal("clipped image wrapped to next row")
		}
		scr.Fini()
	}
}

func TestGraphicsColorsAlphaAndReflow(t *testing.T) {
	p := regressionPane(8, 4)
	p.captureAndWrite([]byte("\x1b_Ga=T,i=9,f=100,c=2,r=1,C=1;" + graphicsPNG(t) + "\x1b\\"))
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(9, 4)
	renderPane(scr, p, testTheme())
	_, style, _ := scr.Get(0, 0)
	fg, bg, _ := style.Decompose()
	if fg != tcell.NewRGBColor(255, 0, 0) || bg != tcell.NewRGBColor(0, 0, 255) {
		t.Fatalf("half-block colours: fg=%v bg=%v", fg, bg)
	}
	if got := imageColor(color.NRGBA{R: 255, A: 128}, tcell.NewRGBColor(0, 0, 255)); got != tcell.NewRGBColor(128, 0, 127) {
		t.Fatalf("alpha=%v", got)
	}
	// The upload may fall outside the rolling replay window; cached image
	// placements must still survive a width change without sending new replies.
	p.rawBuf = []byte("\x1b_Ga=p,i=9,c=2,r=1,C=1\x1b\\")
	p.resize(0, 0, 8, 4)
	if cell := p.term.Cell(0, 0); cell.Image == nil || cell.Image.Bottom.B != 255 {
		t.Fatal("cached image lost on width reflow")
	}
	p.captureAndWrite([]byte("\x1b[4;1H\n"))
	if p.sb.count == 0 || p.sb.get(0)[0].Image == nil {
		t.Fatal("image lost from scrollback")
	}
	p.resize(0, 0, 8, 5)
	if p.term.Cell(0, 0).Image == nil {
		t.Fatal("height-only reflow lost image")
	}
}

func TestGraphicsPercentageReflowUsesPaneHeight(t *testing.T) {
	p := regressionPane(10, 6)
	p.captureAndWrite([]byte("\x1b]1337;File=inline=1;width=20%;height=50%;preserveAspectRatio=0:" + graphicsPNG(t) + "\a"))
	p.resize(0, 0, 10, 6)
	for row := 0; row < 6; row++ {
		hasImage := p.term.Cell(0, row).Image != nil
		if hasImage != (row < 3) {
			t.Fatalf("row %d has image=%v; percentage used scratch height", row, hasImage)
		}
	}
}

func TestGraphicsFramingAndHistoryBounds(t *testing.T) {
	for _, prefix := range []string{"\x1b_G", "\x1b]1337;File=inline=1:", "\x1bPq"} {
		var stream ptyStream
		sequence := []byte(prefix + strings.Repeat("A", oscMaxBuf+1) + "\x1b\\")
		if got := stream.scan(sequence); !bytes.Equal(got, sequence) {
			t.Fatal("graphics control truncated to text limit")
		}
		if got := stream.scan([]byte(prefix + strings.Repeat("A", graphics.MaxSequenceBytes+1) + "\x1b\\recovered")); string(got) != "\x18recovered" {
			t.Fatal("oversized graphics payload leaked into text")
		}
		if len(stream.pending) != 0 || cap(stream.pending) > oscMaxBuf {
			t.Fatal("graphics buffer retained after completion")
		}
	}
	seq := []byte("prefix\x1b_Gpayload\nmorepayload\x1b\\界suffix")
	for target := 7; target < 28; target++ {
		cut := controlBoundary(seq, target)
		if cut < target || !utf8.Valid(seq[cut:]) || bytes.Contains(seq[cut:], []byte("payload")) {
			t.Fatalf("unsafe trim at %d: %q", target, seq[cut:])
		}
	}
	p := regressionPane(8, 2)
	p.hasGraphics = true
	p.rawBuf = append(bytes.Repeat([]byte{'a'}, graphics.MaxSequenceBytes+10), []byte("\x1b_G"+strings.Repeat("A", graphics.MaxSequenceBytes-10)+"\x1b\\OK")...)
	p.trimRawHistory()
	if len(p.rawBuf) > 2*graphics.MaxSequenceBytes {
		t.Fatal("raw history exceeded graphics budget")
	}
}

func TestGraphicsPaneQueries(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := pr.Close(); err != nil {
			t.Logf("close read pipe: %v", err)
		}
		if err := pw.Close(); err != nil {
			t.Logf("close write pipe: %v", err)
		}
	})
	p := regressionPane(20, 5)
	p.ptmx = pw
	p.term = vt10x.New(vt10x.WithSize(20, 5), vt10x.WithGraphicsReply(p.writeInput))
	p.captureAndWrite([]byte("\x1b[14t\x1b[16t\x1b[18t\x1b_Ga=q,i=7,f=24,s=1,v=1;/wAA\x1b\\"))
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	want := "\x1b[4;80;160t\x1b[6;16;8t\x1b[8;5;20t\x1b_Gi=7;OK\x1b\\"
	if string(got) != want {
		t.Fatalf("replies=%q want=%q", got, want)
	}
	ws := paneWinsize(20, 5, 8, 16)
	if ws.X != 160 || ws.Y != 80 {
		t.Fatal("PTY virtual pixels do not match query replies")
	}
}

func TestGraphicsFramingAbortsMultipart(t *testing.T) {
	for _, cancellation := range []string{
		"\x1b_Gm=0;\x18",
		"\x1b_Gm=0;" + strings.Repeat("A", graphics.MaxSequenceBytes+1) + "\x1b\\",
	} {
		p := regressionPane(5, 3)
		var stream ptyStream
		p.captureAndWrite(stream.scan([]byte("\x1b_Ga=T,f=24,s=2,v=1,m=1;/wAA\x1b\\")))
		p.captureAndWrite(stream.scan([]byte(cancellation)))
		p.captureAndWrite(stream.scan([]byte("\x1b_Gm=0;AAD/\x1b\\OK")))
		if p.term.Cell(0, 0).Image != nil || p.term.Cell(0, 0).Char != 'O' {
			t.Fatal("cancelled upload was resumed")
		}
	}
}
