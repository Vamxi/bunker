// gui_tabs.go - tabs: one terminal per tab, a strip of tab rows.
//
// Each tab owns a complete bunk pane model (an App with its own redraw,
// paneDead, and done channels) and a termView. Views live in a GtkStack, so
// only the visible tab is drawn; background tabs keep running.
// The strip is a sidebar (left/right) or a bar (top/bottom),
// rebuilt in place when [tabs] position changes.
package main

import (
	"slices"
	"strconv"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

const (
	guiTitlePollSeconds = 1  // tab titles follow cwd/process changes
	guiCollapsedWidth   = 46 // sidebar width when collapsed to one character
	guiMenuSettleMs     = 80 // wait after a menu closes before moving focus
)

type guiTab struct {
	gw   *guiWin
	app  *App
	view *termView

	row      *gtk.Box
	short    *gtk.Label // one-character label for the collapsed sidebar
	closeBtn *gtk.Button
	title    *gtk.Label
	entry    *gtk.Entry // rename field, shown while editing
	info     *gtk.Label // context (container, ssh, sudo) and pane count
	lastSeen string     // last title and info shown
	closed   bool

	customTitle string // set by renaming; "" = follow the program's title
	editing     bool
	renameFrom  string // entry text when editing started
	recollapse  bool   // rename expanded a collapsed sidebar; collapse after

	menu *gtk.PopoverMenu // right-click menu, created on first use
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
	// A shell exiting removes its pane from bunk's tree (focus moves to a
	// neighbour, as in bunk); the tab closes with its last pane.
	app.onEmpty = func() { coreglib.IdleAdd(func() { gw.closeTab(t) }) }
	go func() {
		for {
			select {
			case p := <-app.paneDead:
				L.Info("gui: shell exited", "pane", p.id)
				app.removePane(p)
				coreglib.IdleAdd(t.refreshTitle)
			case <-app.done:
				return
			}
		}
	}()

	t.applyRowMode()
	gw.selectTab(t)
	gw.updateStripVisibility()
	return t
}

// redrawBridge turns pane redraw signals into coalesced draws; a hidden
// tab's view is not snapshotted, so its requests cost nothing.
func (t *guiTab) redrawBridge() {
	app := t.app
	for {
		select {
		case <-app.redraw:
			sleepRenderSettle()
			drainRedraw(app.redraw)
			t.view.requestDraw()
		case <-app.done:
			return
		}
	}
}

func (t *guiTab) buildRow() {
	// Every child is centred vertically: titles often start with a symbol
	// from a fallback font (Claude Code's ✳) whose taller line metrics
	// would otherwise push the text off-centre.
	t.title = gtk.NewLabel("shell")
	t.title.SetXAlign(t.gw.tabTextAlign())
	t.title.SetHExpand(true)
	t.title.SetVAlign(gtk.AlignCenter)
	t.title.SetSingleLineMode(true)
	t.title.SetEllipsize(pango.EllipsizeEnd)
	t.title.SetMaxWidthChars(28)

	t.info = gtk.NewLabel("")
	t.info.AddCSSClass("bunker-tab-info")
	t.info.SetVAlign(gtk.AlignCenter)
	t.info.SetVisible(false)

	t.entry = gtk.NewEntry()
	t.entry.AddCSSClass("bunker-tab-entry")
	t.entry.SetHExpand(true)
	t.entry.SetVAlign(gtk.AlignCenter)
	t.entry.SetVisible(false)
	t.entry.ConnectActivate(func() { t.finishRename(true) })
	entryKeys := gtk.NewEventControllerKey()
	entryKeys.ConnectKeyPressed(func(keyval, _ uint, _ gdk.ModifierType) bool {
		if keyval == gdk.KEY_Escape {
			t.finishRename(false)
			return true
		}
		return false
	})
	t.entry.AddController(entryKeys)
	entryFocus := gtk.NewEventControllerFocus()
	entryFocus.ConnectLeave(func() { t.finishRename(true) })
	t.entry.AddController(entryFocus)

	closeBtn := gtk.NewButtonFromIconName("window-close-symbolic")
	closeBtn.SetHasFrame(false)
	closeBtn.SetFocusOnClick(false)
	closeBtn.SetCanFocus(false)
	closeBtn.AddCSSClass("bunker-tab-close")
	closeBtn.SetVAlign(gtk.AlignCenter)
	closeBtn.SetTooltipText("Close Tab")
	closeBtn.ConnectClicked(func() { t.gw.closeTab(t) })

	t.closeBtn = closeBtn

	t.short = gtk.NewLabel("")
	t.short.AddCSSClass("bunker-tab-short")
	t.short.SetHExpand(true)
	t.short.SetXAlign(0.5)
	t.short.SetVAlign(gtk.AlignCenter)

	t.row = gtk.NewBox(gtk.OrientationHorizontal, 6)
	t.row.AddCSSClass("bunker-tab")
	t.row.Append(t.short)
	t.row.Append(t.title)
	t.row.Append(t.info)
	t.row.Append(t.entry)
	t.row.Append(closeBtn)

	click := gtk.NewGestureClick()
	click.SetButton(0)
	click.ConnectPressed(func(n int, x, y float64) {
		if t.editing {
			return
		}
		switch click.CurrentButton() {
		case 1:
			t.gw.selectTab(t)
			// In the collapsed sidebar a double-click is almost always two
			// quick selects, so only the expanded sidebar renames.
			if n == 2 && !t.gw.isCollapsed() {
				t.startRename()
			}
		case 2:
			t.gw.closeTab(t)
		case 3:
			t.showMenu(x, y)
		}
	})
	t.row.AddController(click)
	t.installActions()
}

// installActions gives the row a "tab" action group for its menu.
func (t *guiTab) installActions() {
	group := gio.NewSimpleActionGroup()
	add := func(name string, fn func()) {
		a := gio.NewSimpleAction(name, nil)
		a.ConnectActivate(func(*glib.Variant) { fn() })
		group.AddAction(a)
	}
	// GTK hides the menu before running the action, and hiding hands focus
	// back to the terminal; starting the edit right away would lose focus
	// at once and end it. Start it just after that settles instead.
	add("rename", func() {
		coreglib.TimeoutAdd(guiMenuSettleMs, func() bool {
			t.startRename()
			return false
		})
	})
	add("reset-name", func() {
		t.customTitle = ""
		t.lastSeen = ""
		t.refreshTitle()
		t.applyRowMode()
	})
	add("close", func() { t.gw.closeTab(t) })
	add("close-others", func() {
		for _, other := range slices.Clone(t.gw.tabs) {
			if other != t {
				t.gw.closeTab(other)
			}
		}
	})
	t.row.InsertActionGroup("tab", group)
}

// showMenu opens the right-click menu at (x, y) in row coordinates.
func (t *guiTab) showMenu(x, y float64) {
	names := gio.NewMenu()
	names.Append("Rename…", "tab.rename")
	if t.customTitle != "" {
		names.Append("Reset Name", "tab.reset-name")
	}
	closing := gio.NewMenu()
	closing.Append("Close Tab", "tab.close")
	if len(t.gw.tabs) > 1 {
		closing.Append("Close Other Tabs", "tab.close-others")
	}
	menu := gio.NewMenu()
	menu.AppendSection("", names)
	menu.AppendSection("", closing)

	if t.menu == nil {
		t.menu = gtk.NewPopoverMenuFromModel(menu)
		t.menu.SetParent(t.row)
		t.menu.SetHasArrow(false)
	} else {
		t.menu.SetMenuModel(menu)
	}
	rect := gdk.NewRectangle(int(x), int(y), 1, 1)
	t.menu.SetPointingTo(&rect)
	t.menu.Popup()
}

// refreshTitle updates the row (and the window, for the active tab).
func (t *guiTab) refreshTitle() {
	if t.closed {
		return
	}
	info := t.describe()
	if info.title == "" {
		info.title = "shell"
	}
	if t.customTitle != "" {
		info.title = t.customTitle
	}
	key := info.title + "\x00" + info.context + "\x00" + info.cwd + "\x00" + strconv.Itoa(info.panes)
	if key == t.lastSeen {
		return
	}
	t.lastSeen = key

	t.title.SetText(info.title)
	var chip []string
	if info.context != "" {
		chip = append(chip, info.context)
	}
	if info.panes > 1 {
		chip = append(chip, "⊞"+strconv.Itoa(info.panes))
	}
	t.info.SetText(strings.Join(chip, " "))
	t.info.SetVisible(len(chip) > 0 && !t.gw.isCollapsed())
	tip := info.title
	if info.context != "" {
		tip += "\n" + info.context
	}
	if info.cwd != "" {
		tip += "\n" + tildePath(info.cwd)
	}
	t.row.SetTooltipText(tip)
	if t == t.gw.active {
		t.gw.setWindowTitle(info)
	}
}

// tabInfo is what the tab strip and header bar show about a tab: the
// focused pane's title, cwd, and context, and how many panes it has.
type tabInfo struct {
	title, cwd, context string
	panes               int
}

func (t *guiTab) describe() tabInfo {
	var ti tabInfo
	t.app.mu.Lock()
	p := t.app.active
	if t.app.root != nil {
		ti.panes = len(t.app.root.leaves())
	}
	t.app.mu.Unlock()
	if p == nil {
		return ti
	}
	ti.title = sanitizeTitle(paneDisplayTitle(p, ""))
	ti.cwd = p.cwd()
	ti.context, _ = p.context()
	return ti
}

// shortLabel is the collapsed-sidebar label: the first character of a
// custom name, else the tab's position.
func (t *guiTab) shortLabel() string {
	for _, r := range t.customTitle {
		return strings.ToUpper(string(r))
	}
	return strconv.Itoa(slices.Index(t.gw.tabs, t) + 1)
}

// applyRowMode shows either the full row or the one-character label.
func (t *guiTab) applyRowMode() {
	collapsed := t.gw.isCollapsed()
	t.short.SetText(t.shortLabel())
	t.short.SetVisible(collapsed)
	t.title.SetVisible(!collapsed && !t.editing)
	t.entry.SetVisible(!collapsed && t.editing)
	t.closeBtn.SetVisible(!collapsed)
	t.info.SetVisible(!collapsed && t.info.Text() != "")
	if collapsed {
		t.row.AddCSSClass("collapsed")
	} else {
		t.row.RemoveCSSClass("collapsed")
	}
}

// startRename swaps the title for an entry (double-click on a tab).
func (t *guiTab) startRename() {
	if t.editing || t.closed {
		return
	}
	if t.gw.isCollapsed() {
		t.gw.setCollapsed(false) // the entry needs the full width
		t.recollapse = true
	}
	t.editing = true
	t.renameFrom = t.title.Text()
	t.entry.SetText(t.renameFrom)
	t.title.SetVisible(false)
	t.entry.SetVisible(true)
	t.entry.GrabFocus()
	t.entry.SelectRegion(0, -1)
}

// finishRename ends editing. Committing an empty name returns the tab to
// the program's own title.
func (t *guiTab) finishRename(commit bool) {
	if !t.editing {
		return
	}
	t.editing = false
	if commit {
		t.customTitle = renamedTitle(t.customTitle, t.renameFrom, t.entry.Text())
	}
	t.entry.SetVisible(false)
	t.title.SetVisible(true)
	t.lastSeen = ""
	t.refreshTitle()
	if t.recollapse {
		t.recollapse = false
		t.gw.setCollapsed(true)
	}
	t.applyRowMode() // the short label follows a new name
	if t == t.gw.active {
		t.view.GrabFocus()
	}
}

// renamedTitle is the custom title after a rename that started from
// initial and ended with entered. Leaving the text untouched keeps the
// previous state, so clicking away never turns the automatic title into a
// fixed name; clearing it returns to the automatic title.
func renamedTitle(prev, initial, entered string) string {
	entered = strings.TrimSpace(entered)
	if entered == strings.TrimSpace(initial) {
		return prev
	}
	return entered
}

// tabTextAlign centres titles in a horizontal bar and left-aligns them in
// a sidebar.
func (gw *guiWin) tabTextAlign() float32 {
	if gw.cfg.Tabs.Position == "top" || gw.cfg.Tabs.Position == "bottom" {
		return 0.5
	}
	return 0
}

func (gw *guiWin) selectTab(t *guiTab) {
	if t == nil || t.closed {
		return
	}
	if prev := gw.active; prev != nil && prev != t {
		prev.row.RemoveCSSClass("active")
	}
	gw.active = t
	t.row.AddCSSClass("active")
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
	t.shutdown()
	i := slices.Index(gw.tabs, t)
	gw.tabs = slices.Delete(gw.tabs, i, i+1)
	gw.stack.Remove(t.view)
	gw.strip.Remove(t.row)
	for _, other := range gw.tabs {
		other.applyRowMode() // numbers shift down
	}

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
		t.shutdown()
	}
	gw.tabs = nil
}

