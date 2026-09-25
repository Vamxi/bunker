// shortcuts.go - bunker's window shortcuts and key-clash detection.
//
// bunk's pane actions (split, zoom, search, …) live in keybindingDefaults.
// The window's own actions (tabs, clipboard, font size, Preferences) live
// here. Both are set under [keys] with the same syntax, "mod+mod+key", and
// "none" switches one off. The window handles its shortcuts first, so when
// a key is set twice the window action wins; findKeyClashes names every
// such key so the user sees it.
package main

import (
	"fmt"
	"slices"
	"strings"
)

// windowShortcut is one of bunker's own actions.
type windowShortcut struct {
	action string // key under [keys]
	def    string
	desc   string
}

// windowShortcuts lists bunker's actions in the order Preferences shows them.
var windowShortcuts = []windowShortcut{
	{"new_tab", "ctrl+shift+t", "New tab in the current directory"},
	{"close_tab", "ctrl+shift+w", "Close tab"},
	{"next_tab", "ctrl+pgdn", "Next tab"},
	{"prev_tab", "ctrl+pgup", "Previous tab"},
	{"copy_clipboard", "ctrl+shift+c", "Copy the selection"},
	{"paste_clipboard", "ctrl+shift+v", "Paste"},
	{"paste_clipboard_alt", "shift+insert", "Paste (alternative key)"},
	{"font_bigger", "ctrl+shift+plus", "Make text bigger"},
	{"font_smaller", "ctrl+shift+minus", "Make text smaller"},
	{"font_reset", "ctrl+shift+0", "Reset text size"},
	{"preferences", "ctrl+comma", "Preferences"},
	{"close_window", "ctrl+shift+q", "Close window"},
}

// keyNone switches a shortcut off.
const keyNone = "none"

// keyAliases maps accepted spellings to the one canonicalKey writes.
var keyAliases = map[string]string{
	"esc": "escape", "return": "enter", "pageup": "pgup", "pagedown": "pgdn",
	"del": "delete", "ins": "insert",
}

// keyNamesSpecial are the non-character keys a shortcut may use, besides
// f1-f24, letters, and digits.
var keyNamesSpecial = []string{
	"up", "down", "left", "right", "pgup", "pgdn", "home", "end",
	"enter", "escape", "backspace", "delete", "tab", "insert", "space",
	"comma", "period", "slash", "backslash", "semicolon", "apostrophe",
	"grave", "minus", "equal", "plus", "underscore", "bracketleft",
	"bracketright",
}

// canonicalKey checks a shortcut and returns it in one spelling:
// lowercase, modifiers in the order ctrl, alt, shift, aliases resolved.
// "none" stays "none".
func canonicalKey(s string) (string, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == keyNone {
		return keyNone, nil
	}
	if strings.HasPrefix(s, "<") {
		s = fromGTKAccel(s)
	}
	parts := strings.Split(s, "+")
	name := parts[len(parts)-1]
	if a, ok := keyAliases[name]; ok {
		name = a
	}
	if !validKeyName(name) {
		return "", fmt.Errorf("unknown key %q in %q", parts[len(parts)-1], s)
	}
	var ctrl, alt, shift bool
	for _, m := range parts[:len(parts)-1] {
		switch m {
		case "ctrl":
			ctrl = true
		case "alt":
			alt = true
		case "shift":
			shift = true
		default:
			return "", fmt.Errorf("unknown modifier %q in %q", m, s)
		}
	}
	var out []string
	for _, m := range []struct {
		on   bool
		name string
	}{{ctrl, "ctrl"}, {alt, "alt"}, {shift, "shift"}} {
		if m.on {
			out = append(out, m.name)
		}
	}
	return strings.Join(append(out, name), "+"), nil
}

// gtkKeyAliases maps GTK key names (lowercased) to [keys] names.
var gtkKeyAliases = map[string]string{
	"page_up": "pgup", "page_down": "pgdn", "prior": "pgup", "next": "pgdn",
	"iso_left_tab": "tab",
}

