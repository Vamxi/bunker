// gui_input.go - GTK keyboard, mouse, focus, and clipboard input.
//
// Keys become tcell key events and go through bunk's encoder (keyToBytesMode),
// so kitty keyboard flags, application cursor/keypad modes, and modifier
// encodings match the TUI exactly. Mouse events become tcell mouse events in
// cell coordinates and go through App.handleMouse for selection, wheel
// scrollback, and SGR/X10 reporting.
package main

import (
	"context"

	"bunker/internal/vt10x"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/gdamore/tcell/v2"
)

func (v *termView) installInput() {
	keys := gtk.NewEventControllerKey()
	keys.ConnectKeyPressed(v.keyPressed)
	v.installIME(keys)
	v.AddController(keys)

	focus := gtk.NewEventControllerFocus()
	focus.ConnectEnter(func() { v.setFocused(true) })
	focus.ConnectLeave(func() { v.setFocused(false) })
	v.AddController(focus)

	click := gtk.NewGestureClick()
	click.SetButton(0)
	click.ConnectPressed(func(_ int, x, y float64) {
		v.GrabFocus()
		v.mouseButton(click.CurrentButton(), true, x, y, click.CurrentEventState())
	})
	click.ConnectReleased(func(_ int, x, y float64) {
		v.mouseButton(click.CurrentButton(), false, x, y, click.CurrentEventState())
	})
	v.AddController(click)

	motion := gtk.NewEventControllerMotion()
	motion.ConnectMotion(func(x, y float64) { v.mouseMotion(x, y, motion.CurrentEventState()) })
	motion.ConnectLeave(func() { v.updateHoverLink(-1, -1) })
	v.AddController(motion)

	scroll := gtk.NewEventControllerScroll(gtk.EventControllerScrollVertical)
	scroll.ConnectScroll(func(_, dy float64) bool {
		v.mouseScroll(dy, scroll.CurrentEventState())
		return true
	})
	v.AddController(scroll)
}

func (v *termView) setFocused(focused bool) {
	v.focused = focused
	v.imeFocus(focused)
	v.app.mu.Lock()
	p := v.app.active
	v.app.mu.Unlock()
	if p != nil {
		if focused {
			sendFocusIn(p)
		} else {
			sendFocusOut(p)
		}
	}
	v.QueueDraw()
}

// ---------------------------------------------------------------------------
// Keyboard
// ---------------------------------------------------------------------------

func (v *termView) keyPressed(keyval, _ uint, state gdk.ModifierType) bool {
	defer guiRecover("keyPressed")
	ctrl := state&gdk.ControlMask != 0
	shift := state&gdk.ShiftMask != 0

	v.app.mu.Lock()
	p := v.app.active
	v.app.mu.Unlock()
	if p == nil || p.isDead() {
		return false
	}
	p.mu.Lock()
	passthrough := p.passthrough
	kitty := 0
	if len(p.kittyStack) > 0 {
		kitty = p.kittyStack[len(p.kittyStack)-1]
	}
	p.mu.Unlock()

	// With bunk's keyboard passthrough on (Ctrl+F12) every key except the
	// toggle reaches the program, window shortcuts included.
	if !passthrough && v.windowShortcut(keyval, ctrl, shift) {
		return true
	}

	ev, raw := gdkKeyEvent(keyval, state, kitty)
	if ev == nil && raw == nil {
		return false // modifier-only or unmapped key: let GTK have it
	}
	if raw != nil {
		if v.app.searchMode {
			return true // the search bar takes typed text only
		}
		v.resetViewForInput(p)
		p.writeInput(raw)
		return true
	}
	// bunk's key handling: splits, zoom, pane navigation, search, copy and
	// paste, scrollback, passthrough, then the pane. It asks to quit with
	// false, which closes the window here.
	if !v.app.handleKey(ev) {
		v.ActivateAction("win.close-window", nil)
	}
	return true
}

