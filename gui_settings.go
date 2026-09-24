// gui_settings.go - the Preferences window.
//
// Every control writes one key into the config file through guiWin.setKey;
// the window never holds state of its own. After any reload (including edits
// made in a text editor) load() refreshes the controls from the new Config,
// with `updating` set so that refresh does not write back.
package main

import (
	"slices"
	"strconv"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
)

type settingsWindow struct {
	gw       *guiWin
	win      *gtk.Window
	updating bool

	themes       []string
	theme        *gtk.DropDown
	font         *gtk.FontDialogButton
	padding      *gtk.SpinButton
	tabsPos      *gtk.DropDown
	tabsWidth    *gtk.SpinButton
	tabsHide     *gtk.Switch
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

func newSettingsWindow(gw *guiWin) *settingsWindow {
	s := &settingsWindow{gw: gw, themes: guiThemeNames()}

	s.win = gtk.NewWindow()
	s.win.SetTitle("Preferences")
	s.win.SetTransientFor(&gw.win.Window)
	s.win.SetDestroyWithParent(true)
	s.win.SetHideOnClose(true)
	s.win.SetDefaultSize(460, -1)
	s.win.SetResizable(false)
	closeKey := gtk.NewEventControllerKey()
	closeKey.ConnectKeyPressed(func(keyval, _ uint, _ gdk.ModifierType) bool {
		if keyval == gdk.KEY_Escape {
			s.win.Close()
			return true
		}
		return false
	})
	s.win.AddController(closeKey)

	grid := gtk.NewGrid()
	grid.SetRowSpacing(10)
	grid.SetColumnSpacing(16)
	grid.SetMarginTop(18)
	grid.SetMarginBottom(18)
	grid.SetMarginStart(22)
	grid.SetMarginEnd(22)
	row := 0
	heading := func(text string) {
		l := gtk.NewLabel(text)
		l.AddCSSClass("bunker-settings-heading")
		l.SetXAlign(0)
		grid.Attach(l, 0, row, 2, 1)
		row++
	}
	field := func(label string, w gtk.Widgetter) {
		l := gtk.NewLabel(label)
		l.SetXAlign(0)
		l.SetHExpand(true)
		grid.Attach(l, 0, row, 1, 1)
		gtk.BaseWidget(w).SetHAlign(gtk.AlignEnd)
		grid.Attach(w, 1, row, 1, 1)
		row++
	}
	hint := func(text string) {
		l := gtk.NewLabel(text)
		l.AddCSSClass("bunker-settings-hint")
		l.SetXAlign(0)
		l.SetWrap(true)
		grid.Attach(l, 0, row, 2, 1)
		row++
	}

	// Appearance
	heading("Appearance")
	s.theme = gtk.NewDropDownFromStrings(s.themes)
	s.theme.NotifyProperty("selected", func() {
		if i := int(s.theme.Selected()); !s.updating && i < len(s.themes) {
			s.write("", "theme", tomlString(s.themes[i]))
		}
	})
	field("Theme", s.theme)

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
	field("Font", s.font)

	s.padding = spin(0, maxPadding, 1)
	s.padding.ConnectValueChanged(func() {
		if !s.updating {
			s.write("window", "padding", strconv.Itoa(s.padding.ValueAsInt()))
		}
	})
	field("Padding", s.padding)

	// Tabs
	heading("Tabs")
	s.tabsPos = gtk.NewDropDownFromStrings([]string{"Left", "Right", "Top", "Bottom"})
	s.tabsPos.NotifyProperty("selected", func() {
		if i := int(s.tabsPos.Selected()); !s.updating && i < len(tabPositions) {
			s.write("tabs", "position", tomlString(tabPositions[i]))
		}
	})
	field("Position", s.tabsPos)

	s.tabsWidth = spin(minTabsWidth, maxTabsWidth, 10)
	s.tabsWidth.ConnectValueChanged(func() {
		if !s.updating {
			s.write("tabs", "width", strconv.Itoa(s.tabsWidth.ValueAsInt()))
		}
	})
	field("Sidebar width", s.tabsWidth)

	s.tabsHide = gtk.NewSwitch()
	s.tabsHide.NotifyProperty("active", func() {
		if !s.updating {
			s.write("tabs", "autohide", strconv.FormatBool(s.tabsHide.Active()))
		}
	})
	field("Hide with a single tab", s.tabsHide)

	// Scrollback
	heading("Scrollback")
	s.scrollback = spin(100, 1_000_000, 1000)
	s.scrollback.ConnectValueChanged(func() {
		if !s.updating {
			s.write("", "scrollback", strconv.Itoa(s.scrollback.ValueAsInt()))
		}
	})
	field("Lines", s.scrollback)

	s.scrollbackMB = spin(0, 4096, 8)
	s.scrollbackMB.ConnectValueChanged(func() {
		if !s.updating {
			s.write("", "scrollback_mb", strconv.Itoa(s.scrollbackMB.ValueAsInt()))
		}
	})
	field("Memory cap (MiB)", s.scrollbackMB)
	hint("The smaller limit wins; 0 turns the memory cap off. Applies to new terminals.")

	// Config file
	heading("Config File")
	path := gtk.NewLabel(gw.path())
	path.SetSelectable(true)
	path.SetXAlign(0)
	path.SetEllipsize(pango.EllipsizeMiddle)
	path.SetHExpand(true)
	grid.Attach(path, 0, row, 1, 1)
	open := gtk.NewButtonWithLabel("Open in Editor")
	open.ConnectClicked(func() { gw.openConfigFile() })
	grid.Attach(open, 1, row, 1, 1)
	row++
	hint("Edits saved in any editor apply immediately. Settings here keep your comments.")

	s.status = gtk.NewLabel("")
	s.status.AddCSSClass("error")
	s.status.SetXAlign(0)
	s.status.SetWrap(true)
	s.status.SetVisible(false)
	grid.Attach(s.status, 0, row, 2, 1)

	s.win.SetChild(grid)
	return s
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