// shutdown ends the tab's shells and goroutines (idempotent).
func (t *guiTab) shutdown() {
	t.closed = true
	t.app.shutOnce.Do(func() {
		t.app.closeAllPanes()
		close(t.app.done)
	})
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
		width := tc.Width
		if gw.collapsed {
			width = guiCollapsedWidth
		}
		gw.layout.SetOrientation(gtk.OrientationHorizontal)
		gw.strip.SetOrientation(gtk.OrientationVertical)
		gw.stripScroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
		gw.stripScroll.SetSizeRequest(width, -1)
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
	for _, t := range gw.tabs {
		t.title.SetXAlign(gw.tabTextAlign())
		t.applyRowMode()
	}
	if gw.isCollapsed() {
		gw.strip.AddCSSClass("collapsed")
	} else {
		gw.strip.RemoveCSSClass("collapsed")
	}
	if gw.sidebarBtn != nil {
		gw.sidebarBtn.SetVisible(vertical)
		if tc.Position == "right" {
			gw.sidebarBtn.SetIconName("sidebar-show-right-symbolic")
		} else {
			gw.sidebarBtn.SetIconName("sidebar-show-symbolic")
		}
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

// isCollapsed reports whether the sidebar shows one character per tab;
// collapsing only applies to a left or right sidebar.
func (gw *guiWin) isCollapsed() bool {
	pos := gw.cfg.Tabs.Position
	return gw.collapsed && (pos == "left" || pos == "right")
}

// setCollapsed narrows or widens the sidebar for this window.
func (gw *guiWin) setCollapsed(collapsed bool) {
	if gw.collapsed == collapsed {
		return
	}
	gw.collapsed = collapsed
	gw.applyTabsLayout()
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
