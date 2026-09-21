package main

import (
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gdamore/tcell/v2"
)

func TestPassthroughKeys(t *testing.T) {
	for _, kitty := range []bool{false, true} {
		name := "legacy"
		if kitty {
			name = "kitty"
		}
		t.Run(name, func(t *testing.T) {
			p := regressionPane(40, 4)
			f, err := os.CreateTemp(t.TempDir(), "input")
			if err != nil {
				t.Fatal(err)
			}
			defer func() {
				if err := f.Close(); err != nil {
					t.Error(err)
				}
			}()
			p.ptmx = f
			if kitty {
				p.kittyStack = []int{1}
			}
			other := regressionPane(40, 4)
			app := &App{active: p, keys: resolveKeybindings(nil), redraw: make(chan struct{}, 1)}
			toggle := keyEv(tcell.KeyF12, tcell.ModCtrl)
			app.searchMode = true
			app.searchPane = p
			app.handleKey(toggle)
			if !p.passthrough || app.searchMode {
				t.Fatal("toggle did not enable passthrough and exit search")
			}
			var want strings.Builder
			for _, tc := range []struct {
				key           tcell.Key
				mod           tcell.ModMask
				legacy, kitty string
			}{
				{tcell.KeyF1, 0, "\x1bOP", "\x1bOP"},
				{tcell.KeyUp, tcell.ModAlt, "\x1b[1;3A", "\x1b[1;3A"},
				{tcell.KeyCtrlQ, tcell.ModCtrl, "\x11", "\x1b[113;5u"},
				{tcell.KeyCtrlF, tcell.ModCtrl, "\x06", "\x1b[102;5u"},
				{tcell.KeyCtrlC, tcell.ModCtrl, "\x03", "\x1b[99;5u"},
				{tcell.KeyCtrlV, tcell.ModCtrl, "\x16", "\x1b[118;5u"},
				{tcell.KeyPgUp, tcell.ModShift, "\x1b[5;2~", "\x1b[5;2~"},
			} {
				p.selActive = true
				if !app.handleKey(keyEv(tc.key, tc.mod)) {
					t.Fatal("passthrough initiated shutdown")
				}
				if kitty {
					want.WriteString(tc.kitty)
				} else {
					want.WriteString(tc.legacy)
				}
			}
			app.active = other
			if app.handleKey(keyEv(tcell.KeyCtrlQ, tcell.ModCtrl)) {
				t.Fatal("other pane inherited passthrough")
			}
			app.active = p
			app.handleKey(toggle)
			if p.passthrough {
				t.Fatal("toggle did not disable passthrough")
			}
			if app.handleKey(keyEv(tcell.KeyCtrlQ, tcell.ModCtrl)) {
				t.Fatal("normal bindings not restored")
			}
			got, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != want.String() {
				t.Fatalf("forwarded %q, want %q", got, want.String())
			}
		})
	}
}

func TestPassthroughRemappedToggle(t *testing.T) {
	p := regressionPane(20, 3)
	app := &App{active: p, keys: resolveKeybindings(map[string]string{"passthrough": "ctrl+f11"}), redraw: make(chan struct{}, 1)}
	for _, want := range []bool{true, false} {
		app.handleKey(keyEv(tcell.KeyF11, tcell.ModCtrl))
		if p.passthrough != want {
			t.Fatalf("passthrough = %v, want %v", p.passthrough, want)
		}
	}
}

func TestPassthroughBadges(t *testing.T) {
	for _, width := range []int{80, 12, 6} {
		t.Run(strconv.Itoa(width), func(t *testing.T) {
			scr := tcell.NewSimulationScreen("UTF-8")
			if err := scr.Init(); err != nil {
				t.Fatal(err)
			}
			defer scr.Fini()
			scr.SetSize(90, 4)
			p := regressionPane(width, 3)
			p.x = 2
			p.passthrough = true
			p.statusMsg = "COPIED"
			p.statusMsgEnd = time.Now().Add(time.Minute)
			p.fgProcess = "ssh"
			p.sshHost = "host"
			p.containerType = "podman"
			p.containerID = "box"
			p.sbOff = 2
			// Leave room for the scrollbar even in the smallest case.
			p.w = width + 1
			drawPaneStatus(scr, p, false, testTheme(), true)
			row := screenRowString(scr, 0, 90)
			if !strings.Contains(row, " PASS ") {
				t.Fatalf("PASS missing: %q", row)
			}
			if width == 80 {
				for _, badge := range []string{"COPIED", "ZOOM", "box", "host", " -2 "} {
					if !strings.Contains(row, badge) {
						t.Errorf("missing %s: %q", badge, row)
					}
				}
			}
		})
	}
}

func TestPassthroughBadgeRepaint(t *testing.T) {
	p := regressionPane(30, 3)
	scr := tcell.NewSimulationScreen("UTF-8")
	if err := scr.Init(); err != nil {
		t.Fatal(err)
	}
	defer scr.Fini()
	scr.SetSize(30, 3)
	for _, enabled := range []bool{false, true, false} {
		p.passthrough = enabled
		renderPane(scr, p, testTheme())
		drawPaneStatus(scr, p, true, testTheme(), false)
		row := screenRowString(scr, 0, 30)
		if strings.Contains(row, "PASS") != enabled {
			t.Fatalf("enabled=%v row=%q", enabled, row)
		}
	}
}

func TestPassthroughTerminalToggle(t *testing.T) {
	events := make(chan tcell.Event, 4)
	input := tcell.NewInputProcessor(events)
	input.ScanUTF8([]byte("\x1b[24;5~"))
	select {
	case event := <-events:
		key, ok := event.(*tcell.EventKey)
		if !ok {
			t.Fatalf("expected key, got %T", event)
		}
		p := regressionPane(20, 3)
		app := &App{active: p, keys: resolveKeybindings(nil), redraw: make(chan struct{}, 1)}
		app.handleKey(key)
		if !p.passthrough {
			t.Fatalf("Ctrl+F12 decoded as %s; toggle not activated", key.Name())
		}
	default:
		t.Fatal("Ctrl+F12 produced no event")
	}
}
