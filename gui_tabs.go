// gui_tabs.go - tabs: one terminal per tab, a strip of tab rows.
//
// Each tab owns a complete bunk pane model (an App with its own redraw,
// paneDead, and done channels) and a termView. Views live in a GtkStack, so
// only the visible tab is drawn; background tabs keep running and flag
// activity. The strip is a sidebar (left/right) or a bar (top/bottom),
// rebuilt in place when [tabs] position changes.
package main

import (
	"fmt"
	"slices"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const guiTitlePollSeconds = 1 // tab titles follow cwd/process changes

type guiTab struct {
	gw   *guiWin
	app  *App
	view *termView

	row      *gtk.Box
	title    *gtk.Label
	activity *gtk.Label
	lastSeen string // last title shown
	closed   bool
}

// newTabApp builds the pane model for one tab from the current config.
func newTabApp(cfg Config) *App {
	return &App{
		theme:           cfg.Theme,
		keys:            cfg.Keybindings,
		scrollback:      cfg.Scrollback,
		scrollbackBytes: cfg.ScrollbackBytes,
		redraw:          make(chan struct{}, 1),
		paneDead:        make(chan *Pane, 8),
		done:            make(chan struct{}),
		oscBuf:          newOSCBuffer(),
	}
}

// newTab opens a tab running command (nil = login shell) in dir ("" = the
// process cwd) and selects it.
func (gw *guiWin) newTab(dir string, command []string) *guiTab {
	t := &guiTab{gw: gw, app: newTabApp(gw.cfg)}
	app := t.app
	view := newTermView(app, gw.cfg)
	t.view = view
	view.win = gw.win
	view.onTitle = func(string) { t.refreshTitle() }
	view.onSpawn = func(cols, rows int) (*Pane, error) {
		// Pane widths include bunk's reserved scrollbar column.
		p, err := NewPane(app.nextID, 0, 0, cols+1, rows, app.scrollback, app.scrollbackBytes, dir, command,
			app.paneOSCColors(), app.redraw, app.paneDead, app.done, app.oscBuf,
			view.cellH/view.cellW)
		if err != nil {
			return nil, err
		}
		app.mu.Lock()
		app.nextID++
		app.root = newLeaf(p, 0, 0, cols+1, rows)
		app.active = p
		app.mu.Unlock()
		if view.focused {
			sendFocusIn(p)
		}
		view.requestDraw()
		coreglib.IdleAdd(t.refreshTitle)
		return p, nil
	}

	// GtkStack sizes only the visible child, so a view spawns its shell on
	// first allocation. Start the shell now at the current tab's grid size
	// instead, so background tabs run (and get titles) before being shown;
	// sizeAllocate corrects the size if this view ends up different.
	if prev := gw.active; prev != nil && prev.view.cols > 0 {
		view.cols, view.rows = prev.view.cols, prev.view.rows
		if _, err := view.onSpawn(view.cols, view.rows); err != nil {
			L.Error("gui: spawn failed", "err", err)
		}
	}

	t.buildRow()
	gw.tabs = append(gw.tabs, t)
	gw.stack.AddChild(view)
	gw.strip.Append(t.row)

	go t.redrawBridge()
	go func() {
		select {
		case p := <-app.paneDead:
			L.Info("gui: shell exited, closing tab", "pane", p.id)
			coreglib.IdleAdd(func() { gw.closeTab(t) })
		case <-app.done:
		}
	}()

	gw.selectTab(t)
	gw.updateStripVisibility()
	return t
}

// redrawBridge turns pane redraw signals into coalesced draws for the
// visible tab, and an activity mark for background tabs.
func (t *guiTab) redrawBridge() {
	app := t.app
	for {
		select {
		case <-app.redraw:
			sleepRenderSettle()
			drainRedraw(app.redraw)
			t.view.requestDraw()
			coreglib.IdleAdd(func() {
				if t != t.gw.active && !t.closed {
					t.activity.SetVisible(true)
				}
			})
		case <-app.done:
			return
		}
	}
}

func (t *guiTab) buildRow() {
	t.activity = gtk.NewLabel("●")
	t.activity.AddCSSClass("bunker-tab-activity")
	t.activity.SetVisible(false)

	t.title = gtk.NewLabel("shell")
	t.title.SetXAlign(0)
	t.title.SetHExpand(true)
	t.title.SetEllipsize(pango.EllipsizeEnd)
	t.title.SetMaxWidthChars(28)

	closeBtn := gtk.NewButtonFromIconName("window-close-symbolic")
	closeBtn.SetHasFrame(false)
	closeBtn.SetFocusOnClick(false)
	closeBtn.SetCanFocus(false)
	closeBtn.AddCSSClass("bunker-tab-close")
	closeBtn.SetTooltipText("Close Tab")
	closeBtn.ConnectClicked(func() { t.gw.closeTab(t) })

	t.row = gtk.NewBox(gtk.OrientationHorizontal, 6)
	t.row.AddCSSClass("bunker-tab")
	t.row.Append(t.activity)
	t.row.Append(t.title)
	t.row.Append(closeBtn)

	click := gtk.NewGestureClick()
	click.SetButton(0)
	click.ConnectPressed(func(_ int, _, _ float64) {
		switch click.CurrentButton() {
		case 1:
			t.gw.selectTab(t)
		case 2:
			t.gw.closeTab(t)
		}
	})
	t.row.AddController(click)
}

// refreshTitle updates the row (and the window, for the active tab).
func (t *guiTab) refreshTitle() {
	if t.closed {
		return
	}
	title := "shell"
	t.app.mu.Lock()
	p := t.app.active
	t.app.mu.Unlock()
	if p != nil {
		title = sanitizeTitle(paneDisplayTitle(p, "shell"))
	}
	if title == t.lastSeen {
		return
	}
	t.lastSeen = title
	t.title.SetText(title)
	t.row.SetTooltipText(title)
	if t == t.gw.active {
		t.gw.win.SetTitle(title)
	}
}

func (gw *guiWin) selectTab(t *guiTab) {
	if t == nil || t.closed {
		return
	}
	if prev := gw.active; prev != nil && prev != t {
		prev.row.RemoveCSSClass("active")
	}
	gw.active = t
	L.Debug("gui: select tab", "index", slices.Index(gw.tabs, t))
	t.row.AddCSSClass("active")
	t.activity.SetVisible(false)
	gw.stack.SetVisibleChild(t.view)
	t.view.GrabFocus()
	t.lastSeen = "" // force the window title to follow
	t.refreshTitle()
}

// selectRelative moves the selection by delta tabs, wrapping around.
func (gw *guiWin) selectRelative(delta int) {
	n := len(gw.tabs)
	if n < 2 {
		return
	}
	i := slices.Index(gw.tabs, gw.active)
	gw.selectTab(gw.tabs[((i+delta)%n+n)%n])
}

// closeTab ends the tab's shell and removes it; the last tab closes the
// window.
func (gw *guiWin) closeTab(t *guiTab) {
	if t.closed {
		return
	}
	t.closed = true
	i := slices.Index(gw.tabs, t)
	gw.tabs = slices.Delete(gw.tabs, i, i+1)
	t.app.shutOnce.Do(func() {
		t.app.closeAllPanes()
		close(t.app.done)
	})
	gw.stack.Remove(t.view)
	gw.strip.Remove(t.row)

	if len(gw.tabs) == 0 {
		gw.win.Close()
		return
	}
	if gw.active == t {
		gw.active = nil
		gw.selectTab(gw.tabs[min(i, len(gw.tabs)-1)])
	}
	gw.updateStripVisibility()
}

// closeAllTabs ends every shell; used when the window goes away.
func (gw *guiWin) closeAllTabs() {
	for _, t := range gw.tabs {
		t.closed = true
		t.app.shutOnce.Do(func() {
			t.app.closeAllPanes()
			close(t.app.done)
		})
	}
	gw.tabs = nil
}

// activeCwd is the working directory of the active tab's shell, so a new
// tab opens where you are.
func (gw *guiWin) activeCwd() string {
	if gw.active == nil {
		return ""
	}
	gw.active.app.mu.Lock()
	p := gw.active.app.active
	gw.active.app.mu.Unlock()
	if p == nil {
		return ""
	}
	return p.cwd()
}

// ---------------------------------------------------------------------------
// Strip layout
// ---------------------------------------------------------------------------

// buildLayout creates the stack and strip containers once.
func (gw *guiWin) buildLayout() *gtk.Box {
	gw.stack = gtk.NewStack()
	gw.stack.SetTransitionType(gtk.StackTransitionTypeNone)
	gw.stack.SetHExpand(true)
	gw.stack.SetVExpand(true)

	gw.strip = gtk.NewBox(gtk.OrientationVertical, 2)
	gw.strip.AddCSSClass("bunker-tab-list")
	gw.stripScroll = gtk.NewScrolledWindow()
	gw.stripScroll.AddCSSClass("bunker-tabs")
	gw.stripScroll.SetChild(gw.strip)

	gw.layout = gtk.NewBox(gtk.OrientationHorizontal, 0)
	gw.applyTabsLayout()
	return gw.layout
}

// applyTabsLayout places the strip for [tabs] position/width. Safe to call
// again on every config reload.
func (gw *guiWin) applyTabsLayout() {
	tc := gw.cfg.Tabs
	if gw.stack.Parent() != nil {
		gw.layout.Remove(gw.stack)
	}
	if gw.stripScroll.Parent() != nil {
		gw.layout.Remove(gw.stripScroll)
	}

	vertical := tc.Position == "left" || tc.Position == "right"
	if vertical {
		gw.layout.SetOrientation(gtk.OrientationHorizontal)
		gw.strip.SetOrientation(gtk.OrientationVertical)
		gw.stripScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
		gw.stripScroll.SetSizeRequest(tc.Width, -1)
		gw.stripScroll.SetHExpand(false)
		gw.stripScroll.SetVExpand(true)
	} else {
		gw.layout.SetOrientation(gtk.OrientationVertical)
		gw.strip.SetOrientation(gtk.OrientationHorizontal)
		gw.stripScroll.SetPolicy(gtk.PolicyAutomatic, gtk.PolicyNever)
		gw.stripScroll.SetSizeRequest(-1, -1)
		gw.stripScroll.SetHExpand(true)
		gw.stripScroll.SetVExpand(false)
	}
	for _, side := range []string{"left", "right", "top", "bottom"} {
		gw.stripScroll.RemoveCSSClass(side)
	}
	gw.stripScroll.AddCSSClass(tc.Position)

	if tc.Position == "left" || tc.Position == "top" {
		gw.layout.Append(gw.stripScroll)
		gw.layout.Append(gw.stack)
	} else {
		gw.layout.Append(gw.stack)
		gw.layout.Append(gw.stripScroll)
	}
	gw.updateStripVisibility()
	if gw.active != nil {
		gw.active.view.GrabFocus()
	}
}

func (gw *guiWin) updateStripVisibility() {
	if gw.stripScroll == nil {
		return
	}
	gw.stripScroll.SetVisible(!(gw.cfg.Tabs.Autohide && len(gw.tabs) <= 1))
}

// pollTitles keeps tab titles current (cwd and foreground process change
// without output reaching the view). Stops when the window is gone.
func (gw *guiWin) pollTitles() {
	coreglib.TimeoutSecondsAdd(guiTitlePollSeconds, func() bool {
		if gw.win == nil || len(gw.tabs) == 0 {
			return false
		}
		for _, t := range gw.tabs {
			t.refreshTitle()
		}
		return true
	})
}

// ---------------------------------------------------------------------------
// Theme-coloured window chrome
// ---------------------------------------------------------------------------

// themeCSS derives the window chrome (header bar, tab strip) from the
// terminal theme, so the window reads as one surface instead of terminal
// colours inside generic GTK grey. Scoped to .bunker-window so dialogs keep
// the system style.
func themeCSS(rt resolvedTheme) string {
	rgb := func(c interface{ RGB() (int32, int32, int32) }) [3]int32 {
		r, g, b := c.RGB()
		return [3]int32{r, g, b}
	}
	bg, fg, accent := rgb(rt.bg), rgb(rt.fg), rgb(rt.palette[4])
	// mix blends a towards b by t and formats the result as #rrggbb.
	mix := func(a, b [3]int32, t float64) string {
		var c [3]int32
		for i := range c {
			c[i] = int32(float64(a[i])*(1-t) + float64(b[i])*t)
		}
		return fmt.Sprintf("#%02x%02x%02x", c[0], c[1], c[2])
	}
	return strings.NewReplacer(
		"@bg", mix(bg, fg, 0),
		"@sidebar", mix(bg, fg, 0.04),
		"@hover", mix(bg, fg, 0.08),
		"@line", mix(bg, fg, 0.10),
		"@selected", mix(bg, fg, 0.15),
		"@fg", mix(fg, bg, 0),
		"@text", mix(fg, bg, 0.08),
		"@muted", mix(fg, bg, 0.30),
		"@dim", mix(fg, bg, 0.45),
		"@accent", mix(accent, fg, 0.1),
	).Replace(`
window.bunker-window { background-color: @bg; }
window.bunker-window headerbar {
	background: @bg;
	color: @text;
	box-shadow: none;
	border-bottom: 1px solid @line;
}
window.bunker-window headerbar:backdrop { background: @bg; color: @dim; }
window.bunker-window headerbar button { color: inherit; background: transparent; box-shadow: none; }
window.bunker-window headerbar button:hover { background-color: @hover; }
window.bunker-window headerbar button:active,
window.bunker-window headerbar button:checked { background-color: @selected; }
window.bunker-window headerbar windowcontrols button > image { background-color: @hover; color: inherit; }
window.bunker-window headerbar windowcontrols button:hover > image { background-color: @selected; }
.bunker-tabs { background-color: @sidebar; color: @text; }
.bunker-tabs.left { border-right: 1px solid @line; }
.bunker-tabs.right { border-left: 1px solid @line; }
.bunker-tabs.top { border-bottom: 1px solid @line; }
.bunker-tabs.bottom { border-top: 1px solid @line; }
.bunker-tab-list { padding: 6px; }
.bunker-tab { padding: 5px 4px 5px 10px; border-radius: 7px; min-height: 26px; color: @muted; }
.bunker-tab:hover { background-color: @hover; }
.bunker-tab.active { background-color: @selected; color: @fg; }
.bunker-tab .bunker-tab-close { min-width: 22px; min-height: 22px; padding: 0; opacity: 0; }
.bunker-tab:hover .bunker-tab-close, .bunker-tab.active .bunker-tab-close { opacity: 0.75; }
.bunker-tab-activity { color: @accent; font-size: 9px; }
`)
}
