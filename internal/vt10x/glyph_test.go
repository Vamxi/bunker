package vt10x

import (
	"testing"
	"unsafe"
)

// Scrollback stores rows of Glyphs; growing the struct costs memory and
// scroll throughput across every cell.
func TestGlyphSize(t *testing.T) {
	if got := unsafe.Sizeof(Glyph{}); got != 32 {
		t.Fatalf("Glyph is %d bytes, want 32", got)
	}
}

func TestGlyphExtIsCopyOnWrite(t *testing.T) {
	var a Glyph
	a.SetCombining("́")
	b := a
	b.SetCombining(b.Combining() + "̈")
	if a.Combining() != "́" {
		t.Fatalf("copy shares mutable extra data: a = %q", a.Combining())
	}
	img := &ImageCell{}
	b.SetImage(img)
	if b.Image() != img || b.Combining() != "́̈" || a.Image() != nil {
		t.Fatal("SetImage lost combining data or leaked into copy")
	}
	b.SetImage(nil)
	b.SetCombining("")
	if b.ext != nil {
		t.Fatal("empty extras should release the allocation")
	}
}
