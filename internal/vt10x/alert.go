package vt10x

import (
	"strconv"
	"strings"
)

// AlertKind says why a program wants attention.
type AlertKind int

const (
	// AlertBell is BEL (^G) outside an escape sequence.
	AlertBell AlertKind = iota
	// AlertNotify is a desktop notification request: OSC 9 (iTerm2),
	// OSC 777;notify (urxvt, Ghostty), or OSC 99 (kitty).
	AlertNotify
	// AlertCommandStart is OSC 133;C: the shell is running a command.
	AlertCommandStart
	// AlertCommandDone is OSC 133;D: the command finished.
	AlertCommandDone
)

// Alert is one attention event, reported in stream order.
type Alert struct {
	Kind        AlertKind
	Title, Body string // AlertNotify; Title may be empty
	ExitCode    int    // AlertCommandDone, when HasExitCode
	HasExitCode bool
}

// maxAlertText bounds notification text taken from a program.
const maxAlertText = 256

// WithAlertCallback installs fn, which receives bells, notification
// requests, and shell command boundaries. It runs synchronously inside
// terminal mutation code with the terminal locked; it must be fast and
// non-blocking.
func WithAlertCallback(fn func(Alert)) TerminalOption {
	return func(info *TerminalInfo) {
		info.alertCb = fn
	}
}

func (t *State) alert(a Alert) {
	if t.alertCb != nil {
		t.alertCb(a)
	}
}

// alertText makes program-supplied text safe to show: controls removed,
// length capped.
func alertText(s string) string {
	s = strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) {
			return -1
		}
		return r
	}, s)
	s = strings.TrimSpace(s)
	if len(s) > maxAlertText {
		cut := maxAlertText
		for cut > 0 && !isRuneStart(s[cut]) {
			cut--
		}
		s = s[:cut] + "…"
	}
	return s
}

func isRuneStart(b byte) bool { return b&0xc0 != 0x80 }

// oscAlert handles the OSC numbers that request attention. args are the
// ';'-separated fields, args[0] being the number. It reports whether it
// recognised the sequence.
func (t *State) oscAlert(num int, args []string) bool {
	rest := func(from int) string {
		if len(args) <= from {
			return ""
		}
		return strings.Join(args[from:], ";")
	}
	switch num {
	case 9:
		// ConEmu uses OSC 9 ; <number> ; … for its own commands (9;4 is a
		// progress bar); only free text is a notification.
		if len(args) < 2 || isDigits(args[1]) {
			return len(args) >= 2
		}
		if body := alertText(rest(1)); body != "" {
			t.alert(Alert{Kind: AlertNotify, Body: body})
		}
		return true
	case 777:
		if len(args) >= 3 && args[1] == "notify" {
			title, body := alertText(args[2]), alertText(rest(3))
			if title != "" || body != "" {
				t.alert(Alert{Kind: AlertNotify, Title: title, Body: body})
			}
		}
		return true
	case 99:
		t.kittyNotify(args)
		return true
	case 133:
		if len(args) < 2 || args[1] == "" {
			return true
		}
		switch args[1][0] {
		case 'C':
			t.alert(Alert{Kind: AlertCommandStart})
		case 'D':
			a := Alert{Kind: AlertCommandDone}
			if len(args) >= 3 {
				if code, err := strconv.Atoi(strings.TrimSpace(args[2])); err == nil {
					a.ExitCode, a.HasExitCode = code, true
				}
			}
			t.alert(a)
		}
		return true
	}
	return false
}

// kittyNotify handles OSC 99 ; metadata ; payload. A notification may come
// in chunks (d=0 until the last); p=title or p=body says what the payload
// is. Base64 payloads (e=1) and other extras are not decoded.
func (t *State) kittyNotify(args []string) {
	if len(args) < 3 {
		return
	}
	done, part, encoded := true, "body", false
	for _, kv := range strings.Split(args[1], ":") {
		k, v, _ := strings.Cut(kv, "=")
		switch k {
		case "d":
			done = v != "0"
		case "p":
			part = v
		case "e":
			encoded = v == "1"
		}
	}
	if encoded || (part != "title" && part != "body") {
		return
	}
	text := strings.Join(args[2:], ";")
	if part == "title" {
		t.kittyTitle += text
	} else {
		t.kittyBody += text
	}
	if !done {
		if len(t.kittyTitle)+len(t.kittyBody) > 4*maxAlertText {
			t.kittyTitle, t.kittyBody = "", "" // abandoned chunks do not grow forever
		}
		return
	}
	title, body := alertText(t.kittyTitle), alertText(t.kittyBody)
	t.kittyTitle, t.kittyBody = "", ""
	if title != "" || body != "" {
		t.alert(Alert{Kind: AlertNotify, Title: title, Body: body})
	}
}

func isDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
