package main

// GUI test harness.
//
// GTK may only be used from the thread that initialised it, while Go runs
// tests on arbitrary threads. TestMain therefore keeps the process's main
// thread for GTK: it initialises GTK, runs the test binary on another
// goroutine, and meanwhile executes queued GTK work (onMain) and iterates the
// GLib main loop, so frame clocks, idles, timeouts, and file monitors all
// run as in the real app.
//
// Without a display (or with BUNKER_NO_GUI_TESTS=1) GTK is never touched and
// GUI tests skip; every other test runs normally.

import (
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

// guiQueue carries work to the GTK thread; nil when GUI tests are off.
var guiQueue chan func()

func TestMain(m *testing.M) {
	for _, k := range []string{"BUNKER_SCREENSHOT", "BUNKER_OPEN", "BUNKER_TABS", "BUNKER_KEYS", "BUNKER_FONT", "BUNKER_CPUPROFILE"} {
		os.Unsetenv(k) //nolint:errcheck // debug hooks must not fire in tests
	}
	noDisplay := os.Getenv("WAYLAND_DISPLAY") == "" && os.Getenv("DISPLAY") == ""
	if noDisplay || os.Getenv("BUNKER_NO_GUI_TESTS") != "" {
		os.Exit(m.Run())
	}
	runtime.LockOSThread()
	if !gtk.InitCheck() {
		os.Exit(m.Run())
	}
	if os.Getenv("BUNKER_TEST_LOG") == "" {
		L = slog.New(slog.NewTextHandler(io.Discard, nil)) // GTK warnings would flood test output
		slog.SetDefault(L)                                 // gotk4 routes GLib logs through slog.Default
	}
	guiQueue = make(chan func())
	exit := make(chan int)
	go func() { exit <- m.Run() }()
	ctx := glib.MainContextDefault()
	for {
		select {
		case f := <-guiQueue:
			f()
		case code := <-exit:
			os.Exit(code)
		default:
			if !ctx.Iteration(false) {
				time.Sleep(time.Millisecond)
			}
		}
	}
}

func needGUI(t testing.TB) {
	t.Helper()
	if guiQueue == nil {
		t.Skip("no display (set WAYLAND_DISPLAY or DISPLAY); GUI test skipped")
	}
}

// onMain runs f on the GTK thread and waits for it.
func onMain(f func()) {
	done := make(chan struct{})
	guiQueue <- func() {
		defer close(done)
		f()
	}
	<-done
}

// waitMain polls cond on the GTK thread (the main loop keeps running in
// between) until it holds, or fails the test after timeout.
func waitMain(t testing.TB, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		var ok bool
		onMain(func() { ok = cond() })
		if ok {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out after %v waiting for %s", timeout, what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// settle lets the main loop run (layout, frames, idles) for d.
func settle(d time.Duration) { time.Sleep(d) }

var testAppSeq atomic.Int32

// testWin is a real bunker window driven from a test.
type testWin struct {
	*guiWin
	t    testing.TB
	path string
}

// newTestWin opens a bunker window whose first tab runs cmd under /bin/sh.
// keys edits the documented default config ("section.key" → TOML literal,
// section empty for top level) before loading it.
func newTestWin(t testing.TB, cmd string, keys map[string]string) *testWin {
	t.Helper()
	needGUI(t)
	t.Setenv("SHELL", "/bin/sh") // split panes start a fast, predictable shell
	path := filepath.Join(t.TempDir(), "config.toml")
	text := DefaultConfigTOML()
	for k, lit := range keys {
		section, key, found := strings.Cut(k, ".")
		if !found {
			section, key = "", k
		}
		text = setTOMLKey(text, section, key, lit)
	}
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	cfg = guiConfig(cfg)

	var gw *guiWin
	onMain(func() {
		gapp := gtk.NewApplication(fmt.Sprintf("dev.bunker.Test%d", testAppSeq.Add(1)), gio.ApplicationNonUnique)
		if err := gapp.Register(context.Background()); err != nil {
			t.Errorf("register: %v", err)
			return
		}
		gw = &guiWin{gapp: gapp, cfg: cfg, configPath: path, collapsed: cfg.Tabs.Collapsed}
		gw.build([]string{"/bin/sh", "-c", cmd})
	})
	if gw == nil {
		t.FailNow()
	}
	tw := &testWin{guiWin: gw, t: t, path: path}
	t.Cleanup(func() {
		onMain(func() {
			gw.closeAllTabs()
			gw.win.Destroy()
		})
		settle(50 * time.Millisecond)
	})
	tw.waitPanes(1)
	return tw
}

// activeApp is the active tab's pane model (GTK thread).
func (w *testWin) activeApp() *App { return w.active.app }

// panes lists the active tab's panes; safe from the test goroutine.
func (w *testWin) panes() []*Pane {
	var app *App
	onMain(func() { app = w.activeApp() })
	app.mu.Lock()
	defer app.mu.Unlock()
	if app.root == nil {
		return nil
	}
	var out []*Pane
	for _, leaf := range app.root.leaves() {
		out = append(out, leaf.pane)
	}
	return out
}

func (w *testWin) waitPanes(n int) {
	w.t.Helper()
	waitMain(w.t, 5*time.Second, fmt.Sprintf("%d pane(s)", n), func() bool {
		app := w.activeApp()
		app.mu.Lock()
		defer app.mu.Unlock()
		return app.root != nil && len(app.root.leaves()) == n
	})
}

// waitDrawn waits until pane p has been drawn by the view with text in it:
// that proves capture and snapshot ran, not just the emulator.
func (w *testWin) waitDrawn(p *Pane, text string) {
	w.t.Helper()
	waitMain(w.t, 5*time.Second, fmt.Sprintf("%q drawn", text), func() bool {
		f := w.active.view.frames[p]
		return f != nil && f.valid && strings.Contains(frameText(f), text)
	})
}

// key sends a key press through the view's real key handler.
func (w *testWin) key(keyval uint, state gdk.ModifierType) {
	onMain(func() { w.active.view.keyPressed(keyval, 0, state) })
	settle(30 * time.Millisecond)
}

// typeText types s as individual key presses.
func (w *testWin) typeText(s string) {
	for _, r := range s {
		w.key(uint(r), 0)
	}
}

// screenshot renders the window to a PNG and decodes it.
func (w *testWin) screenshot() image.Image {
	w.t.Helper()
	path := filepath.Join(w.t.TempDir(), "shot.png")
	var err error
	for range 20 {
		onMain(func() { err = guiRenderPNG(w.win, path) })
		if err != errNoFrame {
			break
		}
		settle(50 * time.Millisecond)
	}
	if err != nil {
		w.t.Fatalf("render: %v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		w.t.Fatal(err)
	}
	defer f.Close() //nolint:errcheck
	img, err := png.Decode(f)
	if err != nil {
		w.t.Fatal(err)
	}
	return img
}

// frameText is a frame's text, rows joined by newlines.
func frameText(f *paneFrame) string {
	var b strings.Builder
	for _, row := range f.grid {
		for _, g := range row {
			if g.Width >= 0 {
				b.WriteString(g.Text())
			}
		}
		b.WriteByte('\n')
	}
	return b.String()
}

// sameRGB compares a pixel to a theme colour.
func sameRGB(c color.Color, want interface{ RGB() (int32, int32, int32) }) bool {
	r, g, b, _ := c.RGBA()
	wr, wg, wb := want.RGB()
	return int32(r>>8) == wr && int32(g>>8) == wg && int32(b>>8) == wb
}
