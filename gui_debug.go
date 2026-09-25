// gui_debug.go - offscreen screenshots for manual and scripted checks.
//
// BUNKER_TABS (one command per line) opens one extra tab per command (run with
// sh -c), for screenshots of the tab strip.
//
// BUNKER_KEYS="f1,alt+left,ctrl+f" sends bunk key presses to the first tab,
// 300ms apart, after startup (key names as in [keys]).
//
// BUNKER_OPEN=tab-menu opens the active tab's right-click menu and a
// screenshot captures the menu.
//
// BUNKER_OPEN=preferences[/page] opens the Preferences window (optionally on
// a page: appearance, tabs, terminal, advanced) at startup, and a
// screenshot then captures it instead of the terminal.
//
// BUNKER_SCREENSHOT=out.png renders the window through its own GSK renderer
// (the same GPU path as on screen) after BUNKER_SCREENSHOT_DELAY (default
// 1.5s), writes a PNG, and quits. Combine with `-- command` to capture a
// program. Implemented in C because gotk4 mishandles GskRenderNode refcounts.
package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdint.h>
#include <stdlib.h>

// Returns 0 on success, else the failing step: 1 no frame, 2 no renderer,
// 3 render failed, 4 save failed.
static int bunker_screenshot(uintptr_t widget, const char *path) {
	GtkWidget *w = GTK_WIDGET((gpointer)widget);
	GdkPaintable *p = gtk_widget_paintable_new(w);
	GtkSnapshot *s = gtk_snapshot_new();
	gdk_paintable_snapshot(p, s, gtk_widget_get_width(w), gtk_widget_get_height(w));
	GskRenderNode *n = gtk_snapshot_free_to_node(s);
	g_object_unref(p);
	if (n == NULL)
		return 1;
	GtkNative *native = gtk_widget_get_native(w);
	GskRenderer *r = native ? gtk_native_get_renderer(native) : NULL;
	if (r == NULL) {
		gsk_render_node_unref(n);
		return 2;
	}
	GdkTexture *t = gsk_renderer_render_texture(r, n, NULL);
	gsk_render_node_unref(n);
	if (t == NULL)
		return 3;
	gboolean ok = gdk_texture_save_to_png(t, path);
	g_object_unref(t);
	return ok ? 0 : 4;
}
*/
import "C"

import (
	"errors"
	"os"
	"runtime/pprof"
	"strings"
	"time"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/gdamore/tcell/v2"
)

// guiScheduleScreenshot captures target() (the main window unless a debug
// hook opened another) and then closes main.
func guiScheduleScreenshot(main *gtk.ApplicationWindow, target func() gtk.Widgetter) {
	path := os.Getenv("BUNKER_SCREENSHOT")
	if path == "" {
		return
	}
	delay := 1500 * time.Millisecond
	if d, err := time.ParseDuration(os.Getenv("BUNKER_SCREENSHOT_DELAY")); err == nil {
		delay = d
	}
	attempts := 0
	// attempt returns true to be retried while GTK has no frame yet.
	attempt := func() bool {
		win := target()
		err := guiRenderPNG(win, path)
		if errors.Is(err, errNoFrame) && attempts < 20 {
			attempts++
			gtk.BaseWidget(win).QueueDraw()
			return true
		}
		if err != nil {
			L.Error("screenshot: failed", "path", path, "err", err)
		}
		main.Close()
		return false
	}
	coreglib.TimeoutAdd(uint(delay.Milliseconds()), func() bool {
		if attempt() {
			coreglib.TimeoutAdd(150, attempt)
		}
		return false
	})
}

// errNoFrame means the widget has not been drawn yet.
var errNoFrame = errors.New("no frame yet")

