// gui_ime.go - input methods: dead keys, Compose, CJK input, emoji picker.
//
// The view's key controller hands every key press to a GtkIMMulticontext
// first. The input method either consumes it (building a composition, or
// committing text) or lets it through to keyPressed as before. Plain keys
// with Ctrl or Alt are never composed, so shortcuts are unaffected.
//
// Committed text goes through bunk's key handling one character at a time,
// exactly like typed keys: search mode, passthrough, and leaving scrollback
// behave the same. While composing, the preedit text is drawn underlined at
// the cursor, and the input method is told where the cursor is so its
// candidate popup opens next to it.
package main

import (
	"math"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/gdamore/tcell/v2"
)

type imeState struct {
	ctx     *gtk.IMMulticontext
	preedit string        // text being composed; "" when not composing
	caret   int           // caret position in preedit, in characters
	area    [4]int        // cursor rectangle last reported to the IM
	layout  *pango.Layout // shaped preedit, rebuilt when it changes
}

func (v *termView) installIME(keys *gtk.EventControllerKey) {
	im := gtk.NewIMMulticontext()
	im.SetClientWidget(v)
	im.SetUsePreedit(true)
	im.SetObjectProperty("input-purpose", gtk.InputPurposeTerminal)
	im.ConnectCommit(v.imeCommit)
	im.ConnectPreeditChanged(func() {
		text, _, caret := im.PreeditString()
		v.setPreedit(text, caret)
	})
	im.ConnectPreeditEnd(func() { v.setPreedit("", 0) })
	// A terminal has no editable text around the cursor to offer.
	im.ConnectRetrieveSurrounding(func() bool { return false })
	keys.SetIMContext(im)
	v.ime.ctx = im
}

// imeCommit sends text the input method produced as typed characters.
func (v *termView) imeCommit(text string) {
	defer guiRecover("imeCommit")
	v.setPreedit("", 0)
	for _, r := range text {
		if !v.app.handleKey(tcell.NewEventKey(tcell.KeyRune, r, tcell.ModNone)) {
			v.ActivateAction("win.close-window", nil)
			return
		}
	}
}

func (v *termView) setPreedit(text string, caret int) {
	if text == v.ime.preedit && caret == v.ime.caret {
		return
	}
	v.ime.preedit, v.ime.caret = text, caret
	v.ime.layout = nil
	v.QueueDraw()
}

func (v *termView) imeFocus(focused bool) {
	if v.ime.ctx == nil {
		return
	}
	if focused {
		v.ime.ctx.FocusIn()
	} else {
		v.ime.ctx.FocusOut()
	}
}

// imeReportCursor tells the input method where the cursor is (widget
// coordinates), so candidate windows appear next to it. Only changes are
// sent.
func (v *termView) imeReportCursor(x, y, w, h float64) {
	area := [4]int{int(x), int(y), int(math.Ceil(w)), int(math.Ceil(h))}
	if v.ime.ctx == nil || area == v.ime.area {
		return
	}
	v.ime.area = area
	rect := gdk.NewRectangle(area[0], area[1], area[2], area[3])
	v.ime.ctx.SetCursorLocation(&rect)
}

// drawPreedit paints the composition at the cursor: the text on the theme
// background, underlined, with a thin caret.
func (v *termView) drawPreedit(s *gtk.Snapshot, x, y float64) {
	if v.ime.preedit == "" {
		return
	}
	if v.ime.layout == nil {
		l := pango.NewLayout(v.PangoContext())
		l.SetFontDescription(v.fonts[fontRegular])
		l.SetText(v.ime.preedit)
		v.ime.layout = l
	}
	l := v.ime.layout
	_, logical := l.Extents()
	w := float64(logical.Width()) / pango.SCALE
	fg, bg := v.theme.fg, v.theme.bg
	v.fillRect(s, x, y, w+1, v.cellH, rgba(bg, 1))
	s.Save()
	s.Translate(v.pt(x, y+math.Round(v.baseline-float64(l.Baseline())/pango.SCALE)))
	c := rgba(fg, 1)
	s.AppendLayout(l, &c)
	s.Restore()
	v.fillRect(s, x, y+math.Round(min(v.ulPos, v.cellH-v.ulThick)), w, v.ulThick, rgba(fg, 1))

	// Caret: the IM reports characters, pango indexes bytes.
	runes := []rune(v.ime.preedit)
	caret := min(max(v.ime.caret, 0), len(runes))
	pos := l.IndexToPos(len(string(runes[:caret])))
	cx := x + float64(pos.X())/pango.SCALE
	v.fillRect(s, cx, y, max(1, math.Round(v.cellH/14)), v.cellH, rgba(fg, 1))
}
