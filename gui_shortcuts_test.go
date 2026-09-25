package main

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

// Remapped window shortcuts drive the window; the old keys stop working
// and reach the program instead.
func TestGUI_RemappedShortcuts(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", map[string]string{
		"keys.new_tab":  `"f2"`,
		"keys.next_tab": `"ctrl+right"`,
		"keys.prev_tab": `"ctrl+left"`,
	})
	tabs := func() (n, active int) {
		onMain(func() { n, active = len(w.tabs), indexOf(w.tabs, w.active) })
		return
	}
	w.key(gdk.KEY_F2, 0)
	w.key(gdk.KEY_F2, 0)
	if n, active := tabs(); n != 3 || active != 2 {
		t.Fatalf("F2 twice: %d tabs, active %d; want 3, active 2", n, active)
	}
	w.key(gdk.KEY_T, ctrl|shift) // the old new-tab key is free now
	if n, _ := tabs(); n != 3 {
		t.Fatalf("Ctrl+Shift+T still opened a tab (%d tabs)", n)
	}
	w.key(gdk.KEY_Right, ctrl)
	if _, active := tabs(); active != 0 {
		t.Fatalf("Ctrl+Right from the last tab: active %d, want 0", active)
	}
	w.key(gdk.KEY_Left, ctrl)
	if _, active := tabs(); active != 2 {
		t.Fatalf("Ctrl+Left from the first tab: active %d, want 2", active)
	}
}

// Shifted digits match by their key: Ctrl+Shift+0 arrives as ')' on US
// keyboards and must still reset the font.
func TestGUI_ShortcutMatchesUnshiftedKey(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	k, ok := parseGUIKey("ctrl+shift+0")
	if !ok {
		t.Fatal("ctrl+shift+0 does not parse")
	}
	// XKB keycode of the 0 key (evdev KEY_0 = 11, plus 8). gotk4's
	// Display.MapKeyval misreads GTK's array, so it cannot be looked up.
	const keycode = 19
	onMain(func() {
		if baseKeyval(keycode) != gdk.KEY_0 {
			t.Skip("the test display's keymap is not US")
		}
	})
	onMain(func() {
		if !k.matches(gdk.KEY_parenright, keycode, gdk.ControlMask|gdk.ShiftMask) {
			t.Error("Ctrl+Shift+0 typed as ')' did not match")
		}
		if k.matches(gdk.KEY_parenright, keycode, gdk.ControlMask) {
			t.Error("matched without Shift")
		}
	})
	_ = w
}

// Preferences records shortcuts: a key press sets it, Escape cancels,
// Backspace turns it off, a key in use asks before moving it, and pane
// actions refuse keys bunk cannot read.
func TestGUI_RecordShortcut(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	var s *settingsWindow
	onMain(func() {
		w.openSettings()
		s = w.settings
	})
	fileHas := func(line string) bool {
		data, _ := os.ReadFile(w.path)
		return strings.Contains(string(data), line)
	}
	record := func(action string, keyval uint, state gdk.ModifierType) {
		onMain(func() {
			s.startRecording(s.keyRows[action])
			s.recordKey(keyval, 0, state)
		})
		settle(100 * time.Millisecond) // the directory monitor reloads
	}

	record("new_tab", gdk.KEY_F3, 0)
	if !fileHas(`new_tab             = "f3"`) {
		t.Fatal("recording F3 did not write new_tab")
	}
	waitMain(t, 3*time.Second, "new_tab reloaded", func() bool { return w.cfg.WindowKeys["new_tab"] == "f3" })

	record("new_tab", gdk.KEY_Escape, 0)
	if !fileHas(`new_tab             = "f3"`) {
		t.Fatal("Escape changed the shortcut")
	}

	record("close_tab", gdk.KEY_BackSpace, 0)
	waitMain(t, 3*time.Second, "close_tab off", func() bool { return w.cfg.WindowKeys["close_tab"] == keyNone })
	onMain(func() {
		if got := s.keyRows["close_tab"].label.Text(); got != "Off" {
			t.Errorf("a disabled shortcut shows %q", got)
		}
	})

	record("new_tab", gdk.KEY_F1, 0) // split's key
	onMain(func() {
		if !s.clashBar.RevealChild() || s.clash == nil || s.clash.other != "split" {
			t.Fatalf("taking split's key did not ask first (clash %+v)", s.clash)
		}
	})
	if fileHas(`new_tab             = "f1"`) {
		t.Fatal("a clashing key was written before Replace")
	}
	onMain(func() {
		s.assignKey(s.keyRows[s.clash.other], keyNone) // what Replace does
		s.assignKey(s.clash.row, s.clash.key)
		s.clash = nil
	})
	waitMain(t, 3*time.Second, "key moved", func() bool {
		return w.cfg.WindowKeys["new_tab"] == "f1" && w.cfg.Keybindings.Split.off
	})

	record("zoom", gdk.KEY_comma, gdk.ControlMask)
	onMain(func() {
		if !s.status.Visible() || !strings.Contains(s.status.Text(), "pane actions") {
			t.Errorf("a key bunk cannot read was not explained: %q", s.status.Text())
		}
	})
	if !fileHas(`zoom         = "f12"`) {
		t.Fatal("an unusable pane key was written")
	}
}