// windowShortcut handles bunker's own keys: tabs, preferences, clipboard,
// font zoom. They use Ctrl+Shift or GNOME conventions so they don't collide
// with bunk's bindings.
func (v *termView) windowShortcut(keyval uint, ctrl, shift bool) bool {
	if ctrl {
		action := ""
		switch lower := gdk.KeyvalToLower(keyval); {
		case !shift && keyval == gdk.KEY_comma:
			action = "win.preferences"
		case shift && lower == gdk.KEY_t:
			action = "win.new-tab"
		case shift && lower == gdk.KEY_w:
			action = "win.close-tab"
		case !shift && (keyval == gdk.KEY_Page_Down || keyval == gdk.KEY_KP_Page_Down):
			action = "win.next-tab"
		case !shift && (keyval == gdk.KEY_Page_Up || keyval == gdk.KEY_KP_Page_Up):
			action = "win.prev-tab"
		}
		if action != "" {
			v.ActivateAction(action, nil)
			return true
		}
	}
	if ctrl && shift {
		switch gdk.KeyvalToLower(keyval) {
		case gdk.KEY_c:
			v.copySelection()
			return true
		case gdk.KEY_v:
			v.pasteClipboard()
			return true
		case gdk.KEY_plus, gdk.KEY_equal:
			v.zoomFont(1)
			return true
		case gdk.KEY_minus, gdk.KEY_underscore:
			v.zoomFont(-1)
			return true
		case gdk.KEY_BackSpace, gdk.KEY_0, gdk.KEY_parenright:
			v.zoomFont(0)
			return true
		}
	}
	if shift && !ctrl && (keyval == gdk.KEY_Insert || keyval == gdk.KEY_KP_Insert) {
		v.pasteClipboard()
		return true
	}
	return false
}

// resetViewForInput mirrors App.forwardKey for raw byte input.
func (v *termView) resetViewForInput(p *Pane) {
	if p.inScrollback() {
		p.scrollReset()
	}
	p.mu.Lock()
	p.selActive = false
	p.mu.Unlock()
	v.app.triggerRedraw()
}