// guiRenderPNG renders widget w (and its children) through its own GSK
// renderer into a PNG at path. Used by BUNKER_SCREENSHOT and the GUI tests.
func guiRenderPNG(w gtk.Widgetter, path string) error {
	cpath := C.CString(path)
	defer C.free(unsafe.Pointer(cpath))
	switch C.bunker_screenshot(C.uintptr_t(coreglib.BaseObject(w).Native()), cpath) {
	case 0:
		return nil
	case 1:
		return errNoFrame
	case 2:
		return errors.New("no renderer")
	case 3:
		return errors.New("render failed")
	default:
		return errors.New("saving PNG failed")
	}
}

func guiStartProfile() func() {
	path := os.Getenv("BUNKER_CPUPROFILE")
	if path == "" {
		return func() {}
	}
	f, err := os.Create(path)
	if err != nil {
		L.Error("cpuprofile: create", "err", err)
		return func() {}
	}
	closeFile := func() {
		if err := f.Close(); err != nil {
			L.Error("cpuprofile: close", "err", err)
		}
	}
	if err := pprof.StartCPUProfile(f); err != nil {
		L.Error("cpuprofile: start", "err", err)
		closeFile()
		return func() {}
	}
	return func() {
		pprof.StopCPUProfile()
		closeFile()
	}
}

func guiDebugExtraTabs() [][]string {
	var cmds [][]string
	for _, c := range strings.Split(os.Getenv("BUNKER_TABS"), "\n") {
		if c = strings.TrimSpace(c); c != "" {
			cmds = append(cmds, []string{"/bin/sh", "-c", c})
		}
	}
	return cmds
}

func (gw *guiWin) runDebugKeys() {
	spec := os.Getenv("BUNKER_KEYS")
	if spec == "" || len(gw.tabs) == 0 {
		return
	}
	t := gw.tabs[0]
	keys := strings.Split(spec, ",")
	i := 0
	coreglib.TimeoutAdd(600, func() bool {
		if i >= len(keys) || t.closed {
			return false
		}
		kb, err := parseKey(keys[i])
		i++
		if err != nil {
			L.Error("debug keys", "err", err)
			return true
		}
		if !t.app.handleKey(tcell.NewEventKey(kb.key, kb.r, kb.mod)) {
			gw.win.Close()
		}
		return true
	})
}

// runDebugHooks starts whatever BUNKER_TABS, BUNKER_OPEN, BUNKER_KEYS, and
// BUNKER_SCREENSHOT ask for (see the top of this file). Nothing runs when
// they are unset.
func (gw *guiWin) runDebugHooks() {
	// Extra tabs open once the first tab has a size.
	if extras := guiDebugExtraTabs(); len(extras) > 0 {
		coreglib.TimeoutAdd(300, func() bool {
			for _, extra := range extras {
				gw.newTab("", extra)
			}
			return false
		})
	}
	open := os.Getenv("BUNKER_OPEN")
	switch {
	case strings.HasPrefix(open, "preferences"):
		gw.openSettings()
		if _, page, ok := strings.Cut(open, "/"); ok {
			gw.settings.stack.SetVisibleChildName(page)
		}
	case open == "tab-menu":
		coreglib.TimeoutAdd(800, func() bool {
			if gw.active != nil {
				gw.active.showMenu(40, 12)
			}
			return false
		})
	case open == "tab-rename": // choose Rename… the way a click does: hide, then activate
		coreglib.TimeoutAdd(800, func() bool {
			if t := gw.active; t != nil {
				t.showMenu(40, 12)
				coreglib.TimeoutAdd(200, func() bool {
					t.menu.Popdown()
					t.row.ActivateAction("tab.rename", nil)
					return false
				})
			}
			return false
		})
	}
	guiScheduleScreenshot(gw.win, func() gtk.Widgetter {
		if gw.settings != nil && gw.settings.win.IsVisible() {
			return gw.settings.win
		}
		if gw.active != nil && gw.active.menu != nil && gw.active.menu.IsVisible() {
			return gw.active.menu
		}
		return gw.win
	})
	gw.runDebugKeys()
}
