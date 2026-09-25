// gui_shortcuts.go - window shortcuts in GTK terms: matching key presses,
// labels, application accelerators, and turning a key press into the
// [keys] syntax for Preferences.
package main

import (
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// guiShortcutMods are the modifiers shortcuts use; others (Caps Lock, Num
// Lock, Super) are ignored when matching.
const guiShortcutMods = gdk.ControlMask | gdk.AltMask | gdk.ShiftMask

// gdkKeyNames spells [keys] names that differ from GDK's key names.
var gdkKeyNames = map[string]string{
	"up": "Up", "down": "Down", "left": "Left", "right": "Right",
	"pgup": "Page_Up", "pgdn": "Page_Down", "home": "Home", "end": "End",
	"enter": "Return", "escape": "Escape", "backspace": "BackSpace",
	"delete": "Delete", "tab": "Tab", "insert": "Insert",
}

// guiKey is a canonical shortcut as GDK sees it.
type guiKey struct {
	keyval uint
	mods   gdk.ModifierType
}

// parseGUIKey turns a canonical key ("ctrl+shift+t") into a GDK key; ok is
// false for "none" or a name GDK does not know.
func parseGUIKey(canon string) (guiKey, bool) {
	if canon == "" || canon == keyNone {
		return guiKey{}, false
	}
	parts := strings.Split(canon, "+")
	var k guiKey
	for _, m := range parts[:len(parts)-1] {
		switch m {
		case "ctrl":
			k.mods |= gdk.ControlMask
		case "alt":
			k.mods |= gdk.AltMask
		case "shift":
			k.mods |= gdk.ShiftMask
		}
	}
	name := parts[len(parts)-1]
	switch {
	case gdkKeyNames[name] != "":
		name = gdkKeyNames[name]
	case len(name) >= 2 && name[0] == 'f' && validKeyName(name):
		name = "F" + name[1:]
	}
	k.keyval = gdk.KeyvalFromName(name)
	return k, k.keyval != 0 && k.keyval != gdk.KEY_VoidSymbol
}

// matches reports whether a key press is k. The press counts when either
// the keyval it produced or the key's unshifted keyval matches, so
// "ctrl+shift+0" works although Shift turns 0 into ')'.
func (k guiKey) matches(keyval, keycode uint, state gdk.ModifierType) bool {
	if state&guiShortcutMods != k.mods {
		return false
	}
	if gdk.KeyvalToLower(keyval) == k.keyval {
		return true
	}
	return keycode != 0 && baseKeyval(keycode) == k.keyval
}

// baseKeyval is what keycode types without modifiers, or 0.
func baseKeyval(keycode uint) uint {
	d := gdk.DisplayGetDefault()
	if d == nil {
		return 0
	}
	kv, _, _, _, ok := d.TranslateKey(keycode, 0, 0)
	if !ok {
		return 0
	}
	return gdk.KeyvalToLower(kv)
}

// shortcutLabel spells a canonical key the way GTK shows shortcuts in
// menus ("Shift+Ctrl+T"), falling back to keyLabel.
func shortcutLabel(canon string) string {
	if k, ok := parseGUIKey(canon); ok {
		return gtk.AcceleratorGetLabel(k.keyval, k.mods)
	}
	return keyLabel(canon)
}

// shortcutAccel is the GTK accelerator for a canonical key, or "".
func shortcutAccel(canon string) string {
	if k, ok := parseGUIKey(canon); ok {
		return gtk.AcceleratorName(k.keyval, k.mods)
	}
	return ""
}

// gdkModifierKeys are keys that only modify others; recording ignores them.
var gdkModifierKeys = map[uint]bool{
	gdk.KEY_Shift_L: true, gdk.KEY_Shift_R: true, gdk.KEY_Control_L: true,
	gdk.KEY_Control_R: true, gdk.KEY_Alt_L: true, gdk.KEY_Alt_R: true,
	gdk.KEY_Meta_L: true, gdk.KEY_Meta_R: true, gdk.KEY_Super_L: true,
	gdk.KEY_Super_R: true, gdk.KEY_ISO_Level3_Shift: true, gdk.KEY_Caps_Lock: true,
	gdk.KEY_Num_Lock: true,
}

// pressedKey turns a key press into canonical [keys] syntax, preferring the
// key's unshifted name ("ctrl+shift+1", not "ctrl+shift+exclam"). ok is
// false for a lone modifier or a key [keys] cannot name.
func pressedKey(keyval, keycode uint, state gdk.ModifierType) (string, bool) {
	if gdkModifierKeys[keyval] {
		return "", false
	}
	var mods []string
	if state&gdk.ControlMask != 0 {
		mods = append(mods, "ctrl")
	}
	if state&gdk.AltMask != 0 {
		mods = append(mods, "alt")
	}
	if state&gdk.ShiftMask != 0 {
		mods = append(mods, "shift")
	}
	for _, kv := range []uint{baseKeyval(keycode), gdk.KeyvalToLower(keyval)} {
		if kv == 0 {
			continue
		}
		if name, ok := keysName(kv); ok {
			c, err := canonicalKey(strings.Join(append(mods, name), "+"))
			return c, err == nil
		}
	}
	return "", false
}

// keysName is the [keys] name for a keyval.
func keysName(keyval uint) (string, bool) {
	name := gdk.KeyvalName(keyval)
	for ours, gdkName := range gdkKeyNames {
		if gdkName == name {
			return ours, true
		}
	}
	if keyval == gdk.KEY_ISO_Left_Tab {
		return "tab", true
	}
	lower := strings.ToLower(name)
	return lower, validKeyName(lower)
}
