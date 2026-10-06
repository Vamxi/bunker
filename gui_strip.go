// gui_strip.go - the tab strip's layout: rows stacked along the strip, the
// gaps between them, and a tab being dragged to a new place.
//
// GtkBox can only snap rows into place, so the strip lays them out itself:
// a dragged tab follows the pointer, and the others slide out of its way,
// as in Ptyxis. The rows stay children of gw.strip (a GtkBox, for Append
// and Remove) with stripLayout as its layout manager; their order there is
// only drawing order, so a dragged row is moved last to draw on top.
package main

/*
#cgo pkg-config: gtk4
#include <gtk/gtk.h>
#include <stdint.h>

// Both calls take over the transform. gotk4's Transform wrappers would free
// it again from a finalizer, so this stays in C.
static void bunker_allocate_at(uintptr_t widget, int width, int height, float x, float y) {
	GskTransform *t = gsk_transform_translate(NULL, &GRAPHENE_POINT_INIT(x, y));
	gtk_widget_allocate(GTK_WIDGET((gpointer)widget), width, height, -1, t);
}
*/
import "C"

import (
	"math"
	"slices"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

const (
	stripGap         = 4  // px between tabs in the expanded sidebar
	stripGapTight    = 2  // in the collapsed sidebar and the bar
	stripDragStart   = 8  // px a press moves before it drags a tab
	stripSlideMs     = 60 // time constant of a tab sliding into place
	stripSlideSettle = 0.5
)

// stripLayout places the strip's rows (see the top of this file).
type stripLayout struct {
	gtk.LayoutManager
	gw *guiWin
}

var stripLayoutType = coreglib.RegisterSubclassWithConstructor[*stripLayout](
	func() *stripLayout { return &stripLayout{} },
	coreglib.WithOverrides(func(l *stripLayout) gtk.LayoutManagerOverrides {
		return gtk.LayoutManagerOverrides{
			Measure:  l.measure,
			Allocate: l.allocate,
		}
	}),
)

func newStripLayout(gw *guiWin) *stripLayout {
	l := stripLayoutType.New()
	l.gw = gw
	return l
}

// stripDrag is a tab being dragged along the strip.
type stripDrag struct {
	tab     *guiTab
	pointer float64 // along the strip, in its coordinates
	grab    float64 // where in the tab it was picked up
}

// along is the strip's direction: down a sidebar, across a bar.
func (gw *guiWin) along() gtk.Orientation {
	if gw.sidebar() {
		return gtk.OrientationVertical
	}
	return gtk.OrientationHorizontal
}

// gapBefore is the space between tab i-1 and tab i: none inside a colour
// group outside the collapsed sidebar, so the group's bar runs on.
func (gw *guiWin) gapBefore(i int) float64 {
	switch {
	case i == 0:
		return 0
	case gw.isCollapsed():
		return stripGapTight
	case gw.tabs[i].colour != "" && gw.tabs[i].colour == gw.tabs[i-1].colour:
		return 0
	case gw.sidebar():
		return stripGap
	}
	return stripGapTight
}

func (l *stripLayout) measure(_ gtk.Widgetter, o gtk.Orientation, forSize int) (minimum, natural, _, _ int) {
	gw := l.gw
	if gw == nil {
		return 0, 0, -1, -1
	}
	for i, t := range gw.tabs {
		if o == gw.along() {
			lo, hi, _, _ := t.row.Measure(o, forSize)
			gap := int(gw.gapBefore(i))
			minimum, natural = minimum+lo+gap, natural+hi+gap
		} else {
			lo, hi, _, _ := t.row.Measure(o, -1)
			minimum, natural = max(minimum, lo), max(natural, hi)
		}
	}
	return minimum, natural, -1, -1
}

// lengths sizes the rows along the strip. A sidebar's rows take their
// natural height; a bar's tabs share its width as GtkBox would share it:
// from their minimum towards their natural width, smallest shortfall
// first, and any width left over goes to the tabs that expand.
func (gw *guiWin) lengths(width, height int) []int {
	n := len(gw.tabs)
	lo, hi := make([]int, n), make([]int, n)
	room := width
	for i, t := range gw.tabs {
		if gw.sidebar() {
			_, hi[i], _, _ = t.row.Measure(gtk.OrientationVertical, width)
			lo[i] = hi[i]
		} else {
			lo[i], hi[i], _, _ = t.row.Measure(gtk.OrientationHorizontal, height)
			room -= int(gw.gapBefore(i))
		}
	}
	if gw.sidebar() {
		return hi
	}
	sum := func(xs []int) (s int) {
		for _, x := range xs {
			s += x
		}
		return s
	}
	if spare := room - sum(hi); spare >= 0 {
		var expand []int
		for i, t := range gw.tabs {
			if t.row.ComputeExpand(gtk.OrientationHorizontal) {
				expand = append(expand, i)
			}
		}
		for k, i := range expand {
			hi[i] += spare / len(expand)
			if k < spare%len(expand) {
				hi[i]++
			}
		}
		return hi
	}
	sizes := slices.Clone(lo)
	spare := room - sum(lo)
	order := make([]int, n)
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return (hi[a] - lo[a]) - (hi[b] - lo[b]) })
	for k, i := range order {
		if spare <= 0 {
			break
		}
		give := min(hi[i]-lo[i], spare/(n-k))
		sizes[i] += give
		spare -= give
	}
	return sizes
}

