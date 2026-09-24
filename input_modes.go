package main

import (
	"fmt"

	"bunker/internal/vt10x"

	"github.com/gdamore/tcell/v2"
)

func keyToBytesMode(ev *tcell.EventKey, flags int, mode vt10x.ModeFlag) []byte {
	mod := ev.Modifiers()
	if mod&tcell.ModKeypad != 0 {
		if code, ss3, ok := keypadCode(ev); ok {
			if flags&1 != 0 {
				m := 1
				if mod&tcell.ModShift != 0 {
					m += 1
				}
				if mod&tcell.ModAlt != 0 {
					m += 2
				}
				if mod&tcell.ModCtrl != 0 {
					m += 4
				}
				return fmt.Appendf(nil, "\x1b[%d;%du", code, m)
			}
			if mode&vt10x.ModeAppKeypad != 0 {
				return []byte{'\x1b', 'O', ss3}
			}
		}
	}
	data := keyToBytes(ev, flags)
	if mode&vt10x.ModeAppCursor != 0 && mod&^tcell.ModKeypad == 0 && len(data) == 3 && data[0] == 27 && data[1] == '[' {
		switch data[2] {
		case 'A', 'B', 'C', 'D', 'H', 'F':
			data[1] = 'O'
		}
	}
	return data
}

func keypadCode(ev *tcell.EventKey) (int, byte, bool) {
	for _, key := range []struct {
		key  tcell.Key
		code int
		ss3  byte
	}{
		{tcell.KeyLeft, 57417, 't'}, {tcell.KeyRight, 57418, 'v'},
		{tcell.KeyUp, 57419, 'x'}, {tcell.KeyDown, 57420, 'r'},
		{tcell.KeyPgUp, 57421, 'y'}, {tcell.KeyPgDn, 57422, 's'},
		{tcell.KeyHome, 57423, 'w'}, {tcell.KeyEnd, 57424, 'q'},
		{tcell.KeyInsert, 57425, 'p'}, {tcell.KeyDelete, 57426, 'n'}, {tcell.KeyClear, 57427, 'u'},
	} {
		if ev.Key() == key.key {
			return key.code, key.ss3, true
		}
	}
	if ev.Key() == tcell.KeyEnter {
		return 57414, 'M', true
	}
	if ev.Key() != tcell.KeyRune {
		return 0, 0, false
	}
	r := ev.Rune()
	if r >= '0' && r <= '9' {
		return 57399 + int(r-'0'), byte('p' + r - '0'), true
	}
	for _, key := range []struct {
		ch   rune
		code int
		ss3  byte
	}{
		{'.', 57409, 'n'}, {'/', 57410, 'o'}, {'*', 57411, 'j'},
		{'-', 57412, 'm'}, {'+', 57413, 'k'}, {'=', 57415, 'X'}, {',', 57416, 'l'},
	} {
		if r == key.ch {
			return key.code, key.ss3, true
		}
	}
	return 0, 0, false
}

func (app *App) handleFocus(focused bool) {
	app.mu.Lock()
	defer app.mu.Unlock()
	if p := app.active; p != nil {
		p.mu.Lock()
		enabled := !p.dead && p.term.Mode()&vt10x.ModeFocus != 0
		p.mu.Unlock()
		if enabled {
			if focused {
				p.writeInput([]byte("\x1b[I"))
			} else {
				p.writeInput([]byte("\x1b[O"))
			}
		}
	}
}
