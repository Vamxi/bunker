// gui_settings.go - the Preferences window.
//
// Layout: a sidebar of pages; each page is a column of titled groups, each
// group a card of rows (title, optional subtitle, control on the right).
// Adding a setting means adding one row() call and a line in load().
//
// Every control writes one key into the config file through guiWin.setKey;
// the window holds no state of its own. After any reload (including edits
// made in a text editor) load() refreshes the controls from the new Config,
// with `updating` set so that refresh does not write back.
package main

import (
	"os"
	"slices"
	"strconv"
	"strings"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

type settingsWindow struct {
	gw       *guiWin
	win      *gtk.Window
	stack    *gtk.Stack
	updating bool

	themes       []string
	theme        *gtk.DropDown
	font         *gtk.FontDialogButton
	padding      *gtk.SpinButton
	tabsPos      *gtk.DropDown
	tabsWidth    *gtk.SpinButton
	tabsHide     *gtk.Switch
	tabsCollapse *gtk.Switch
	scrollback   *gtk.SpinButton
	scrollbackMB *gtk.SpinButton
	status       *gtk.Label
}

// guiThemeNames lists themes usable in a window, sorted.
func guiThemeNames() []string {
	var names []string
	for name := range BuiltinThemes {
		if name != "terminal" {
			names = append(names, name)
		}
	}
	slices.Sort(names)
	return names
}

func (gw *guiWin) openSettings() {
	if gw.settings == nil {
		gw.settings = newSettingsWindow(gw)
	}
	gw.settings.load(gw.cfg)
	gw.settings.win.Present()
}

// ---------------------------------------------------------------------------
// Page / group / row builders
// ---------------------------------------------------------------------------

type settingsPage struct{ box *gtk.Box }

type settingsGroup struct{ list *gtk.ListBox }

func newSettingsPage(stack *gtk.Stack, name, title string) *settingsPage {
	box := gtk.NewBox(gtk.OrientationVertical, 22)
	box.SetMarginTop(26)
	box.SetMarginBottom(26)
	box.SetMarginStart(30)
	box.SetMarginEnd(30)
	heading := gtk.NewLabel(title)
	heading.AddCSSClass("bunker-page-title")
	heading.SetXAlign(0)
	box.Append(heading)

	scroll := gtk.NewScrolledWindow()
	scroll.SetPolicy(gtk.PolicyNever, gtk.PolicyAutomatic)
	scroll.SetChild(box)
	stack.AddTitled(scroll, name, title)
	return &settingsPage{box: box}
}

// group adds a titled card; description may be empty.
func (p *settingsPage) group(title, description string) *settingsGroup {
	g := gtk.NewBox(gtk.OrientationVertical, 6)
	if title != "" {
		l := gtk.NewLabel(title)
		l.AddCSSClass("bunker-group-title")
		l.SetXAlign(0)
		g.Append(l)
	}
	if description != "" {
		d := gtk.NewLabel(description)
		d.AddCSSClass("bunker-dim")
		d.SetXAlign(0)
		d.SetWrap(true)
		g.Append(d)
	}
	list := gtk.NewListBox()
	list.SetSelectionMode(gtk.SelectionNone)
	list.AddCSSClass("bunker-card")
	g.Append(list)
	p.box.Append(g)
	return &settingsGroup{list: list}
}

// row adds "title / subtitle ... control" to the group.
func (g *settingsGroup) row(title, subtitle string, control gtk.Widgetter) {
	text := gtk.NewBox(gtk.OrientationVertical, 2)
	text.SetHExpand(true)
	text.SetVAlign(gtk.AlignCenter)
	t := gtk.NewLabel(title)
	t.SetXAlign(0)
	text.Append(t)
	if subtitle != "" {
		s := gtk.NewLabel(subtitle)
		s.AddCSSClass("bunker-dim")
		s.SetXAlign(0)
		s.SetWrap(true)
		text.Append(s)
	}
	box := gtk.NewBox(gtk.OrientationHorizontal, 18)
	box.AddCSSClass("bunker-row")
	box.Append(text)
	if control != nil {
		gtk.BaseWidget(control).SetVAlign(gtk.AlignCenter)
		box.Append(control)
	}
	r := gtk.NewListBoxRow()
	r.SetActivatable(false)
	r.SetChild(box)
	g.list.Append(r)
}

// ---------------------------------------------------------------------------
// Window
// ---------------------------------------------------------------------------

func newSettingsWindow(gw *guiWin) *settingsWindow {
	s := &settingsWindow{gw: gw, themes: guiThemeNames()}

	s.win = gtk.NewWindow()
	s.win.SetTitle("Preferences")
	s.win.AddCSSClass("bunker-settings")
	s.win.SetTransientFor(&gw.win.Window)
	s.win.SetDestroyWithParent(true)
	s.win.SetHideOnClose(true)
	s.win.SetDefaultSize(780, 560)
	closeKey := gtk.NewEventControllerKey()
	closeKey.ConnectKeyPressed(func(keyval, _ uint, _ gdk.ModifierType) bool {
		if keyval == gdk.KEY_Escape {
			s.win.Close()
			return true
		}
		return false
	})
	s.win.AddController(closeKey)

	stack := gtk.NewStack()
	s.stack = stack
	stack.SetHExpand(true)
	stack.SetVExpand(true)
	stack.SetTransitionType(gtk.StackTransitionTypeCrossfade)

	s.buildAppearance(newSettingsPage(stack, "appearance", "Appearance"))
	s.buildTabs(newSettingsPage(stack, "tabs", "Tabs"))
	s.buildTerminal(newSettingsPage(stack, "terminal", "Terminal"))
	s.buildAdvanced(newSettingsPage(stack, "advanced", "Advanced"))

	sidebar := gtk.NewStackSidebar()
	sidebar.SetStack(stack)
	sidebar.SetSizeRequest(190, -1)
	sidebar.AddCSSClass("bunker-settings-sidebar")

	s.status = gtk.NewLabel("")
	s.status.AddCSSClass("bunker-settings-error")
	s.status.SetWrap(true)
	s.status.SetXAlign(0)
	s.status.SetVisible(false)

	right := gtk.NewBox(gtk.OrientationVertical, 0)
	right.Append(s.status)
	right.Append(stack)

	body := gtk.NewBox(gtk.OrientationHorizontal, 0)
	body.Append(sidebar)
	body.Append(gtk.NewSeparator(gtk.OrientationVertical))
	body.Append(right)
	s.win.SetChild(body)
	return s
}

func (s *settingsWindow) buildAppearance(p *settingsPage) {
	g := p.group("Colours", "")
	s.theme = gtk.NewDropDownFromStrings(s.themes)
	s.theme.NotifyProperty("selected", func() {
		if i := int(s.theme.Selected()); !s.updating && i < len(s.themes) {
			s.write("", "theme", tomlString(s.themes[i]))
		}
	})
	g.row("Theme", "Terminal colours; the header bar and tabs follow them", s.theme)

	g = p.group("Text", "")
	dialog := gtk.NewFontDialog()
	dialog.SetTitle("Terminal Font")
	dialog.SetModal(true)
	dialog.SetFilter(&gtk.NewCustomFilter(guiMonospaceOnly).Filter)
	s.font = gtk.NewFontDialogButton(dialog)
	s.font.SetLevel(gtk.FontLevelFont)
	s.font.NotifyProperty("font-desc", func() {
		if desc := s.font.FontDesc(); !s.updating && desc != nil {
			s.write("", "font", tomlString(desc.String()))
		}
	})
	g.row("Font", "Monospace fonts only; Ctrl+Shift+= / - zooms a window", s.font)

	s.padding = spin(0, maxPadding, 1)
	s.padding.ConnectValueChanged(func() {
		if !s.updating {
			s.write("window", "padding", strconv.Itoa(s.padding.ValueAsInt()))
		}
	})
	g.row("Padding", "Space between the window edge and the text, in pixels", s.padding)
}

func (s *settingsWindow) buildTabs(p *settingsPage) {
	g := p.group("Tab Bar", "")
	s.tabsPos = gtk.NewDropDownFromStrings([]string{"Left", "Right", "Top", "Bottom"})
	s.tabsPos.NotifyProperty("selected", func() {
		if i := int(s.tabsPos.Selected()); !s.updating && i < len(tabPositions) {
			s.write("tabs", "position", tomlString(tabPositions[i]))
		}
	})
	g.row("Position", "Left and right show a sidebar; top and bottom a bar", s.tabsPos)

	s.tabsWidth = spin(minTabsWidth, maxTabsWidth, 10)
	s.tabsWidth.ConnectValueChanged(func() {
		if !s.updating {
			s.write("tabs", "width", strconv.Itoa(s.tabsWidth.ValueAsInt()))
		}
	})
	g.row("Sidebar width", "In pixels, for left and right", s.tabsWidth)

	s.tabsHide = gtk.NewSwitch()
	s.tabsHide.NotifyProperty("active", func() {
		if !s.updating {
			s.write("tabs", "autohide", strconv.FormatBool(s.tabsHide.Active()))
		}
	})
	g.row("Hide with a single tab", "", s.tabsHide)

	s.tabsCollapse = gtk.NewSwitch()
	s.tabsCollapse.NotifyProperty("active", func() {
		if !s.updating {
			s.write("tabs", "collapsed", strconv.FormatBool(s.tabsCollapse.Active()))
		}
	})
	g.row("Start collapsed", "The sidebar shows one character per tab: its number, or the first letter of a name you gave it. The header-bar button toggles it.", s.tabsCollapse)

	g = p.group("Shortcuts", "")
	for _, sc := range [][2]string{
		{"New tab in the current directory", "Ctrl+Shift+T"},
		{"Close tab", "Ctrl+Shift+W"},
		{"Previous / next tab", "Ctrl+PgUp / Ctrl+PgDn"},
		{"Rename tab", "Double-click"},
		{"Close tab with the mouse", "Middle-click"},
	} {
		key := gtk.NewLabel(sc[1])
		key.AddCSSClass("bunker-dim")
		g.row(sc[0], "", key)
	}
}

func (s *settingsWindow) buildTerminal(p *settingsPage) {
	g := p.group("Scrollback", "The smaller limit wins. Changes apply to tabs opened afterwards.")
	s.scrollback = spin(100, 1_000_000, 1000)
	s.scrollback.ConnectValueChanged(func() {
		if !s.updating {
			s.write("", "scrollback", strconv.Itoa(s.scrollback.ValueAsInt()))
		}
	})
	g.row("Lines", "History kept per terminal", s.scrollback)

	s.scrollbackMB = spin(0, 4096, 8)
	s.scrollbackMB.ConnectValueChanged(func() {
		if !s.updating {
			s.write("", "scrollback_mb", strconv.Itoa(s.scrollbackMB.ValueAsInt()))
		}
	})
	g.row("Memory cap (MiB)", "0 turns the cap off; wide windows keep fewer lines under a cap", s.scrollbackMB)
}

func (s *settingsWindow) buildAdvanced(p *settingsPage) {
	g := p.group("Config File", "Everything here is stored in this file. Edits saved in any editor apply immediately, and settings changed here keep your comments.")
	open := gtk.NewButtonWithLabel("Open in Editor")
	open.ConnectClicked(func() { s.gw.openConfigFile() })
	g.row("config.toml", tildePath(s.gw.path()), open)
}

// tildePath shortens a path under the home directory to ~/….
func tildePath(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		if rest, ok := strings.CutPrefix(path, home+"/"); ok {
			return "~/" + rest
		}
	}
	return path
}

