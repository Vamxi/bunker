// gui_cursor.go - cursor blinking.
//
// [cursor] blink is "system", "on", or "off", as in Ptyxis. With "system"
// a program's request wins (DECSCUSR 1/3/5 blink, 2/4/6 are steady) and
// otherwise GNOME's cursor-blink setting decides. Like GTK's text fields,
// the cursor blinks only while the window has focus and stops, solid, after
// GNOME's blink timeout without input, so an idle window does not wake up.
package main

import (
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// cursorBlink is a view's blink state; the zero value blinks with GNOME's
// timing.
type cursorBlink struct {
	mode    string // [cursor] blink
	hidden  bool   // in the off half of a blink
	timer   coreglib.SourceHandle
	until   time.Time     // stop blinking after this
	half    time.Duration // 0: from GNOME
	timeout time.Duration // 0: from GNOME
}

// gtkSetting reads an integer or boolean GtkSettings property.
func gtkSetting[T int | bool](name string, fallback T) T {
	s := gtk.SettingsGetDefault()
	if s == nil {
		return fallback
	}
	if v, ok := s.ObjectProperty(name).(T); ok {
		return v
	}
	return fallback
}

func (b *cursorBlink) halfPeriod() time.Duration {
	if b.half > 0 {
		return b.half
	}
	return time.Duration(max(gtkSetting("gtk-cursor-blink-time", 1200), 200)) * time.Millisecond / 2
}

func (b *cursorBlink) idleTimeout() time.Duration {
	if b.timeout > 0 {
		return b.timeout
	}
	return time.Duration(max(gtkSetting("gtk-cursor-blink-timeout", 10), 1)) * time.Second
}

// wants reports whether a cursor of shape should blink.
func (b *cursorBlink) wants(shape int) bool {
	switch b.mode {
	case "on":
		return true
	case "off":
		return false
	}
	switch shape {
	case 1, 3, 5:
		return true
	case 2, 4, 6:
		return false
	}
	return gtkSetting("gtk-cursor-blink", true)
}

// kickBlink shows the cursor and (re)starts blinking from now: on key
// presses, focus, and when the program asks for a blinking cursor.
func (v *termView) kickBlink() {
	b := &v.blink
	b.until = time.Now().Add(b.idleTimeout())
	if b.hidden {
		b.hidden = false
		v.QueueDraw()
	}
	if b.timer != 0 || !v.focused || b.mode == "off" {
		return
	}
	b.timer = coreglib.TimeoutAdd(uint(b.halfPeriod().Milliseconds()), func() bool {
		defer guiRecover("cursor blink")
		if !v.focused || time.Now().After(b.until) || !v.activeCursorBlinks() {
			b.timer = 0
			if b.hidden {
				b.hidden = false
				v.QueueDraw()
			}
			return false
		}
		b.hidden = !b.hidden
		v.QueueDraw()
		return true
	})
}

// stopBlink leaves the cursor solid.
func (v *termView) stopBlink() {
	if v.blink.timer != 0 {
		coreglib.SourceRemove(v.blink.timer)
		v.blink.timer = 0
	}
	v.blink.hidden = false
}

// activeCursorBlinks reports whether the focused pane's cursor wants to
// blink, from its last drawn frame.
func (v *termView) activeCursorBlinks() bool {
	v.app.mu.Lock()
	p := v.app.active
	v.app.mu.Unlock()
	f := v.frames[p]
	return f != nil && f.cursorOn && v.blink.wants(f.cursor.Shape)
}
