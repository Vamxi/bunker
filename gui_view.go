// gui_view.go - GTK4 terminal widget.
//
// termView is a GtkWidget subclass that paints one Pane's vt10x grid through
// GtkSnapshot. Every cell becomes GSK colour and text render nodes, which GTK
// rasterises on the GPU (Vulkan/GL) with its own glyph cache.
//
// Frame pipeline:
//
//	readPTY → app.redraw → redrawBridge → glib idle → QueueDraw
//	GTK frame clock → snapshot(): copy visible rows under Pane.mu, release,
//	then emit colour and text nodes row by row.
//
// Pane.mu is held only for the grid copy; drawing happens after release.
package main

import (
	"math"
	"strings"
	"sync/atomic"
	"time"

	"bunker/internal/vt10x"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/graphene"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/gdamore/tcell/v2"
)

const (
	guiDefaultCols   = 100
	guiDefaultRows   = 30
	guiReflowDelay   = 50 * time.Millisecond
	guiLayoutCacheMx = 8192
)

// Font variants index the four faces a cell can be drawn with.
const (
	fontRegular = iota
	fontBold
	fontItalic
	fontBoldItalic
)

type layoutKey struct {
	text    string
	variant uint8
}

type layoutEntry struct {
	layout   *pango.Layout
	baseline float64 // layout baseline in pixels from its top edge
}

type termView struct {
	gtk.Widget

	app   *App
	theme resolvedTheme
	win   *gtk.ApplicationWindow

	pad      float64 // pixels around the grid ([window] padding)
	fontBase string
	fontSize float64 // points; 0 = size from fontBase
	fonts    [4]*pango.FontDescription

	// Cell metrics in logical pixels.
	cellW, cellH   float64
	baseline       float64
	ulPos, ulThick float64
	stPos, stThick float64

	cols, rows  int
	reflowTimer *time.Timer

	layouts map[layoutKey]*layoutEntry
	frames  map[*Pane]*paneFrame // last captured state of each visible pane

	focused bool
	title   string
	queued  atomic.Bool
	onTitle func(string)
	onSpawn func(cols, rows int) (*Pane, error)
	rect    *graphene.Rect
	point   *graphene.Point

	mouseBtn  tcell.ButtonMask
	mouseCell [2]int
	scrollAcc float64
}

var termViewType = coreglib.RegisterSubclassWithConstructor[*termView](
	func() *termView { return &termView{} },
	coreglib.WithOverrides(func(v *termView) gtk.WidgetOverrides {
		return gtk.WidgetOverrides{
			Snapshot:     v.snapshot,
			SizeAllocate: v.sizeAllocate,
			Measure:      v.measure,
		}
	}),
)

func newTermView(app *App, cfg Config) *termView {
	v := termViewType.New()
	v.app = app
	v.theme = cfg.Theme
	v.pad = float64(cfg.Padding)
	v.fontBase = cfg.Font
	v.layouts = make(map[layoutKey]*layoutEntry)
	v.frames = make(map[*Pane]*paneFrame)
	// bunk sizes a zoomed pane to the whole grid; see sizeAllocate for the
	// extra column.
	app.sizeFn = func() (int, int) { return v.cols + 1, v.rows }
	v.rect = graphene.RectAlloc()
	v.point = graphene.NewPointAlloc()
	v.SetFocusable(true)
	v.SetFocusOnClick(true)
	v.SetHExpand(true)
	v.SetVExpand(true)
	v.SetCursorFromName("text")
	v.setFont(v.fontBase, 0)
	v.installInput()
	return v
}

// applyConfig applies a reloaded config to a live view. Only what changed
// is touched, so reloading an unchanged file (or our own write) is free and
// keeps the current font zoom.
func (v *termView) applyConfig(cfg Config) {
	relayout := false
	if cfg.Font != v.fontBase {
		v.fontBase = cfg.Font
		v.setFont(cfg.Font, 0)
		relayout = true
	}
	if pad := float64(cfg.Padding); pad != v.pad {
		v.pad = pad
		relayout = true
	}
	v.theme = cfg.Theme // layouts are colour-independent, nothing to flush
	if relayout {
		v.cols, v.rows = 0, 0 // force sizeAllocate to recompute the grid
		v.QueueResize()
	}
	v.QueueDraw()
}

