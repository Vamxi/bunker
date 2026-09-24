package main

import (
	"testing"

	"bunker/internal/vt10x"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
)

func TestGDKKeyEventBytes(t *testing.T) {
	const (
		shift = gdk.ShiftMask
		ctrl  = gdk.ControlMask
		alt   = gdk.AltMask
	)
	tests := []struct {
		name   string
		keyval uint
		state  gdk.ModifierType
		kitty  int
		mode   vt10x.ModeFlag
		want   string
	}{
		{"letter", gdk.KEY_a, 0, 0, 0, "a"},
		{"shifted letter", gdk.KEY_A, shift, 0, 0, "A"},
		{"unicode", gdk.KEY_eacute, 0, 0, 0, "é"},
		{"enter", gdk.KEY_Return, 0, 0, 0, "\r"},
		{"tab", gdk.KEY_Tab, 0, 0, 0, "\t"},
		{"shift tab", gdk.KEY_ISO_Left_Tab, shift, 0, 0, "\x1b[Z"},
		{"backspace sends DEL", gdk.KEY_BackSpace, 0, 0, 0, "\x7f"},
		{"alt backspace", gdk.KEY_BackSpace, alt, 0, 0, "\x1b\x7f"},
		{"ctrl backspace", gdk.KEY_BackSpace, ctrl, 0, 0, "\x08"},
		{"escape", gdk.KEY_Escape, 0, 0, 0, "\x1b"},
		{"ctrl c", gdk.KEY_c, ctrl, 0, 0, "\x03"},
		{"ctrl shift c letter", gdk.KEY_C, ctrl | shift, 0, 0, "\x03"},
		{"alt x", gdk.KEY_x, alt, 0, 0, "\x1bx"},
		{"ctrl space", gdk.KEY_space, ctrl, 0, 0, "\x00"},
		{"ctrl bracket", gdk.KEY_bracketleft, ctrl, 0, 0, "\x1b"},
		{"ctrl slash", gdk.KEY_slash, ctrl, 0, 0, "\x1f"},
		{"up", gdk.KEY_Up, 0, 0, 0, "\x1b[A"},
		{"up app cursor", gdk.KEY_Up, 0, 0, vt10x.ModeAppCursor, "\x1bOA"},
		{"ctrl right", gdk.KEY_Right, ctrl, 0, 0, "\x1b[1;5C"},
		{"page down", gdk.KEY_Page_Down, 0, 0, 0, "\x1b[6~"},
		{"delete", gdk.KEY_Delete, 0, 0, 0, "\x1b[3~"},
		{"f1", gdk.KEY_F1, 0, 0, 0, "\x1bOP"},
		{"f5", gdk.KEY_F5, 0, 0, 0, "\x1b[15~"},
		{"keypad digit", gdk.KEY_KP_5, 0, 0, 0, "5"},
		{"keypad digit app mode", gdk.KEY_KP_5, 0, 0, vt10x.ModeAppKeypad, "\x1bOu"},
		{"kitty ctrl c", gdk.KEY_c, ctrl, 1, 0, "\x1b[99;5u"},
		{"kitty backspace", gdk.KEY_BackSpace, 0, 1, 0, "\x1b[127u"},
		{"kitty ctrl space", gdk.KEY_space, ctrl, 1, 0, "\x1b[32;5u"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ev, raw := gdkKeyEvent(tt.keyval, tt.state, tt.kitty)
			got := string(raw)
			if ev != nil {
				got = string(keyToBytesMode(ev, tt.kitty, tt.mode))
			}
			if got != tt.want {
				t.Fatalf("got %q, want %q", got, tt.want)
			}
		})
	}
}

func TestGDKKeyEventModifierOnly(t *testing.T) {
	for _, k := range []uint{gdk.KEY_Shift_L, gdk.KEY_Control_L, gdk.KEY_Alt_L, gdk.KEY_Super_L} {
		if ev, raw := gdkKeyEvent(k, 0, 0); ev != nil || raw != nil {
			t.Fatalf("keyval %#x produced input", k)
		}
	}
}

func TestBoxArmsTable(t *testing.T) {
	// ─ │ ┌ ┼ ╋ must be drawable; dashed ┄ and double ═ fall back to the font.
	for _, r := range []rune{'─', '│', '┌', '┼', '╋', '╴', '╿'} {
		if boxArms[r-0x2500] == 0 {
			t.Errorf("%q has no arms", r)
		}
	}
	for _, r := range []rune{'┄', '═', '╭'} {
		if boxArms[r-0x2500] != 0 {
			t.Errorf("%q unexpectedly procedural", r)
		}
	}
	if got := boxArms['┼'-0x2500]; got != 1|1<<2|1<<4|1<<6 {
		t.Errorf("┼ arms = %08b", got)
	}
}