func spin(lo, hi, step float64) *gtk.SpinButton {
	sb := gtk.NewSpinButtonWithRange(lo, hi, step)
	sb.SetDigits(0)
	sb.SetNumeric(true)
	return sb
}

// write stores one key and reports a failure in the window.
func (s *settingsWindow) write(section, key, literal string) {
	if err := s.gw.setKey(section, key, literal); err != nil {
		L.Warn("settings: write failed", "key", key, "err", err)
		s.status.SetText("Could not save: " + err.Error())
		s.status.SetVisible(true)
		return
	}
	s.status.SetVisible(false)
}

// load shows cfg in the controls without writing anything back.
func (s *settingsWindow) load(cfg Config) {
	s.updating = true
	defer func() { s.updating = false }()

	if i := slices.Index(s.themes, cfg.ThemeName); i >= 0 {
		s.theme.SetSelected(uint(i))
	}
	s.font.SetFontDesc(pango.FontDescriptionFromString(cfg.Font))
	s.padding.SetValue(float64(cfg.Padding))
	if i := slices.Index(tabPositions, cfg.Tabs.Position); i >= 0 {
		s.tabsPos.SetSelected(uint(i))
	}
	s.tabsWidth.SetValue(float64(cfg.Tabs.Width))
	s.tabsHide.SetActive(cfg.Tabs.Autohide)
	s.tabsCollapse.SetActive(cfg.Tabs.Collapsed)
	s.scrollback.SetValue(float64(cfg.Scrollback))
	s.scrollbackMB.SetValue(float64(cfg.ScrollbackBytes >> 20))
}

// guiMonospaceOnly keeps the font chooser to fixed-width families; a
// proportional font would break the cell grid.
func guiMonospaceOnly(item *coreglib.Object) bool {
	switch v := item.Cast().(type) {
	case pango.FontFamilier:
		return pango.BaseFontFamily(v).IsMonospace()
	case pango.FontFacer:
		if fam := pango.BaseFontFace(v).Family(); fam != nil {
			return pango.BaseFontFamily(fam).IsMonospace()
		}
	}
	return true
}