// The cursor blinks with "on", never with "off", and stops, visible, after
// the idle timeout.
func TestGUI_CursorBlink(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", map[string]string{"cursor.blink": `"on"`})
	p := w.panes()[0]
	w.waitDrawn(p, "")
	onMain(func() {
		v := w.active.view
		v.blink.half, v.blink.timeout = 20*time.Millisecond, 300*time.Millisecond
		v.focused = true
		v.stopBlink()
		v.kickBlink()
	})
	sawHidden := false
	waitMain(t, 2*time.Second, "cursor hides while blinking", func() bool {
		sawHidden = sawHidden || w.active.view.blink.hidden
		return sawHidden
	})
	waitMain(t, 3*time.Second, "blinking stops after the timeout", func() bool {
		v := w.active.view
		return v.blink.timer == 0 && !v.blink.hidden
	})

	writeKey(t, w.path, "cursor", "blink", `"off"`)
	waitMain(t, 3*time.Second, "blink off applied", func() bool { return w.active.view.blink.mode == "off" })
	onMain(func() {
		v := w.active.view
		v.kickBlink()
		if v.blink.timer != 0 {
			t.Error(`"off" started a blink timer`)
		}
	})
}

func TestGUI_PressedKey(t *testing.T) {
	needGUI(t)
	for _, tc := range []struct {
		name    string
		keyval  uint
		keycode uint
		state   gdk.ModifierType
		want    string
	}{
		{"letter", gdk.KEY_T, 0, gdk.ControlMask | gdk.ShiftMask, "ctrl+shift+t"},
		{"function key", gdk.KEY_F2, 0, 0, "f2"},
		{"page down", gdk.KEY_Page_Down, 0, gdk.ControlMask, "ctrl+pgdn"},
		{"arrow", gdk.KEY_Right, 0, gdk.ControlMask, "ctrl+right"},
		{"shift tab", gdk.KEY_ISO_Left_Tab, 0, gdk.ShiftMask, "shift+tab"},
		{"shifted digit by its key", gdk.KEY_exclam, 10, gdk.ControlMask | gdk.ShiftMask, "ctrl+shift+1"},
		{"punctuation", gdk.KEY_comma, 0, gdk.ControlMask, "ctrl+comma"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			onMain(func() {
				if got, ok := pressedKey(tc.keyval, tc.keycode, tc.state); !ok || got != tc.want {
					t.Errorf("pressedKey = %q, %v; want %q", got, ok, tc.want)
				}
			})
		})
	}
	onMain(func() {
		if _, ok := pressedKey(gdk.KEY_Control_L, 0, gdk.ControlMask); ok {
			t.Error("a lone modifier became a shortcut")
		}
	})
}

// Every default shortcut is a key GTK knows, labels readably, and survives
// the trip to a GTK accelerator.
func TestGUI_DefaultShortcutsAreGTKKeys(t *testing.T) {
	needGUI(t)
	onMain(func() {
		defaults := map[string]string{}
		for _, w := range windowShortcuts {
			defaults[w.action] = w.def
		}
		for _, e := range keybindingDefaults {
			defaults[e.action] = e.def
		}
		for action, def := range defaults {
			c, err := canonicalKey(def)
			if err != nil {
				t.Errorf("%s: %v", action, err)
				continue
			}
			if _, ok := parseGUIKey(c); !ok {
				t.Errorf("%s: GTK does not know %q", action, c)
			}
			if shortcutLabel(c) == "" || shortcutAccel(c) == "" {
				t.Errorf("%s: no label or accelerator for %q", action, c)
			}
		}
		for _, name := range keyNamesSpecial {
			if _, ok := parseGUIKey(name); !ok {
				t.Errorf("accepted key name %q is not a GDK key", name)
			}
		}
	})
}
