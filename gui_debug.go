// gui_debug.go - offscreen screenshots for manual and scripted checks.
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

static gboolean bunker_screenshot(uintptr_t widget, const char *path) {
	GtkWidget *w = GTK_WIDGET((gpointer)widget);
	GdkPaintable *p = gtk_widget_paintable_new(w);
	GtkSnapshot *s = gtk_snapshot_new();
	gdk_paintable_snapshot(p, s, gtk_widget_get_width(w), gtk_widget_get_height(w));
	GskRenderNode *n = gtk_snapshot_free_to_node(s);
	gboolean ok = FALSE;
	if (n != NULL) {
		GskRenderer *r = gtk_native_get_renderer(gtk_widget_get_native(w));
		GdkTexture *t = gsk_renderer_render_texture(r, n, NULL);
		ok = gdk_texture_save_to_png(t, path);
		g_object_unref(t);
		gsk_render_node_unref(n);
	}
	g_object_unref(p);
	return ok;
}
*/
import "C"

import (
	"os"
	"runtime/pprof"
	"time"
	"unsafe"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

func guiScheduleScreenshot(win *gtk.ApplicationWindow) {
	path := os.Getenv("BUNKER_SCREENSHOT")
	if path == "" {
		return
	}
	delay := 1500 * time.Millisecond
	if d, err := time.ParseDuration(os.Getenv("BUNKER_SCREENSHOT_DELAY")); err == nil {
		delay = d
	}
	coreglib.TimeoutAdd(uint(delay.Milliseconds()), func() bool {
		defer win.Close()
		cpath := C.CString(path)
		defer C.free(unsafe.Pointer(cpath))
		widget := C.uintptr_t(coreglib.InternObject(win).Native())
		if C.bunker_screenshot(widget, cpath) == 0 {
			L.Error("screenshot: failed", "path", path)
		}
		return false
	})
}

// guiStartProfile writes a CPU profile to $BUNKER_CPUPROFILE until the
// returned stop function runs.
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
