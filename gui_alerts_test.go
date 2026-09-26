package main

import (
	"sync"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gio/v2"
)

// alertWin opens a window whose background tab runs cmd, with the window's
// focus and desktop notifications faked.
func alertWin(t *testing.T, cmd string, focused bool, keys map[string]string) (w *testWin, bg *guiTab, sent *[]string) {
	t.Helper()
	w = newTestWin(t, "exec sleep 30", keys)
	var mu sync.Mutex
	var ids []string
	onMain(func() {
		w.focusedFn = func() bool { return focused }
		w.sendFn = func(id string, _ *gio.Notification) {
			mu.Lock()
			ids = append(ids, id)
			mu.Unlock()
		}
		first := w.active
		w.newTab("", []string{"/bin/sh", "-c", cmd})
		bg = w.active
		w.selectTab(first) // the alerting tab is in the background
	})
	return w, bg, &ids
}

func TestGUI_AlertsLightUpBackgroundTabs(t *testing.T) {
	for _, tc := range []struct {
		name, cmd string
		want      tabAttention
	}{
		{"bell", `sleep 0.3; printf '\a'; exec sleep 30`, attentionAlert},
		{"notification", `sleep 0.3; printf '\033]9;Claude is waiting\a'; exec sleep 30`, attentionAlert},
		{"command finished", `printf '\033]133;C\a'; sleep 1.2; printf '\033]133;D;0\a'; exec sleep 30`, attentionDone},
		{"command failed", `printf '\033]133;C\a'; sleep 1.2; printf '\033]133;D;2\a'; exec sleep 30`, attentionFailed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, bg, _ := alertWin(t, tc.cmd, true, map[string]string{"notify.command_seconds": "1"})
			waitMain(t, 5*time.Second, "tab lights up", func() bool { return bg.attention == tc.want })
			onMain(func() {
				if !bg.row.HasCSSClass(attentionClass[tc.want]) || !bg.dot.Visible() {
					t.Error("the tab row does not show the alert")
				}
				w.selectTab(bg)
				if bg.attention != attentionNone || bg.dot.Visible() {
					t.Error("looking at the tab did not clear it")
				}
			})
		})
	}
}

func TestGUI_ShortCommandsAndVisibleTabsStayQuiet(t *testing.T) {
	w, bg, _ := alertWin(t, `printf '\033]133;C\a'; printf '\033]133;D;0\a'; exec sleep 30`, true, nil)
	settle(700 * time.Millisecond)
	onMain(func() {
		if bg.attention != attentionNone {
			t.Errorf("a quick command lit the tab (%d)", bg.attention)
		}
	})
	// The tab in view with the window focused never lights up.
	onMain(func() {
		w.selectTab(bg)
		bg.onAlert(paneAlert{kind: alertBell})
		if bg.attention != attentionNone {
			t.Error("the visible tab lit up")
		}
	})
}

func TestGUI_DesktopNotificationsOnlyWhenUnfocused(t *testing.T) {
	for _, focused := range []bool{true, false} {
		t.Run(map[bool]string{true: "focused", false: "unfocused"}[focused], func(t *testing.T) {
			_, bg, sent := alertWin(t, `sleep 0.3; printf '\033]777;notify;Build;done\a'; exec sleep 30`, focused, nil)
			waitMain(t, 5*time.Second, "alert", func() bool { return bg.attention == attentionAlert })
			settle(100 * time.Millisecond)
			if got := len(*sent); (got > 0) == focused {
				t.Fatalf("focused=%v sent %d notifications", focused, got)
			}
		})
	}
}

func TestGUI_CollapsedTabShowsAlert(t *testing.T) {
	_, bg, _ := alertWin(t, `sleep 0.3; printf '\a'; exec sleep 30`, true, map[string]string{"tabs.collapsed": "true"})
	waitMain(t, 5*time.Second, "alert", func() bool { return bg.attention == attentionAlert })
	onMain(func() {
		if bg.dot.Visible() || !bg.row.HasCSSClass("collapsed") || !bg.row.HasCSSClass("attn-alert") {
			t.Error("collapsed tabs show the alert on the letter, not a dot")
		}
	})
}

func TestAlertBody(t *testing.T) {
	for _, tc := range []struct {
		a    paneAlert
		want string
	}{
		{paneAlert{kind: alertNotify, title: "Claude", body: "Waiting"}, "Claude: Waiting"},
		{paneAlert{kind: alertNotify, body: "Done"}, "Done"},
		{paneAlert{kind: alertDone, command: "make", ran: 125 * time.Second}, "“make” finished after 2 min 5 s"},
		{paneAlert{kind: alertDone, command: "make", ran: 3 * time.Hour, hasExitCode: true, exitCode: 2}, "“make” failed (exit 2) after 3 h"},
		{paneAlert{kind: alertDone}, "The command finished"},
		{paneAlert{kind: alertBell}, "Wants your attention"},
	} {
		if got := alertBody(tc.a); got != tc.want {
			t.Errorf("alertBody(%+v) = %q, want %q", tc.a, got, tc.want)
		}
	}
}

// Without shell integration, the shell getting the terminal back ends a
// command; with it, OSC 133 alone reports.
func TestForegroundChangedFallback(t *testing.T) {
	var got []paneAlert
	p := &Pane{}
	p.setAlertHook(func(_ *Pane, a paneAlert) { got = append(got, a) })
	start := time.Now()
	p.foregroundChanged(false, "make", start)
	p.foregroundChanged(false, "cc", start.Add(time.Second))
	p.foregroundChanged(true, "bash", start.Add(30*time.Second))
	if len(got) != 1 || got[0].command != "make" || got[0].ran != 30*time.Second {
		t.Fatalf("alerts %+v, want make after 30 s", got)
	}
	p.shellMarks = true
	p.foregroundChanged(false, "make", start)
	p.foregroundChanged(true, "bash", start.Add(time.Minute))
	if len(got) != 1 {
		t.Fatal("the fallback fired although OSC 133 reports commands")
	}
}