// fromGTKAccel turns a lowercased GTK accelerator ("<control><shift>t", as
// gsettings and dconf write them) into [keys] syntax.
func fromGTKAccel(s string) string {
	var mods []string
	for strings.HasPrefix(s, "<") {
		end := strings.IndexByte(s, '>')
		if end < 0 {
			return s
		}
		switch s[1:end] {
		case "control", "ctrl", "primary":
			mods = append(mods, "ctrl")
		case "shift":
			mods = append(mods, "shift")
		case "alt", "mod1":
			mods = append(mods, "alt")
		default:
			mods = append(mods, s[1:end]) // reported as unknown by the caller
		}
		s = s[end+1:]
	}
	if a, ok := gtkKeyAliases[s]; ok {
		s = a
	}
	return strings.Join(append(mods, s), "+")
}

func validKeyName(name string) bool {
	switch {
	case len(name) == 1 && (name[0] >= 'a' && name[0] <= 'z' || name[0] >= '0' && name[0] <= '9'):
		return true
	case len(name) >= 2 && name[0] == 'f':
		var n int
		_, err := fmt.Sscanf(name[1:], "%d", &n)
		return err == nil && n >= 1 && n <= 24 && fmt.Sprint(n) == name[1:]
	}
	return slices.Contains(keyNamesSpecial, name)
}

// resolveWindowKeys returns each window action's shortcut in canonical form,
// taking overrides from [keys]. An invalid override keeps the default and is
// returned as a problem.
func resolveWindowKeys(keys map[string]string) (map[string]string, []string) {
	out := make(map[string]string, len(windowShortcuts))
	var problems []string
	for _, w := range windowShortcuts {
		s := w.def
		if v, ok := keys[w.action]; ok && strings.TrimSpace(v) != "" {
			s = v
		}
		c, err := canonicalKey(s)
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v; using %s", w.action, err, w.def))
			c, _ = canonicalKey(w.def)
		}
		out[w.action] = c
	}
	return out, problems
}

// findKeyClashes reports keys given to more than one action that can fire at
// the same time: window shortcuts with bunk's pane keys, and window shortcuts
// with the search bar's keys. It returns one message per clash.
func findKeyClashes(windowKeys map[string]string, pane map[string]string) []string {
	type owner struct{ action, scope string }
	byKey := map[string][]owner{}
	add := func(key, action, scope string) {
		if key != "" && key != keyNone {
			byKey[key] = append(byKey[key], owner{action, scope})
		}
	}
	for _, w := range windowShortcuts {
		add(windowKeys[w.action], w.action, "window")
	}
	for _, e := range keybindingDefaults {
		c, err := canonicalKey(pane[e.action])
		if err != nil {
			continue
		}
		scope := "pane"
		if strings.HasPrefix(e.action, "search_") {
			scope = "search"
		}
		add(c, e.action, scope)
	}
	var clashes []string
	keys := make([]string, 0, len(byKey))
	for k := range byKey {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, key := range keys {
		owners := byKey[key]
		for i := range owners {
			for j := i + 1; j < len(owners); j++ {
				a, b := owners[i], owners[j]
				if a.scope != b.scope && a.scope != "window" && b.scope != "window" {
					continue // pane and search keys never fire together
				}
				outcome := "only one of them works"
				switch {
				case a.scope == "window" && b.scope != "window":
					outcome = a.action + " wins"
				case b.scope == "window" && a.scope != "window":
					outcome = b.action + " wins"
				}
				clashes = append(clashes, fmt.Sprintf("%s is set for both %s and %s; %s", keyLabel(key), a.action, b.action, outcome))
			}
		}
	}
	return clashes
}

// keyLabel spells a canonical key for people: "Ctrl+Shift+T", "F2".
func keyLabel(key string) string {
	if key == keyNone {
		return "None"
	}
	parts := strings.Split(key, "+")
	for i, p := range parts {
		switch {
		case p == "pgup":
			parts[i] = "PgUp"
		case p == "pgdn":
			parts[i] = "PgDn"
		case len(p) == 1:
			parts[i] = strings.ToUpper(p)
		case p[0] == 'f' && validKeyName(p):
			parts[i] = strings.ToUpper(p)
		default:
			parts[i] = strings.ToUpper(p[:1]) + p[1:]
		}
	}
	return strings.Join(parts, "+")
}
