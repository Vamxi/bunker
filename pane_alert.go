// pane_alert.go - turning a pane's bells, notifications, and finished
// commands into alerts for the frontend.
package main

import (
	"time"

	"bunker/internal/vt10x"
)

// paneAlertKind is what the frontend shows for an alert.
type paneAlertKind int

const (
	alertBell   paneAlertKind = iota // BEL
	alertNotify                      // OSC 9 / 777 / 99
	alertDone                        // a command finished
)

// paneAlert is one reason a pane wants attention.
type paneAlert struct {
	kind        paneAlertKind
	title, body string        // alertNotify
	command     string        // alertDone: the program that ran, when known
	exitCode    int           // alertDone, when hasExitCode
	hasExitCode bool          // from shell integration (OSC 133;D)
	ran         time.Duration // alertDone: how long the command ran
}

// bellInterval drops bells closer together than this; a key repeat against
// a limit, or tab completion, can ring many times a second.
const bellInterval = time.Second

// setAlertHook installs fn to receive the pane's alerts. It may be called
// while the pane's goroutines run.
func (p *Pane) setAlertHook(fn func(*Pane, paneAlert)) {
	p.alertHook.Store(&fn)
}

func (p *Pane) emitAlert(a paneAlert) {
	if fn := p.alertHook.Load(); fn != nil && *fn != nil {
		(*fn)(p, a)
	}
}

// onTerminalAlert receives vt10x alerts, in stream order, with p.mu held
// (inside captureAndWrite).
func (p *Pane) onTerminalAlert(a vt10x.Alert) {
	now := time.Now()
	switch a.Kind {
	case vt10x.AlertCommandStart:
		p.shellMarks = true
		p.cmdStart = now
	case vt10x.AlertCommandDone:
		p.shellMarks = true
		var ran time.Duration
		if !p.cmdStart.IsZero() {
			ran = now.Sub(p.cmdStart)
		}
		p.cmdStart = time.Time{}
		p.emitAlert(paneAlert{kind: alertDone, command: p.busyName, exitCode: a.ExitCode, hasExitCode: a.HasExitCode, ran: ran})
	case vt10x.AlertBell:
		if now.Sub(p.lastBell) < bellInterval {
			return
		}
		p.lastBell = now
		p.emitAlert(paneAlert{kind: alertBell})
	case vt10x.AlertNotify:
		p.emitAlert(paneAlert{kind: alertNotify, title: a.Title, body: a.Body})
	}
}

// foregroundChanged follows the foreground process for shells without
// shell integration: when the shell gets the terminal back after running
// something, that command finished. It runs on the tracker goroutine.
func (p *Pane) foregroundChanged(atPrompt bool, name string, now time.Time) {
	p.mu.Lock()
	if !atPrompt {
		if p.busySince.IsZero() {
			p.busySince, p.busyName = now, name
		}
		p.mu.Unlock()
		return
	}
	since, command, marks := p.busySince, p.busyName, p.shellMarks
	p.busySince = time.Time{}
	p.mu.Unlock()
	if since.IsZero() || marks {
		return // nothing ran, or OSC 133 reports it exactly
	}
	p.emitAlert(paneAlert{kind: alertDone, command: command, ran: now.Sub(since)})
}