// setFont resolves the four font faces and recomputes cell metrics.
func (v *termView) setFont(base string, size float64) {
	desc := pango.FontDescriptionFromString(base)
	if size > 0 {
		desc.SetSize(int(size * pango.SCALE))
	}
	v.fontSize = float64(desc.Size()) / pango.SCALE
	for i := range v.fonts {
		d := desc.Copy()
		if i == fontBold || i == fontBoldItalic {
			d.SetWeight(pango.WeightBold)
		}
		if i == fontItalic || i == fontBoldItalic {
			d.SetStyle(pango.StyleItalic)
		}
		v.fonts[i] = d
	}

	ctx := v.PangoContext()
	m := ctx.Metrics(v.fonts[fontRegular], nil)
	l := pango.NewLayout(ctx)
	l.SetFontDescription(v.fonts[fontRegular])
	l.SetText(strings.Repeat("M", 100))
	_, logical := l.Extents()
	v.cellW = float64(logical.Width()) / pango.SCALE / 100
	v.cellH = math.Ceil(float64(logical.Height()) / pango.SCALE)
	v.baseline = float64(l.Baseline()) / pango.SCALE
	v.ulPos = v.baseline - float64(m.UnderlinePosition())/pango.SCALE
	v.ulThick = max(1, math.Round(float64(m.UnderlineThickness())/pango.SCALE))
	v.stPos = v.baseline - float64(m.StrikethroughPosition())/pango.SCALE
	v.stThick = max(1, math.Round(float64(m.StrikethroughThickness())/pango.SCALE))
	if v.cellW < 1 {
		v.cellW = 8
	}
	if v.cellH < 1 {
		v.cellH = 16
	}
	clear(v.layouts)
	if v.app != nil { // bunk picks the split direction in pixels
		v.app.mu.Lock()
		v.app.cellAspect = v.cellH / v.cellW
		v.app.mu.Unlock()
	}
	L.Debug("gui: font metrics", "font", base, "size", v.fontSize, "cellW", v.cellW, "cellH", v.cellH)
}

// zoomFont changes the font size by delta points (0 resets).
func (v *termView) zoomFont(delta float64) {
	size := 0.0
	if delta != 0 {
		size = min(max(v.fontSize+delta, 5), 72)
	}
	v.setFont(v.fontBase, size)
	v.QueueResize()
	v.QueueDraw()
}

func (v *termView) measure(orientation gtk.Orientation, _ int) (minimum, natural, minBase, natBase int) {
	if orientation == gtk.OrientationHorizontal {
		return int(4*v.cellW + 2*v.pad), int(guiDefaultCols*v.cellW + 2*v.pad), -1, -1
	}
	return int(2*v.cellH + 2*v.pad), int(guiDefaultRows*v.cellH + 2*v.pad), -1, -1
}

// sizeAllocate recomputes the grid and resizes the PTY. The shell gets
// SIGWINCH immediately; primary-screen reflow is debounced like bunk's.
func (v *termView) sizeAllocate(width, height, _ int) {
	defer guiRecover("sizeAllocate")
	cols := max(2, int((float64(width)-2*v.pad)/v.cellW))
	rows := max(1, int((float64(height)-2*v.pad)/v.cellH))
	if cols == v.cols && rows == v.rows {
		return
	}
	v.cols, v.rows = cols, rows

	app := v.app
	app.mu.Lock()
	p := app.active
	app.mu.Unlock()
	if p == nil {
		if v.onSpawn != nil {
			if _, err := v.onSpawn(cols, rows); err != nil {
				L.Error("gui: spawn failed", "err", err)
			}
		}
		return
	}

	// Panes reserve their rightmost column for the scrollbar. The tree gets
	// one extra column so the rightmost pane's lands in the right padding
	// instead of costing a text column.
	app.mu.Lock()
	if app.root != nil {
		app.root.resizePTYOnly(0, 0, cols+1, rows)
	}
	if z := app.zoomedPane; z != nil {
		app.zoomGeom = [4]int{z.x, z.y, z.w, z.h}
		z.resizePTYOnly(0, 0, cols+1, rows)
	}
	app.mu.Unlock()
	if v.reflowTimer != nil {
		v.reflowTimer.Stop()
	}
	v.reflowTimer = time.AfterFunc(guiReflowDelay, func() {
		app.mu.Lock()
		if app.root != nil {
			app.root.resize(0, 0, cols+1, rows)
		}
		if z := app.zoomedPane; z != nil {
			app.zoomGeom = [4]int{z.x, z.y, z.w, z.h}
			z.resize(0, 0, cols+1, rows)
		}
		app.mu.Unlock()
		app.triggerRedraw()
	})
	app.triggerRedraw()
}