func (l *stripLayout) allocate(_ gtk.Widgetter, width, height, _ int) {
	gw := l.gw
	if gw == nil {
		return
	}
	sizes := gw.lengths(width, height)
	slots := make([]float64, len(gw.tabs))
	end := 0.0
	for i := range gw.tabs {
		end += gw.gapBefore(i)
		slots[i] = end
		end += float64(sizes[i])
	}
	for i, t := range gw.tabs {
		slot, size := slots[i], float64(sizes[i])
		dragged := gw.drag != nil && gw.drag.tab == t
		if gw.reordered && !dragged && t.size > 0 {
			t.slide += t.slot - slot // drawn where it was, then slides over
		}
		t.slot, t.size = slot, size
		if dragged {
			at := min(max(gw.drag.pointer-gw.drag.grab, 0), end-size)
			t.slide = at - slot
		}
		x, y, w, h := 0.0, slot+t.slide, width, sizes[i]
		if !gw.sidebar() {
			x, y, w, h = slot+t.slide, 0, sizes[i], height
		}
		C.bunker_allocate_at(C.uintptr_t(coreglib.BaseObject(t.row).Native()), C.int(w), C.int(h), C.float(x), C.float(y))
	}
	gw.reordered = false
	for _, t := range gw.tabs {
		if t.slide != 0 && (gw.drag == nil || gw.drag.tab != t) {
			gw.slideTabs()
			break
		}
	}
}

// slideTabs runs until every tab has slid into its slot, easing out.
func (gw *guiWin) slideTabs() {
	if gw.sliding {
		return
	}
	gw.sliding = true
	last := time.Now()
	gw.strip.AddTickCallback(func(gtk.Widgetter, gdk.FrameClocker) bool {
		now := time.Now()
		decay := math.Exp(-float64(now.Sub(last).Milliseconds()) / stripSlideMs)
		last = now
		moving := gw.reordered // the layout that starts the slide is still to come
		for _, t := range gw.tabs {
			if gw.drag != nil && gw.drag.tab == t {
				continue // it follows the pointer
			}
			if t.slide *= decay; math.Abs(t.slide) < stripSlideSettle {
				t.slide = 0
			} else {
				moving = true
			}
		}
		gw.strip.QueueAllocate()
		gw.sliding = moving
		return moving
	})
}