// gdkKeyEvent converts a GDK key press into a tcell event for bunk's
// encoder. Ctrl+symbol combinations with no tcell key (Ctrl+Space, Ctrl+[,
// Ctrl+/ …) are returned as legacy raw bytes when kitty mode is off.
func gdkKeyEvent(keyval uint, state gdk.ModifierType, kittyFlags int) (*tcell.EventKey, []byte) {
	var mod tcell.ModMask
	if state&gdk.ShiftMask != 0 {
		mod |= tcell.ModShift
	}
	if state&gdk.ControlMask != 0 {
		mod |= tcell.ModCtrl
	}
	if state&gdk.AltMask != 0 {
		mod |= tcell.ModAlt
	}

	special := func(k tcell.Key) (*tcell.EventKey, []byte) {
		return tcell.NewEventKey(k, 0, mod), nil
	}
	keypad := func(k tcell.Key, r rune) (*tcell.EventKey, []byte) {
		return tcell.NewEventKey(k, r, mod|tcell.ModKeypad), nil
	}

	switch keyval {
	case gdk.KEY_Return:
		return special(tcell.KeyEnter)
	case gdk.KEY_KP_Enter:
		return keypad(tcell.KeyEnter, 0)
	case gdk.KEY_Tab:
		if mod&tcell.ModShift != 0 {
			return tcell.NewEventKey(tcell.KeyBacktab, 0, mod&^tcell.ModShift), nil
		}
		return special(tcell.KeyTab)
	case gdk.KEY_ISO_Left_Tab:
		return tcell.NewEventKey(tcell.KeyBacktab, 0, mod&^tcell.ModShift), nil
	case gdk.KEY_BackSpace:
		if kittyFlags&1 != 0 {
			return special(tcell.KeyBackspace2)
		}
		// tcell folds KeyBackspace2 into ^H; terminals send DEL for Backspace.
		if mod&tcell.ModCtrl != 0 {
			return nil, altPrefix(mod, []byte{0x08})
		}
		return nil, altPrefix(mod, []byte{0x7f})
	case gdk.KEY_Escape:
		return special(tcell.KeyEsc)
	case gdk.KEY_Up:
		return special(tcell.KeyUp)
	case gdk.KEY_Down:
		return special(tcell.KeyDown)
	case gdk.KEY_Left:
		return special(tcell.KeyLeft)
	case gdk.KEY_Right:
		return special(tcell.KeyRight)
	case gdk.KEY_Home:
		return special(tcell.KeyHome)
	case gdk.KEY_End:
		return special(tcell.KeyEnd)
	case gdk.KEY_Page_Up:
		return special(tcell.KeyPgUp)
	case gdk.KEY_Page_Down:
		return special(tcell.KeyPgDn)
	case gdk.KEY_Insert:
		return special(tcell.KeyInsert)
	case gdk.KEY_Delete:
		return special(tcell.KeyDelete)
	case gdk.KEY_KP_Up:
		return keypad(tcell.KeyUp, 0)
	case gdk.KEY_KP_Down:
		return keypad(tcell.KeyDown, 0)
	case gdk.KEY_KP_Left:
		return keypad(tcell.KeyLeft, 0)
	case gdk.KEY_KP_Right:
		return keypad(tcell.KeyRight, 0)
	case gdk.KEY_KP_Home:
		return keypad(tcell.KeyHome, 0)
	case gdk.KEY_KP_End:
		return keypad(tcell.KeyEnd, 0)
	case gdk.KEY_KP_Page_Up:
		return keypad(tcell.KeyPgUp, 0)
	case gdk.KEY_KP_Page_Down:
		return keypad(tcell.KeyPgDn, 0)
	case gdk.KEY_KP_Insert:
		return keypad(tcell.KeyInsert, 0)
	case gdk.KEY_KP_Delete:
		return keypad(tcell.KeyDelete, 0)
	case gdk.KEY_KP_Begin:
		return keypad(tcell.KeyClear, 0)
	}
	if keyval >= gdk.KEY_F1 && keyval <= gdk.KEY_F12 {
		return special(tcell.KeyF1 + tcell.Key(keyval-gdk.KEY_F1))
	}
	if keyval >= gdk.KEY_KP_0 && keyval <= gdk.KEY_KP_9 {
		return keypad(tcell.KeyRune, rune('0'+keyval-gdk.KEY_KP_0))
	}
	switch keyval {
	case gdk.KEY_KP_Decimal:
		return keypad(tcell.KeyRune, '.')
	case gdk.KEY_KP_Divide:
		return keypad(tcell.KeyRune, '/')
	case gdk.KEY_KP_Multiply:
		return keypad(tcell.KeyRune, '*')
	case gdk.KEY_KP_Subtract:
		return keypad(tcell.KeyRune, '-')
	case gdk.KEY_KP_Add:
		return keypad(tcell.KeyRune, '+')
	case gdk.KEY_KP_Equal:
		return keypad(tcell.KeyRune, '=')
	case gdk.KEY_KP_Separator:
		return keypad(tcell.KeyRune, ',')
	}

	r := rune(gdk.KeyvalToUnicode(keyval))
	if r == 0 {
		return nil, nil
	}
	if mod&tcell.ModCtrl != 0 {
		lower := rune(gdk.KeyvalToUnicode(gdk.KeyvalToLower(keyval)))
		if lower >= 'a' && lower <= 'z' {
			return tcell.NewEventKey(tcell.KeyCtrlA+tcell.Key(lower-'a'), lower, mod), nil
		}
		if kittyFlags&1 == 0 {
			if b, ok := ctrlSymbolByte(r); ok {
				return nil, altPrefix(mod, []byte{b})
			}
		}
	}
	return tcell.NewEventKey(tcell.KeyRune, r, mod), nil
}

// ctrlSymbolByte gives xterm's legacy control byte for Ctrl+symbol keys.
func ctrlSymbolByte(r rune) (byte, bool) {
	switch r {
	case ' ', '@', '2':
		return 0x00, true
	case '[', '3':
		return 0x1b, true
	case '\\', '4':
		return 0x1c, true
	case ']', '5':
		return 0x1d, true
	case '^', '6', '~':
		return 0x1e, true
	case '_', '/', '-', '7', '?':
		return 0x1f, true
	case '8':
		return 0x7f, true
	}
	return 0, false
}

func altPrefix(mod tcell.ModMask, b []byte) []byte {
	if mod&tcell.ModAlt != 0 {
		return append([]byte{0x1b}, b...)
	}
	return b
}

// ---------------------------------------------------------------------------
// Mouse
// ---------------------------------------------------------------------------