// requestDraw is safe from any goroutine; bursts collapse into one idle.
func (v *termView) requestDraw() {
	if v.queued.Swap(true) {
		return
	}
	coreglib.IdleAdd(func() {
		v.queued.Store(false)
		v.QueueDraw()
	})
}

// drawRow paints one row of a pane whose first column is grid column base;
// the snapshot origin is the grid's left edge at the row's top.
//
// Rows are re-emitted every frame: gotk4 wraps GskRenderNode (a fundamental
// type, not a GObject) with GObject refcounting, so retained nodes are not
// safe. Shaped Pango layouts are cached instead, and GSK caches the glyphs.
func (v *termView) drawRow(rs *gtk.Snapshot, cells []vt10x.Glyph, base int) {
	colX := func(col int) float64 { return v.colX(base + col) }
	rt := v.theme
	n := len(cells)

	// Pass 1: backgrounds (runs of equal colour; theme bg is already painted).
	for col := 0; col < n; {
		c := &cells[col]
		if c.Image() != nil {
			bg := vtColor(c.BG, rt.bg, rt)
			x0, x1 := colX(col), colX(col+1)
			v.fillRect(rs, x0, 0, x1-x0, v.cellH/2, rgba(imageColor(c.Image().Top, bg), 1))
			v.fillRect(rs, x0, v.cellH/2, x1-x0, v.cellH-v.cellH/2, rgba(imageColor(c.Image().Bottom, bg), 1))
			col++
			continue
		}
		bg := vtColor(c.BG, rt.bg, rt)
		end := col + 1
		for end < n && cells[end].Image() == nil && vtColor(cells[end].BG, rt.bg, rt) == bg {
			end++
		}
		if bg != rt.bg {
			x0, x1 := colX(col), colX(end)
			v.fillRect(rs, x0, 0, x1-x0, v.cellH, rgba(bg, 1))
		}
		col = end
	}

	// Pass 2: text. ASCII runs sharing colour and face become one layout;
	// everything else is placed cell by cell so fallback fonts can't drift
	// the grid.
	var run strings.Builder
	runStart, runVariant := -1, uint8(0)
	var runFG tcell.Color
	flush := func() {
		if runStart >= 0 {
			if text := strings.TrimRight(run.String(), " "); text != "" {
				v.drawText(rs, text, runVariant, colX(runStart), runFG)
			}
		}
		run.Reset()
		runStart = -1
	}
	for col := 0; col < n; col++ {
		c := &cells[col]
		if c.Width == -1 || c.Image() != nil {
			flush()
			continue
		}
		ch := c.Char
		if ch == 0 || c.Mode&vtAttrInvisible != 0 {
			ch = ' '
		}
		fg, variant := v.cellStyle(c)
		if ch == ' ' && c.Combining() == "" {
			if runStart >= 0 {
				run.WriteByte(' ')
			}
			continue
		}
		if ch > 0x20 && ch < 0x7f && c.Combining() == "" {
			if runStart >= 0 && (fg != runFG || variant != runVariant) {
				flush()
			}
			if runStart < 0 {
				runStart, runFG, runVariant = col, fg, variant
			}
			run.WriteRune(ch)
			continue
		}
		flush()
		cw := 1
		if c.Width == 2 {
			cw = 2
		}
		if !v.drawSpecial(rs, ch, base+col, cw, fg) {
			v.drawText(rs, string(ch)+c.Combining(), variant, colX(col), fg)
		}
	}
	flush()

	// Pass 3: decorations.
	for col := 0; col < n; col++ {
		c := &cells[col]
		if c.Width == -1 || c.Mode&vtAttrInvisible != 0 {
			continue
		}
		deco := c.Mode & (vtAttrUnderline | vtAttrStrikethrough | vtAttrOverline)
		if deco == 0 {
			continue
		}
		fg, _ := v.cellStyle(c)
		x0 := colX(col)
		wcols := 1
		if c.Width == 2 {
			wcols = 2
		}
		x1 := colX(col + wcols)
		if c.Mode&vtAttrUnderline != 0 {
			ul := fg
			if c.Mode&vtAttrHasULColor != 0 {
				ul = vtColor(c.UL, rt.fg, rt)
			}
			v.drawUnderline(rs, x0, x1, (c.Mode&vtAttrUnderlineStyleMask)/vtAttrUnderlineStyleBit0, rgba(ul, 1))
		}
		if c.Mode&vtAttrStrikethrough != 0 {
			v.fillRect(rs, x0, math.Round(v.stPos-v.stThick/2), x1-x0, v.stThick, rgba(fg, 1))
		}
		if c.Mode&vtAttrOverline != 0 {
			v.fillRect(rs, x0, 0, x1-x0, v.ulThick, rgba(fg, 1))
		}
	}
}

