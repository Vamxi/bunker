package main

import (
	"bytes"
	"image"
	"image/color"
	"image/png"
	"testing"
	"time"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
)

// keyWithin presses a key and fails if the window does not come back: a
// blocking clipboard read on the GTK thread used to freeze it for good.
func (w *testWin) keyWithin(keyval uint, state gdk.ModifierType, what string) {
	w.t.Helper()
	done := make(chan struct{})
	go func() {
		w.key(keyval, state)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		w.t.Fatalf("%s froze the window", what)
	}
}

// bunker owns the clipboard after its own copy; bunk's Ctrl+V, the search
// bar, and image pastes must still read it without blocking the window.
func TestGUI_PasteWhatBunkerCopied(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir()) // pasted images land here
	w := newTestWin(t, "exec cat", nil)
	p := w.panes()[0]

	t.Run("text into the pane", func(t *testing.T) {
		onMain(func() { w.active.view.Clipboard().SetText("clip-from-bunker") })
		w.keyWithin(gdk.KEY_v, ctrl, "Ctrl+V")
		w.waitDrawn(p, "clip-from-bunker")
	})

	t.Run("text into the search bar", func(t *testing.T) {
		onMain(func() { w.active.view.Clipboard().SetText("needle") })
		w.key(gdk.KEY_f, ctrl)
		w.keyWithin(gdk.KEY_v, ctrl, "Ctrl+V in search")
		waitMain(t, 3*time.Second, "query pasted", func() bool { return w.activeApp().searchQuery == "needle" })
		w.key(gdk.KEY_Escape, 0)
	})

	t.Run("image as a file path", func(t *testing.T) {
		img := image.NewNRGBA(image.Rect(0, 0, 2, 2))
		img.Set(0, 0, color.NRGBA{R: 255, A: 255})
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			t.Fatal(err)
		}
		onMain(func() {
			tex, err := gdk.NewTextureFromBytes(glib.NewBytes(buf.Bytes()))
			if err != nil {
				t.Errorf("texture: %v", err)
				return
			}
			w.active.view.Clipboard().SetTexture(tex)
		})
		w.keyWithin(gdk.KEY_v, ctrl, "Ctrl+V with an image")
		w.waitDrawn(p, "bunk-paste-")
	})
}

// Alt+F1 resolves the pane's context in the background, then splits on the
// GTK thread.
func TestGUI_ContextSplitIsAsync(t *testing.T) {
	w := newTestWin(t, "exec sleep 30", nil)
	w.keyWithin(gdk.KEY_F1, alt, "Alt+F1")
	w.waitPanes(2)
}