// installReorder lets a tab be dragged along the strip. It is a plain
// pointer drag, not GTK drag-and-drop: no drag icon and no drop-target
// highlight, and the tab itself moves.
func (gw *guiWin) installReorder() {
	drag := gtk.NewGestureDrag()
	drag.SetButton(gdk.BUTTON_PRIMARY)
	var picked *guiTab
	var startX, startY float64
	drag.ConnectDragBegin(func(x, y float64) {
		picked, startX, startY = gw.tabAt(x, y), x, y
		if picked != nil && picked.editing {
			picked = nil // the rename entry selects text by dragging
		}
	})
	drag.ConnectDragUpdate(func(dx, dy float64) {
		if picked == nil || picked.closed {
			gw.drag = nil
			return
		}
		pointer, start := startY+dy, startY
		if !gw.sidebar() {
			pointer, start = startX+dx, startX
		}
		if gw.drag == nil {
			if math.Hypot(dx, dy) < stripDragStart {
				return // still a click
			}
			gw.drag = &stripDrag{tab: picked, grab: start - (picked.slot + picked.slide)}
			if last := gw.strip.LastChild(); last != nil && coreglib.BaseObject(last).Native() != coreglib.BaseObject(picked.row).Native() {
				gw.strip.ReorderChildAfter(picked.row, last) // draw it on top
			}
			gw.strip.SetCursorFromName("grabbing")
		}
		gw.drag.pointer = pointer
		center := min(max(pointer-gw.drag.grab, 0), gw.stripEnd()-picked.size) + picked.size/2
		gw.moveTab(picked, gw.dragTarget(picked, center))
		gw.strip.QueueAllocate()
	})
	drag.ConnectDragEnd(func(float64, float64) {
		if gw.drag != nil {
			gw.drag = nil
			gw.strip.SetCursorFromName("")
			gw.syncStripOrder()
			gw.slideTabs() // settle into its slot
		}
		picked = nil
	})
	gw.strip.AddController(drag)
}

// stripEnd is where the last tab ends, from the last layout.
func (gw *guiWin) stripEnd() float64 {
	if len(gw.tabs) == 0 {
		return 0
	}
	last := gw.tabs[len(gw.tabs)-1]
	return last.slot + last.size
}

// tabAt is the tab drawn at (x, y) in the strip, or nil.
func (gw *guiWin) tabAt(x, y float64) *guiTab {
	for _, t := range gw.tabs {
		if b, ok := t.row.ComputeBounds(gw.strip); ok &&
			x >= float64(b.X()) && x < float64(b.X()+b.Width()) &&
			y >= float64(b.Y()) && y < float64(b.Y()+b.Height()) {
			return t
		}
	}
	return nil
}

// dragTarget is the index t takes when its middle is dragged to center:
// after every other tab whose slot's middle it has passed. It counts the
// others only, so it gives the same answer before and after the strip lays
// out a move, and the tab does not flicker between places.
func (gw *guiWin) dragTarget(t *guiTab, center float64) int {
	n := 0
	for _, other := range gw.tabs {
		if other != t && other.slot+other.size/2 < center {
			n++
		}
	}
	return n
}

// moveTab puts t at index to in the tab order. The tabs it passes slide to
// their new places at the next layout.
func (gw *guiWin) moveTab(t *guiTab, to int) {
	from := slices.Index(gw.tabs, t)
	if from < 0 || to == from || to < 0 || to >= len(gw.tabs) {
		return
	}
	gw.tabs = slices.Insert(slices.Delete(gw.tabs, from, from+1), to, t)
	if gw.drag == nil {
		gw.syncStripOrder()
	}
	for _, other := range gw.tabs {
		other.applyRowMode() // numbers follow the order
	}
	gw.groupTags()
	gw.reordered = true
	gw.strip.QueueAllocate()
	gw.slideTabs()
}

// syncStripOrder puts the rows in tab order, which is also the order they
// are drawn and focused in.
func (gw *guiWin) syncStripOrder() {
	var prev gtk.Widgetter
	for _, t := range gw.tabs {
		gw.strip.ReorderChildAfter(t.row, prev)
		prev = t.row
	}
}