// cellStyle resolves a cell's effective foreground and font face.
func (v *termView) cellStyle(c *vt10x.Glyph) (tcell.Color, uint8) {
	rt := v.theme
	fg := vtColor(c.FG, rt.fg, rt)
	if c.Mode&vtAttrDim != 0 {
		bg := vtColor(c.BG, rt.bg, rt)
		fr, fg2, fb := fg.RGB()
		br, bg2, bb := bg.RGB()
		fg = tcell.NewRGBColor((fr+br)/2, (fg2+bg2)/2, (fb+bb)/2)
	}
	variant := uint8(fontRegular)
	if c.Mode&vtAttrBold != 0 {
		variant |= fontBold
	}
	if c.Mode&vtAttrItalic != 0 {
		variant |= fontItalic
	}
	return fg, variant
}

func (v *termView) drawText(s *gtk.Snapshot, text string, variant uint8, x float64, fg tcell.Color) {
	ent := v.layout(text, variant)
	s.Save()
	s.Translate(v.pt(x, math.Round(v.baseline-ent.baseline)))
	c := rgba(fg, 1)
	s.AppendLayout(ent.layout, &c)
	s.Restore()
}

func (v *termView) layout(text string, variant uint8) *layoutEntry {
	key := layoutKey{text, variant}
	if ent := v.layouts[key]; ent != nil {
		return ent
	}
	l := pango.NewLayout(v.PangoContext())
	l.SetFontDescription(v.fonts[variant])
	l.SetText(text)
	ent := &layoutEntry{layout: l, baseline: float64(l.Baseline()) / pango.SCALE}
	v.layouts[key] = ent
	return ent
}

func (v *termView) drawUnderline(s *gtk.Snapshot, x0, x1 float64, style int16, c gdk.RGBA) {
	t := v.ulThick
	y := math.Round(min(v.ulPos, v.cellH-t))
	switch style {
	case 1: // double
		y2 := min(y+2*t, v.cellH-t)
		y = max(0, y2-2*t)
		v.fillRect(s, x0, y, x1-x0, t, c)
		v.fillRect(s, x0, y2, x1-x0, t, c)
	case 2: // curly
		amp := max(1, math.Round(v.cellH/12))
		base := min(y, v.cellH-t-amp)
		for x := x0; x < x1; x++ {
			dy := amp * math.Sin((x-x0)/v.cellW*2*math.Pi)
			v.fillRect(s, x, math.Round(base+dy), 1, t, c)
		}
	case 3: // dotted
		for x := x0; x < x1; x += 2 * t {
			v.fillRect(s, x, y, t, t, c)
		}
	case 4: // dashed
		dash := max(2, math.Round(v.cellW/2))
		for x := x0; x < x1; x += 2 * dash {
			v.fillRect(s, x, y, min(dash, x1-x), t, c)
		}
	default:
		v.fillRect(s, x0, y, x1-x0, t, c)
	}
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

// colX is the pixel-snapped x offset of a column inside the grid.
func (v *termView) colX(col int) float64 { return math.Round(float64(col) * v.cellW) }

func (v *termView) fillRect(s *gtk.Snapshot, x, y, w, h float64, c gdk.RGBA) {
	v.rect.Init(float32(x), float32(y), float32(w), float32(h))
	s.AppendColor(&c, v.rect)
}

func (v *termView) pt(x, y float64) *graphene.Point {
	return v.point.Init(float32(x), float32(y))
}

// cellAt maps widget coordinates to a grid cell. Column cols (one past the
// last) addresses the scrollbar gutter, matching Pane's reserved column.
func (v *termView) cellAt(x, y float64) (int, int) {
	col := int((x - v.pad) / v.cellW)
	row := int((y - v.pad) / v.cellH)
	return min(max(col, 0), v.cols), min(max(row, 0), max(v.rows-1, 0))
}

func rgba(c tcell.Color, alpha float32) gdk.RGBA {
	r, g, b := c.RGB()
	if r < 0 {
		r, g, b = 0, 0, 0
	}
	return gdk.NewRGBA(float32(r)/255, float32(g)/255, float32(b)/255, alpha)
}

// guiRecover keeps a bug in one callback from taking down the window.
// A panic inside a cgo callback would otherwise abort the process.
func guiRecover(where string) {
	if r := recover(); r != nil {
		L.Error("gui: recovered panic", "where", where, "panic", r)
	}
}
