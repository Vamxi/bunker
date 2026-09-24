package main

import (
	"io"
	"os"
	"strings"
	"testing"

	"bunker/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func TestAuditQueryReplies(t *testing.T) {
	for _, tc := range []struct{ name, input, process, want string }{
		{"DA3", "\x1b[=c\x1b[=0c", "", strings.Repeat("\x1bP!|00000000\x1b\\", 2)},
		{"DSR", "\x1b[5n", "", "\x1b[0n"},
		{"remote DSR", "\x1b[5n", "ssh", "\x1b[0n"},
		{"alternate cursor", "\x1b[?1049h\x1b[3;4H\x1b[6n", "", "\x1b[3;4R"},
		{"origin cursor", "\x1b[2;5r\x1b[?6h\x1b[2;3H\x1b[6n\x1b[?6n", "", "\x1b[2;3R\x1b[?2;3R"},
		{"SGR status", "\x1b[21m\x1bP$qm\x1b\\", "", "\x1bP1$r0;4:2m\x1b\\"},
		{"unknown status", "\x1bP$qinvalid\x1b\\", "", "\x1bP0$r\x1b\\"},
		{"unknown mode", "\x1b[?999$p", "", "\x1b[?999;0$y"},
		{"cursor blink", "\x1b[?12l\x1b[?12$p\x1b[?12h\x1b[?12$p", "", "\x1b[?12;2$y\x1b[?12;1$y"},
		{"grapheme mode", "\x1b[?2027$p\x1b[?2027l\x1b[?2027$p", "", "\x1b[?2027;1$y\x1b[?2027;2$y"},
		{"no queries inside APC", "\x1b_ignored\x1b[5n\x1b\\", "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pr, pw, err := os.Pipe()
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { pr.Close(); pw.Close() }) //nolint:errcheck // test pipe cleanup
			p := regressionPane(20, 5)
			p.ptmx, p.fgProcess = pw, tc.process
			var stream ptyStream
			for _, b := range []byte(tc.input) {
				p.captureAndWrite(stream.scan([]byte{b}))
			}
			if err := pw.Close(); err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(pr)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("reply=%q want %q", got, tc.want)
			}
		})
	}
}

func TestAuditKeypadAndCursorInput(t *testing.T) {
	for _, tc := range []struct {
		name, host, want string
		mode             vt10x.ModeFlag
		flags            int
	}{
		{"numeric keypad", "\x1bOp", "0", 0, 0},
		{"application keypad", "\x1bOu", "\x1bOu", vt10x.ModeAppKeypad, 0},
		{"keypad enter", "\x1bOM", "\x1bOM", vt10x.ModeAppKeypad, 0},
		{"kitty keypad", "\x1b[57404u", "\x1b[57404;1u", 0, 1},
		{"normal arrow", "\x1b[A", "\x1b[A", 0, 0},
		{"application arrow", "\x1b[A", "\x1bOA", vt10x.ModeAppCursor, 0},
		{"modified arrow", "\x1b[1;5A", "\x1b[1;5A", vt10x.ModeAppCursor, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ch := make(chan tcell.Event, 2)
			tcell.NewInputProcessor(ch).ScanUTF8([]byte(tc.host))
			select {
			case event := <-ch:
				ev, ok := event.(*tcell.EventKey)
				if !ok {
					t.Fatalf("not a key: %T", event)
				}
				if got := string(keyToBytesMode(ev, tc.flags, tc.mode)); got != tc.want {
					t.Fatalf("encoded=%q want %q", got, tc.want)
				}
			default:
				t.Fatal("no key event")
			}
		})
	}
}

func TestAuditGraphemeCopySearchReflow(t *testing.T) {
	p := regressionPane(20, 4)
	p.captureAndWrite([]byte("é❤️👩‍👩‍👧‍👦X"))
	p.selActive, p.selAnchor, p.selCursor = true, selPos{0, 0}, selPos{0, 5}
	if got := p.selText(); got != "é❤️👩‍👩‍👧‍👦X" {
		t.Fatalf("copy=%q", got)
	}
	app := &App{searchPane: p, searchQuery: "👩‍👩‍👧‍👦", searchMode: true}
	app.runSearchScan()
	if len(app.searchMatches) != 1 || app.searchMatches[0].col != 3 || app.searchMatches[0].length != 2 {
		t.Fatalf("matches=%v", app.searchMatches)
	}
	p.resize(0, 0, 11, 4)
	if p.term.Cell(0, 0).Text() != "é" || p.term.Cell(3, 0).Text() != "👩‍👩‍👧‍👦" {
		t.Fatalf("reflow=%q", p.term.String())
	}
}

type cursorAuditScreen struct {
	tcell.Screen
	style tcell.CursorStyle
	color tcell.Color
}

func (s *cursorAuditScreen) SetCursorStyle(style tcell.CursorStyle, colors ...tcell.Color) {
	s.style = style
	if len(colors) > 0 {
		s.color = colors[0]
	}
}

