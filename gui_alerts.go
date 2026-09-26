// gui_alerts.go - tabs that want attention.
//
// A pane's alerts (pane_alert.go) reach its tab here, on the GTK thread. A
// tab you are not looking at lights up: a dot in the sidebar or bar, the
// letter itself in the collapsed sidebar, coloured from the theme's palette
// (green: a command finished, red: it failed, yellow: a bell or a
// notification). Looking at the tab clears it. While the window is in the
// background ([notify] desktop), the alert is also a desktop notification;
// clicking it brings the window up on that tab.
package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// tabAttention orders how urgent a tab's unseen alert is; a tab shows the
// most urgent one until it is looked at.
type tabAttention int

const (
	attentionNone tabAttention = iota
	attentionDone
	attentionAlert
	attentionFailed
)

var attentionClass = map[tabAttention]string{
	attentionDone:   "attn-done",
	attentionAlert:  "attn-alert",
	attentionFailed: "attn-failed",
}

// notifyInterval is the least time between desktop notifications from one
// tab.
const notifyInterval = 3 * time.Second

func init() {
	// bunker runs each window as its own process without a D-Bus name, so
	// GNOME's org.gtk.Notifications could not call back when a notification
	// is clicked. The freedesktop backend delivers the click in-process.
	if os.Getenv("GNOTIFICATION_BACKEND") == "" {
		os.Setenv("GNOTIFICATION_BACKEND", "freedesktop") //nolint:errcheck // cannot fail for a valid name
	}
}

// windowFocused reports whether the user is looking at this window.
func (gw *guiWin) windowFocused() bool {
	if gw.focusedFn != nil {
		return gw.focusedFn()
	}
	return gw.win.IsActive()
}

// onAlert handles one alert for t, on the GTK thread.
func (t *guiTab) onAlert(a paneAlert) {
	if t.closed {
		return
	}
	n := t.gw.cfg.Notify
	level := attentionAlert
	switch a.kind {
	case alertDone:
		if n.CommandSeconds == 0 || a.ran < time.Duration(n.CommandSeconds)*time.Second {
			return
		}
		level = attentionDone
		if a.hasExitCode && a.exitCode != 0 {
			level = attentionFailed
		}
	case alertBell:
		if !n.Bell {
			return
		}
	}
	focused := t.gw.windowFocused()
	if t == t.gw.active && focused {
		return // in view
	}
	if level > t.attention {
		t.setAttention(level)
	}
	if n.Desktop == "always" || (n.Desktop == "unfocused" && !focused) {
		t.gw.notifyDesktop(t, a)
	}
}

func (t *guiTab) setAttention(level tabAttention) {
	for _, c := range attentionClass {
		t.row.RemoveCSSClass(c)
	}
	t.row.RemoveCSSClass("attn")
	t.attention = level
	if c := attentionClass[level]; c != "" {
		t.row.AddCSSClass(c)
		t.row.AddCSSClass("attn")
	}
	t.applyRowMode()
}

// seen clears t's alert once the user looks at it.
func (t *guiTab) seen() {
	if t.attention != attentionNone {
		t.setAttention(attentionNone)
	}
	t.gw.withdrawNotification(t)
}

func (gw *guiWin) notifyDesktop(t *guiTab, a paneAlert) {
	now := time.Now()
	if now.Sub(t.lastNotified) < notifyInterval {
		return
	}
	t.lastNotified = now
	n := gio.NewNotification(t.title.Text())
	n.SetBody(alertBody(a))
	n.SetDefaultAction("app.focus-tab::" + strconv.Itoa(t.id))
	if gw.sendFn != nil {
		gw.sendFn(t.notificationID(), n)
		return
	}
	gw.gapp.SendNotification(t.notificationID(), n)
}

func (gw *guiWin) withdrawNotification(t *guiTab) {
	if t.lastNotified.IsZero() {
		return
	}
	t.lastNotified = time.Time{}
	if gw.sendFn == nil {
		gw.gapp.WithdrawNotification(t.notificationID())
	}
}

func (t *guiTab) notificationID() string {
	return fmt.Sprintf("tab-%d-%d", os.Getpid(), t.id)
}

// alertBody says what happened, for a notification.
func alertBody(a paneAlert) string {
	switch a.kind {
	case alertNotify:
		switch {
		case a.title != "" && a.body != "":
			return a.title + ": " + a.body
		case a.title != "":
			return a.title
		}
		return a.body
	case alertDone:
		what := "The command"
		if a.command != "" {
			what = "“" + a.command + "”"
		}
		verb := "finished"
		if a.hasExitCode && a.exitCode != 0 {
			verb = fmt.Sprintf("failed (exit %d)", a.exitCode)
		}
		if a.ran > 0 {
			return fmt.Sprintf("%s %s after %s", what, verb, humanDuration(a.ran))
		}
		return what + " " + verb
	}
	return "Wants your attention"
}

// humanDuration spells d like "45 s", "2 min 5 s", "1 h 3 min".
func humanDuration(d time.Duration) string {
	d = d.Round(time.Second)
	h, m, s := int(d.Hours()), int(d.Minutes())%60, int(d.Seconds())%60
	var parts []string
	if h > 0 {
		parts = append(parts, fmt.Sprintf("%d h", h))
	}
	if m > 0 {
		parts = append(parts, fmt.Sprintf("%d min", m))
	}
	if s > 0 && h == 0 {
		parts = append(parts, fmt.Sprintf("%d s", s))
	}
	if len(parts) == 0 {
		return "0 s"
	}
	return strings.Join(parts, " ")
}

// installFocusTab adds app.focus-tab, which a clicked notification runs.
func (gw *guiWin) installFocusTab() {
	a := gio.NewSimpleAction("focus-tab", glib.NewVariantType("s"))
	a.ConnectActivate(func(param *glib.Variant) {
		defer guiRecover("focus-tab")
		id, err := strconv.Atoi(param.String())
		if err != nil {
			return
		}
		for _, t := range gw.tabs {
			if t.id == id {
				gw.win.Present()
				gw.selectTab(t)
				return
			}
		}
	})
	gw.gapp.AddAction(a)
	// Coming back to the window counts as looking at its tab.
	gw.win.NotifyProperty("is-active", func() {
		if gw.win.IsActive() && gw.active != nil {
			gw.active.seen()
		}
	})
}
