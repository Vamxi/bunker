// gui_debug.go - offscreen screenshots for manual and scripted checks.
//
// BUNKER_TABS (one command per line) opens one extra tab per command (run with
// sh -c), for screenshots of the tab strip.
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
	"os"
	"runtime/pprof"
	"strings"
	"time"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
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
	cpath := C.CString(path) // freed after the last attempt
	attempts := 0
	// attempt returns true to be retried: step 1 means GTK has no frame for
	// the window yet.
	attempt := func() bool {
		win := gtk.BaseWidget(target())
		code := C.bunker_screenshot(C.uintptr_t(coreglib.InternObject(win).Native()), cpath)
		if code == 1 && attempts < 20 {
			attempts++
			win.QueueDraw()
			return true
		}
		if code != 0 {
			L.Error("screenshot: failed", "path", path, "step", int(code))
		}
		C.free(unsafe.Pointer(cpath))
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
	if err := pprof.StartCPUProfile(f); err != nil {
		f.Close() //nolint:errcheck
		return func() {}
	}
	return func() {
		pprof.StopCPUProfile()
		f.Close() //nolint:errcheck
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