func TestAuditCursorColorAndDecorations(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(20, 5)
	p := regressionPane(19, 5)
	p.captureAndWrite([]byte("\x1b[21;53mA\x1b[0mB\x1b]12;#112233\a\x1b[5 q\x1b[?12l"))
	renderPane(scr, p, testTheme())
	_, style, _ := scr.Get(0, 0)
	_, _, attrs := style.Decompose()
	if attrs&tcell.AttrOverline == 0 || style.GetUnderlineStyle() != tcell.UnderlineStyleDouble {
		t.Fatal("decorations not rendered")
	}
	_, style, _ = scr.Get(1, 0)
	_, _, attrs = style.Decompose()
	if attrs&tcell.AttrOverline != 0 || style.GetUnderlineStyle() != tcell.UnderlineStyleNone {
		t.Fatal("decorations leaked after reset")
	}
	cs := &cursorAuditScreen{Screen: scr}
	app := &App{screen: cs}
	app.emitCursorStyle(p)
	if cs.style != tcell.CursorStyleSteadyBar || cs.color != tcell.NewRGBColor(0x11, 0x22, 0x33) {
		t.Fatal("cursor shape or color not forwarded")
	}
	p.captureAndWrite([]byte("\x1b]112\a"))
	app.emitCursorStyle(p)
	if cs.color != tcell.ColorReset {
		t.Fatal("cursor color not reset")
	}
}

func TestAuditKittySetOperations(t *testing.T) {
	p := regressionPane(10, 2)
	p.handleKittyKeyboard([]byte("\x1b[=1u\x1b[=4;2u"))
	if len(p.kittyStack) != 1 || p.kittyStack[0] != 5 {
		t.Fatalf("set bits: %v", p.kittyStack)
	}
	p.handleKittyKeyboard([]byte("\x1b[=1;3u"))
	if p.kittyStack[0] != 4 {
		t.Fatalf("clear bits: %v", p.kittyStack)
	}
	p.handleKittyKeyboard([]byte("\x1b]2;title\x1b[=9u\a"))
	if p.kittyStack[0] != 4 {
		t.Fatal("title changed keyboard negotiation")
	}
}

func TestAuditPrintedAndErasedSpaces(t *testing.T) {
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(8, 2)
	p := regressionPane(7, 2)
	p.captureAndWrite([]byte("\x1b[21;53m \x1b[K"))
	renderPane(scr, p, testTheme())
	for x := 0; x < 7; x++ {
		_, style, _ := scr.Get(x, 0)
		_, _, attrs := style.Decompose()
		decorated := attrs&tcell.AttrOverline != 0 && style.GetUnderlineStyle() == tcell.UnderlineStyleDouble
		if decorated != (x == 0) {
			t.Fatalf("column %d decorated=%v", x, decorated)
		}
	}
}

func TestAuditCursorFallback(t *testing.T) {
	for _, tc := range []struct {
		input string
		want  tcell.Color
	}{
		{"", tcell.ColorReset}, {"unknown", tcell.ColorReset},
		{"rgb:ffff/0000/8888", tcell.NewRGBColor(255, 0, 136)},
		{"rgb:f/0/8", tcell.NewRGBColor(255, 0, 136)},
		{"#ff0088", tcell.NewRGBColor(255, 0, 136)},
	} {
		t.Run(tc.input, func(t *testing.T) {
			if got := oscCursorColor(tc.input); got != tc.want {
				t.Fatalf("color=%v want %v", got, tc.want)
			}
		})
	}
}

func TestAuditHostFocusForwarding(t *testing.T) {
	pr, pw, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pr.Close(); pw.Close() }) //nolint:errcheck // test cleanup
	p := regressionPane(10, 2)
	p.ptmx = pw
	app := &App{active: p}
	app.handleFocus(true)
	p.captureAndWrite([]byte("\x1b[?1004h"))
	app.handleFocus(false)
	app.handleFocus(true)
	if err := pw.Close(); err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(pr)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "\x1b[O\x1b[I" {
		t.Fatalf("focus=%q", got)
	}
}

func TestAuditModifierPreservation(t *testing.T) {
	for _, tc := range []struct {
		name  string
		key   tcell.Key
		mod   tcell.ModMask
		flags int
		want  string
	}{
		{"ctrl shift", tcell.KeyCtrlA, tcell.ModCtrl | tcell.ModShift, 1, "\x1b[97;6u"},
		{"ctrl alt", tcell.KeyCtrlA, tcell.ModCtrl | tcell.ModAlt, 1, "\x1b[97;7u"},
		{"legacy alt control", tcell.KeyCtrlA, tcell.ModCtrl | tcell.ModAlt, 0, "\x1b\x01"},
		{"legacy alt enter", tcell.KeyEnter, tcell.ModAlt, 0, "\x1b\r"},
		{"legacy alt backspace", tcell.KeyBackspace, tcell.ModAlt, 0, "\x1b\x08"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ev := tcell.NewEventKey(tc.key, 0, tc.mod)
			if got := string(keyToBytes(ev, tc.flags)); got != tc.want {
				t.Fatalf("encoded=%q want %q", got, tc.want)
			}
		})
	}
}
