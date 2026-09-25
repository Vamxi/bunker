// gui_settings_keys.go - the Keyboard page of Preferences.
//
// Every shortcut, the window's and bunk's, is a key-cap button. Clicking it
// records the next key press: Escape cancels, Backspace turns the shortcut
// off. A key that another action already uses asks, in a bar at the top of
// the page, whether to move it. Each change is one [keys] line in the config
// file, like every other setting.
package main

import (
	"fmt"
	"strings"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// keyGroups lays out the Keyboard page.
var keyGroups = []struct {
	title   string
	actions []string
}{
	{"Tabs", []string{"new_tab", "close_tab", "next_tab", "prev_tab"}},
	{"Panes", []string{"split", "split_context", "zoom", "nav_left", "nav_right", "nav_up", "nav_down", "passthrough"}},
	{"Clipboard", []string{"copy_clipboard", "paste_clipboard", "paste_clipboard_alt", "copy", "paste"}},
	{"Scrollback and Search", []string{"scroll_up", "scroll_down", "search", "search_next", "search_prev", "search_exit"}},
	{"Text Size", []string{"font_bigger", "font_smaller", "font_reset"}},
	{"Window", []string{"preferences", "close_window", "quit"}},
}

// keyRow is one shortcut on the page.
type keyRow struct {
	action string
	def    string // canonical default
	pane   bool   // one of bunk's actions (keys bunk's reader knows only)
	button *gtk.Button
	label  *gtk.Label
	reset  *gtk.Button
	value  string // canonical current key
}

// keyAction describes an action for the page.
type keyAction struct {
	desc, def string
	pane      bool
}

func keyActions() map[string]keyAction {
	out := map[string]keyAction{}
	for _, w := range windowShortcuts {
		out[w.action] = keyAction{w.desc, w.def, false}
	}
	for _, e := range keybindingDefaults {
		def, _ := canonicalKey(e.def)
		out[e.action] = keyAction{e.desc, def, true}
	}
	out["quit"] = keyAction{"Close the window (bunk's quit key)", out["quit"].def, true}
	return out
}

// keyScope is when an action's key is live: search keys only while the
// search bar is open, window keys always.
func keyScope(action string) string {
	for _, w := range windowShortcuts {
		if w.action == action {
			return "window"
		}
	}
	if strings.HasPrefix(action, "search_") {
		return "search"
	}
	return "pane"
}

func (s *settingsWindow) buildKeyboard(p *settingsPage) {
	s.keyRows = map[string]*keyRow{}
	s.buildClashBar(p)
	actions := keyActions()
	for i, grp := range keyGroups {
		note := ""
		if i == 0 {
			note = "Click a shortcut and press the new keys. Esc cancels, Backspace turns a shortcut off. They are stored under [keys] in the config file."
		}
		g := p.group(grp.title, note)
		for _, action := range grp.actions {
			a := actions[action]
			r := &keyRow{action: action, def: a.def, pane: a.pane}
			r.label = gtk.NewLabel("")
			r.button = gtk.NewButton()
			r.button.SetChild(r.label)
			r.button.AddCSSClass("bunker-keycap")
			r.button.SetTooltipText("Change the shortcut")
			r.button.ConnectClicked(func() { s.startRecording(r) })
			r.reset = gtk.NewButtonFromIconName("edit-undo-symbolic")
			r.reset.AddCSSClass("flat")
			r.reset.ConnectClicked(func() { s.assignKey(r, r.def) })
			box := gtk.NewBox(gtk.OrientationHorizontal, 6)
			box.Append(r.reset)
			box.Append(r.button)
			s.keyRows[action] = r
			g.row(a.desc, "", box)
		}
	}
}

// buildClashBar adds the bar that asks before moving a key from another
// action.
func (s *settingsWindow) buildClashBar(p *settingsPage) {
	s.clashLabel = gtk.NewLabel("")
	s.clashLabel.SetWrap(true)
	s.clashLabel.SetXAlign(0)
	s.clashLabel.SetHExpand(true)
	replace := gtk.NewButtonWithLabel("Replace")
	replace.AddCSSClass("suggested-action")
	replace.ConnectClicked(func() {
		if c := s.clash; c != nil {
			s.clash = nil
			s.clashBar.SetRevealChild(false)
			s.assignKey(s.keyRows[c.other], keyNone)
			s.assignKey(c.row, c.key)
		}
	})
	cancel := gtk.NewButtonWithLabel("Cancel")
	cancel.ConnectClicked(func() {
		s.clash = nil
		s.clashBar.SetRevealChild(false)
	})
	box := gtk.NewBox(gtk.OrientationHorizontal, 10)
	box.AddCSSClass("bunker-clash")
	box.Append(s.clashLabel)
	box.Append(cancel)
	box.Append(replace)
	s.clashBar = gtk.NewRevealer()
	s.clashBar.SetChild(box)
	s.clashBar.SetRevealChild(false)
	p.box.Append(s.clashBar)
}

// keyClash is a key waiting for "Replace".
type keyClash struct {
	row        *keyRow
	key, other string
}

func (s *settingsWindow) startRecording(r *keyRow) {
	s.stopRecording()
	s.recording = r
	r.label.SetText("Press a shortcut…")
	r.button.AddCSSClass("bunker-recording")
}

func (s *settingsWindow) stopRecording() {
	if r := s.recording; r != nil {
		s.recording = nil
		r.button.RemoveCSSClass("bunker-recording")
		s.showKey(r)
	}
}

// recordKey takes the key press that ends a recording. It returns false
// for presses it does not consume (a lone modifier).
func (s *settingsWindow) recordKey(keyval, keycode uint, state gdk.ModifierType) bool {
	r := s.recording
	if r == nil {
		return false
	}
	mods := state & guiShortcutMods
	switch {
	case keyval == gdk.KEY_Escape && mods == 0:
		s.stopRecording()
		return true
	case keyval == gdk.KEY_BackSpace && mods == 0:
		s.stopRecording()
		s.assignKey(r, keyNone)
		return true
	}
	key, ok := pressedKey(keyval, keycode, state)
	if !ok {
		return !gdkModifierKeys[keyval] // unnamed keys are swallowed, modifiers wait for the key
	}
	s.stopRecording()
	if r.pane {
		if _, err := parseKey(key); err != nil {
			s.showStatus(fmt.Sprintf("%s can't be used for pane actions; they take letters, F-keys, arrows, and navigation keys.", shortcutLabel(key)))
			return true
		}
	}
	if other := s.keyOwner(key, r.action); other != "" {
		s.clash = &keyClash{row: r, key: key, other: other}
		s.clashLabel.SetText(fmt.Sprintf("%s is already used for “%s”. Replace it?", shortcutLabel(key), keyActions()[other].desc))
		s.clashBar.SetRevealChild(true)
		return true
	}
	s.assignKey(r, key)
	return true
}

// keyOwner is another action whose key is key and that can be live at the
// same time as action, or "".
func (s *settingsWindow) keyOwner(key, action string) string {
	scope := keyScope(action)
	for name, r := range s.keyRows {
		if name == action || r.value != key {
			continue
		}
		other := keyScope(name)
		if scope == other || scope == "window" || other == "window" {
			return name
		}
	}
	return ""
}

func (s *settingsWindow) assignKey(r *keyRow, key string) {
	if r == nil {
		return
	}
	s.write("keys", r.action, tomlString(key))
}

func (s *settingsWindow) showStatus(msg string) {
	s.status.SetText(msg)
	s.status.SetVisible(true)
}

// showKey shows a row's current key.
func (s *settingsWindow) showKey(r *keyRow) {
	if r.value == keyNone {
		r.label.SetText("Off")
		r.button.AddCSSClass("bunker-keycap-off")
	} else {
		r.label.SetText(shortcutLabel(r.value))
		r.button.RemoveCSSClass("bunker-keycap-off")
	}
	r.reset.SetVisible(r.value != r.def)
	r.reset.SetTooltipText("Reset to " + shortcutLabel(r.def))
}

// loadKeys shows cfg's shortcuts.
func (s *settingsWindow) loadKeys(cfg Config) {
	for _, r := range s.keyRows {
		raw := cfg.WindowKeys[r.action]
		if r.pane {
			for _, e := range keybindingDefaults {
				if e.action == r.action {
					raw = e.field(&cfg.Keybindings).raw
				}
			}
		}
		if c, err := canonicalKey(raw); err == nil {
			r.value = c
		} else {
			r.value = raw
		}
		if s.recording != r {
			s.showKey(r)
		}
	}
}