func gdkMods(state gdk.ModifierType) tcell.ModMask {
	var mod tcell.ModMask
	if state&gdk.ShiftMask != 0 {
		mod |= tcell.ModShift
	}
	if state&gdk.ControlMask != 0 {
		mod |= tcell.ModCtrl
	}
	if state&gdk.AltMask != 0 {
		mod |= tcell.ModAlt
	}
	return mod
}

func gdkButton(button uint) tcell.ButtonMask {
	switch button {
	case gdk.BUTTON_PRIMARY:
		return tcell.Button1
	case gdk.BUTTON_MIDDLE:
		return tcell.Button2
	case gdk.BUTTON_SECONDARY:
		return tcell.Button3
	}
	return tcell.ButtonNone
}

func (v *termView) sendMouse(x, y float64, btn tcell.ButtonMask, state gdk.ModifierType) {
	col, row := v.cellAt(x, y)
	v.mouseCell = [2]int{col, row}
	v.app.handleMouse(tcell.NewEventMouse(col, row, btn, gdkMods(state)))
}

func (v *termView) mouseButton(button uint, pressed bool, x, y float64, state gdk.ModifierType) {
	defer guiRecover("mouseButton")
	b := gdkButton(button)
	if b == tcell.ButtonNone {
		return
	}
	// Ctrl+click on a link opens it instead of reaching the pane.
	if pressed && b == tcell.Button1 && state&gdk.ControlMask != 0 {
		if link := v.linkAt(x, y); link != nil && safeLink(link.url) {
			v.openLink(link.url)
			return
		}
	}
	if pressed {
		v.mouseBtn |= b
	} else {
		v.mouseBtn &^= b
	}
	v.sendMouse(x, y, v.mouseBtn, state)
}

func (v *termView) mouseMotion(x, y float64, state gdk.ModifierType) {
	defer guiRecover("mouseMotion")
	col, row := v.cellAt(x, y)
	if [2]int{col, row} == v.mouseCell {
		return
	}
	v.updateHoverLink(x, y)
	v.sendMouse(x, y, v.mouseBtn, state)
}

// mouseScroll turns wheel/touchpad deltas into whole-line wheel events.
func (v *termView) mouseScroll(dy float64, state gdk.ModifierType) {
	defer guiRecover("mouseScroll")
	v.scrollAcc += dy
	x := v.pad + (float64(v.mouseCell[0])+0.5)*v.cellW
	y := v.pad + (float64(v.mouseCell[1])+0.5)*v.cellH
	for v.scrollAcc <= -1 {
		v.scrollAcc++
		v.sendMouse(x, y, v.mouseBtn|tcell.WheelUp, state)
	}
	for v.scrollAcc >= 1 {
		v.scrollAcc--
		v.sendMouse(x, y, v.mouseBtn|tcell.WheelDown, state)
	}
}

// ---------------------------------------------------------------------------
// Clipboard
// ---------------------------------------------------------------------------

func (v *termView) copySelection() {
	v.app.mu.Lock()
	p := v.app.active
	v.app.mu.Unlock()
	if p == nil {
		return
	}
	p.mu.Lock()
	text := ""
	if p.selActive {
		text = p.selText()
	}
	p.mu.Unlock()
	if text != "" {
		v.Clipboard().SetText(text)
	}
}

func (v *termView) pasteClipboard() {
	clip := v.Clipboard()
	clip.ReadTextAsync(context.Background(), func(res gio.AsyncResulter) {
		defer guiRecover("paste")
		text, err := clip.ReadTextFinish(res)
		if err != nil || text == "" {
			return
		}
		v.pasteText(text)
	})
}

// pasteText writes text as a paste (see pasteBytes): CR line endings, and
// bracketed without any ESC inside when the application enabled DECSET 2004.
func (v *termView) pasteText(text string) {
	v.app.mu.Lock()
	p := v.app.active
	v.app.mu.Unlock()
	if p == nil || p.isDead() {
		return
	}
	p.mu.Lock()
	bracketed := p.term.Mode()&vt10x.ModeSetPaste != 0
	p.mu.Unlock()
	v.resetViewForInput(p)
	p.writeInput(pasteBytes(text, bracketed))
}
