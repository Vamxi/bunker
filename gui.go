// gui.go - bunker's GTK4 application shell.
//
// runGUI owns the GTK main loop. Panes, PTYs, and emulation are bunk's; each
// tab reuses an App struct as its pane model (root, active, redraw, done)
// with no tcell screen attached. GTK calls stay on the main thread:
// background goroutines only reach it through glib.IdleAdd.
//
// guiWin owns the config for its window: the TOML file is the source of
// truth. Settings writes single keys into it (see tomledit.go), a directory
// monitor picks up edits from any editor, and every change goes through
// reload, which applies only what differs.
package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/glib/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/diamondburned/gotk4/pkg/pango"
	"github.com/gdamore/tcell/v2"
)

const (
	guiAppID          = "dev.bunker.Bunker"
	guiReloadDebounce = 150 // ms; editors often write a file in several steps
	guiBannerTimeout  = 8   // seconds an error banner stays up
)

// guiWin is one bunker window and the config that drives it.
type guiWin struct {
	gapp *gtk.Application
	win  *gtk.ApplicationWindow

	tabs        []*guiTab
	active      *guiTab
	stack       *gtk.Stack
	strip       *gtk.Box
	stripScroll *gtk.ScrolledWindow
	layout      *gtk.Box
	themeCSS    *gtk.CSSProvider
	sidebarBtn  *gtk.Button
	headTitle   *gtk.Label
	headSub     *gtk.Label
	collapsed   bool // this window's sidebar state; starts from [tabs] collapsed

	cfg           Config
	configPath    string // "" = default location
	themeOverride string // --theme, kept across reloads

	settings    *settingsWindow
	banner      *gtk.Label
	bannerTimer coreglib.SourceHandle
	monitor     gio.FileMonitorrer
	reloadTimer coreglib.SourceHandle
}

// runGUI starts the GTK application. command, when non-empty, replaces the
// login shell in the first tab.
func runGUI(configPath, themeName string, debug, trace bool, command []string) error {
	cfg, err := LoadConfig(configPath, themeName)
	if err != nil {
		return err
	}
	logLevel, logFile := cfg.LogLevel, ""
	switch {
	case trace:
		logLevel = "trace"
	case debug:
		logLevel = "debug"
	}
	if debug || trace {
		logFile = cfg.LogFile
	}
	cleanup := initLogger(logFile, logLevel)
	defer cleanup()

	cfg = guiConfig(cfg)
	L.Info("bunker starting", "config", cfg.Path, "font", cfg.Font, "log_level", logLevel)

	stopProfile := guiStartProfile()
	defer stopProfile()

	var windows []*guiWin
	gapp := gtk.NewApplication(guiAppID, gio.ApplicationNonUnique)
	gapp.ConnectActivate(func() {
		gw := &guiWin{gapp: gapp, cfg: cfg, configPath: configPath, themeOverride: themeName, collapsed: cfg.Tabs.Collapsed}
		windows = append(windows, gw)
		gw.build(command)
	})
	code := gapp.Run([]string{os.Args[0]})

	for _, gw := range windows {
		gw.closeAllTabs()
	}
	if code != 0 {
		stopProfile()
		os.Exit(code)
	}
	return nil
}

// guiConfig adapts a loaded config for the GUI: the "terminal" theme
// inherits host colours, and a window has no host. BUNKER_FONT overrides
// the configured font (handy for trying fonts without editing the file).
func guiConfig(cfg Config) Config {
	if cfg.Theme.bg == tcell.ColorDefault || cfg.Theme.fg == tcell.ColorDefault {
		cfg.Theme = resolveTheme(BuiltinThemes[defaultThemeName])
		cfg.ThemeName = defaultThemeName
	}
	if font := os.Getenv("BUNKER_FONT"); font != "" {
		cfg.Font = font
	}
	return cfg
}

func (gw *guiWin) build(command []string) {
	guiInstallCSS()
	gw.themeCSS = gtk.NewCSSProvider()
	gw.themeCSS.LoadFromString(themeCSS(gw.cfg.Theme))
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), gw.themeCSS, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	gw.applyDarkPreference()

	win := gtk.NewApplicationWindow(gw.gapp)
	win.SetTitle("bunker")
	win.AddCSSClass("bunker-window")
	gw.win = win
	gw.installActions()
	win.SetTitlebar(gw.headerBar())
	win.ConnectCloseRequest(func() bool {
		gw.closeAllTabs()
		return false
	})

	// The banner floats over the terminal area for config errors.
	overlay := gtk.NewOverlay()
	overlay.SetChild(gw.buildLayout())
	gw.banner = gtk.NewLabel("")
	gw.banner.AddCSSClass("bunker-banner")
	gw.banner.SetWrap(true)
	gw.banner.SetHAlign(gtk.AlignCenter)
	gw.banner.SetVAlign(gtk.AlignStart)
	gw.banner.SetMarginTop(12)
	gw.banner.SetMarginStart(24)
	gw.banner.SetMarginEnd(24)
	gw.banner.SetVisible(false)
	dismiss := gtk.NewGestureClick()
	dismiss.ConnectPressed(func(int, float64, float64) { gw.banner.SetVisible(false) })
	gw.banner.AddController(dismiss)
	overlay.AddOverlay(gw.banner)
	win.SetChild(overlay)

	gw.newTab("", command)
	win.Present()
	// Debug-only extra tabs open once the first tab has a size.
	if extras := guiDebugExtraTabs(); len(extras) > 0 {
		coreglib.TimeoutAdd(300, func() bool {
			for _, extra := range extras {
				gw.newTab("", extra)
			}
			return false
		})
	}
	if gw.active != nil {
		gw.active.view.GrabFocus()
	}

	if open := os.Getenv("BUNKER_OPEN"); strings.HasPrefix(open, "preferences") {
		gw.openSettings()
		if _, page, ok := strings.Cut(open, "/"); ok {
			gw.settings.stack.SetVisibleChildName(page)
		}
	}
	switch os.Getenv("BUNKER_OPEN") {
	case "tab-menu":
		coreglib.TimeoutAdd(800, func() bool {
			if gw.active != nil {
				gw.active.showMenu(40, 12)
			}
			return false
		})
	case "tab-rename": // choose Rename… the way a click does: hide, then activate
		coreglib.TimeoutAdd(800, func() bool {
			if t := gw.active; t != nil {
				t.showMenu(40, 12)
				coreglib.TimeoutAdd(200, func() bool {
					t.menu.Popdown()
					t.row.ActivateAction("tab.rename", nil)
					return false
				})
			}
			return false
		})
	}
	guiScheduleScreenshot(win, func() gtk.Widgetter {
		if gw.settings != nil && gw.settings.win.IsVisible() {
			return gw.settings.win
		}
		if gw.active != nil && gw.active.menu != nil && gw.active.menu.IsVisible() {
			return gw.active.menu
		}
		return win
	})
	gw.watchConfig()
	gw.pollTitles()
	gw.runDebugKeys()
}

func (gw *guiWin) headerBar() *gtk.HeaderBar {
	menu := gio.NewMenu()
	menu.Append("New Tab", "win.new-tab")
	menu.Append("Preferences", "win.preferences")
	menu.Append("Open Config File", "win.open-config")
	button := gtk.NewMenuButton()
	button.SetIconName("open-menu-symbolic")
	button.SetMenuModel(menu)
	button.SetTooltipText("Main Menu")
	button.SetFocusOnClick(false)

	newTab := gtk.NewButtonFromIconName("tab-new-symbolic")
	newTab.SetTooltipText("New Tab (Ctrl+Shift+T)")
	newTab.SetFocusOnClick(false)
	newTab.SetActionName("win.new-tab")

	gw.sidebarBtn = gtk.NewButtonFromIconName("sidebar-show-symbolic")
	gw.sidebarBtn.SetTooltipText("Collapse or Expand Tabs")
	gw.sidebarBtn.SetFocusOnClick(false)
	gw.sidebarBtn.SetActionName("win.toggle-tabs")

	// Title and a subtitle with the focused pane's directory and context
	// (container, SSH host, sudo), like Ptyxis.
	gw.headTitle = gtk.NewLabel("bunker")
	gw.headTitle.AddCSSClass("title")
	gw.headTitle.SetEllipsize(pango.EllipsizeEnd)
	gw.headSub = gtk.NewLabel("")
	gw.headSub.AddCSSClass("bunker-subtitle")
	gw.headSub.SetEllipsize(pango.EllipsizeMiddle)
	gw.headSub.SetVisible(false)
	titleBox := gtk.NewBox(gtk.OrientationVertical, 0)
	titleBox.SetVAlign(gtk.AlignCenter)
	titleBox.Append(gw.headTitle)
	titleBox.Append(gw.headSub)

	bar := gtk.NewHeaderBar()
	bar.SetTitleWidget(titleBox)
	bar.PackStart(gw.sidebarBtn)
	bar.PackStart(newTab)
	bar.PackEnd(button)
	return bar
}

func (gw *guiWin) installActions() {
	add := func(name string, accels []string, fn func()) {
		a := gio.NewSimpleAction(name, nil)
		a.ConnectActivate(func(*glib.Variant) { fn() })
		gw.win.AddAction(a)
		if len(accels) > 0 {
			gw.gapp.SetAccelsForAction("win."+name, accels)
		}
	}
	add("preferences", []string{"<Control>comma"}, gw.openSettings)
	add("open-config", nil, gw.openConfigFile)
	add("close-window", []string{"<Control><Shift>q"}, func() { gw.win.Close() })
	add("new-tab", []string{"<Control><Shift>t"}, func() { gw.newTab(gw.activeCwd(), nil) })
	add("close-tab", []string{"<Control><Shift>w"}, func() {
		if gw.active != nil {
			gw.closeTab(gw.active)
		}
	})
	add("toggle-tabs", nil, func() { gw.setCollapsed(!gw.collapsed) })
	add("next-tab", []string{"<Control>Page_Down"}, func() { gw.selectRelative(1) })
	add("prev-tab", []string{"<Control>Page_Up"}, func() { gw.selectRelative(-1) })
}

// ---------------------------------------------------------------------------
// Config file: write, watch, reload
// ---------------------------------------------------------------------------

func (gw *guiWin) path() string { return gw.cfg.Path }

// setKey writes section.key = literal into the config file and reloads.
func (gw *guiWin) setKey(section, key, literal string) error {
	changed, err := writeConfigKey(gw.path(), section, key, literal)
	if err == nil && changed {
		gw.reload()
	}
	return err
}

// watchConfig monitors the config directory (not the file: editors save by
// renaming a new file over the old one, which ends a file monitor).
func (gw *guiWin) watchConfig() {
	dir := filepath.Dir(gw.path())
	if err := os.MkdirAll(dir, 0o700); err != nil {
		L.Warn("config: cannot create config dir", "dir", dir, "err", err)
		return
	}
	mon, err := gio.NewFileForPath(dir).MonitorDirectory(context.Background(), gio.FileMonitorWatchMoves)
	if err != nil {
		L.Warn("config: cannot watch config dir", "dir", dir, "err", err)
		return
	}
	gw.monitor = mon // keep a reference or the monitor is collected
	name := filepath.Base(gw.path())
	gio.BaseFileMonitor(mon).ConnectChanged(func(file, other gio.Filer, event gio.FileMonitorEvent) {
		hit := file != nil && file.Basename() == name
		if other != nil && other.Basename() == name {
			hit = true
		}
		if !hit {
			return
		}
		switch event {
		case gio.FileMonitorEventChangesDoneHint, gio.FileMonitorEventCreated,
			gio.FileMonitorEventMovedIn, gio.FileMonitorEventRenamed, gio.FileMonitorEventDeleted:
			gw.scheduleReload()
		}
	})
}

func (gw *guiWin) scheduleReload() {
	if gw.reloadTimer != 0 {
		coreglib.SourceRemove(gw.reloadTimer)
	}
	gw.reloadTimer = coreglib.TimeoutAdd(guiReloadDebounce, func() bool {
		gw.reloadTimer = 0
		gw.reload()
		return false
	})
}

// reload re-reads the config and applies it. An invalid file keeps the
// current settings and shows why in the banner.
func (gw *guiWin) reload() {
	defer guiRecover("reload")
	cfg, err := LoadConfig(gw.configPath, gw.themeOverride)
	if err != nil {
		L.Warn("config: reload failed", "err", err)
		// LoadConfig wraps the parser error with the full path; the file
		// name is enough in a banner.
		msg := err.Error()
		if inner := errors.Unwrap(err); inner != nil {
			msg = filepath.Base(gw.path()) + ": " + strings.TrimPrefix(inner.Error(), "toml: ")
		}
		gw.showBanner("Config not applied (keeping previous settings). " + msg)
		return
	}
	gw.hideBanner()
	gw.apply(guiConfig(cfg))
}

// apply pushes a config into the running window and every tab.
func (gw *guiWin) apply(cfg Config) {
	themeChanged := cfg.Theme != gw.cfg.Theme
	if cfg.Tabs.Collapsed != gw.cfg.Tabs.Collapsed {
		gw.collapsed = cfg.Tabs.Collapsed // a changed default applies now
	}
	gw.cfg = cfg

	for _, t := range gw.tabs {
		app := t.app
		app.mu.Lock()
		app.theme = cfg.Theme
		app.keys = cfg.Keybindings
		app.scrollback = cfg.Scrollback // new panes; existing rings keep their size
		app.scrollbackBytes = cfg.ScrollbackBytes
		var panes []*Pane
		if app.root != nil {
			for _, leaf := range app.root.leaves() {
				panes = append(panes, leaf.pane)
			}
		}
		colors := app.paneOSCColors()
		app.mu.Unlock()
		if themeChanged {
			// OSC 10/11/12 replies should report the new colours.
			for _, p := range panes {
				p.mu.Lock()
				p.themeFGColor, p.themeBGColor, p.themeCursorColor = colors.fg, colors.bg, colors.cursor
				p.mu.Unlock()
			}
		}
		t.view.applyConfig(cfg)
	}
	if themeChanged {
		gw.themeCSS.LoadFromString(themeCSS(cfg.Theme))
		gw.applyDarkPreference()
	}
	gw.applyTabsLayout()
	if gw.settings != nil {
		gw.settings.load(cfg)
	}
	L.Info("config: applied", "theme", cfg.ThemeName, "font", cfg.Font, "padding", cfg.Padding, "tabs", cfg.Tabs.Position)
}

// setWindowTitle shows the active tab in the header bar and window title.
func (gw *guiWin) setWindowTitle(info tabInfo) {
	gw.win.SetTitle(info.title)
	gw.headTitle.SetText(info.title)
	var sub []string
	if info.cwd != "" {
		sub = append(sub, tildePath(info.cwd))
	}
	if info.context != "" {
		sub = append(sub, info.context)
	}
	gw.headSub.SetText(strings.Join(sub, "  ·  "))
	gw.headSub.SetVisible(len(sub) > 0)
}

func (gw *guiWin) applyDarkPreference() {
	if s := gtk.SettingsGetDefault(); s != nil {
		r, g, b := gw.cfg.Theme.bg.RGB()
		s.SetObjectProperty("gtk-application-prefer-dark-theme", r*299+g*587+b*114 < 128_000)
	}
}

func (gw *guiWin) openConfigFile() {
	path := gw.path()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			os.WriteFile(path, []byte(DefaultConfigTOML()), 0o600) //nolint:errcheck
		}
	}
	launcher := gtk.NewFileLauncher(gio.NewFileForPath(path))
	launcher.Launch(context.Background(), &gw.win.Window, func(res gio.AsyncResulter) {
		if err := launcher.LaunchFinish(res); err != nil {
			gw.showBanner("Could not open " + path + ": " + err.Error())
		}
	})
}

func (gw *guiWin) showBanner(msg string) {
	gw.banner.SetText(msg)
	gw.banner.SetVisible(true)
	if gw.bannerTimer != 0 {
		coreglib.SourceRemove(gw.bannerTimer)
	}
	gw.bannerTimer = coreglib.TimeoutSecondsAdd(guiBannerTimeout, func() bool {
		gw.bannerTimer = 0
		gw.banner.SetVisible(false)
		return false
	})
}

func (gw *guiWin) hideBanner() {
	if gw.banner != nil {
		gw.banner.SetVisible(false)
	}
}

func guiInstallCSS() {
	css := gtk.NewCSSProvider()
	css.LoadFromString(`
.bunker-banner {
	background-color: #c01c28;
	color: white;
	padding: 8px 14px;
	border-radius: 8px;
	box-shadow: 0 2px 8px rgba(0, 0, 0, 0.35);
}
.bunker-page-title {
	font-size: 1.5em;
	font-weight: 800;
}
.bunker-group-title {
	font-weight: bold;
	margin-bottom: 2px;
}
.bunker-dim {
	opacity: 0.65;
	font-size: 0.92em;
}
list.bunker-card {
	background-color: alpha(currentColor, 0.05);
	border: 1px solid alpha(currentColor, 0.10);
	border-radius: 12px;
	margin-top: 4px;
}
list.bunker-card > row {
	border-bottom: 1px solid alpha(currentColor, 0.08);
	background: none;
}
list.bunker-card > row:last-child {
	border-bottom: none;
}
.bunker-row {
	padding: 10px 14px;
	min-height: 34px;
}
.bunker-key {
	font-family: monospace;
	opacity: 0.8;
}
.bunker-settings-error {
	background-color: #c01c28;
	color: white;
	padding: 8px 14px;
}
.bunker-settings-sidebar {
	padding: 8px 0;
}
`)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

// sleepRenderSettle lets erase-then-redraw bursts land in one frame, as the
// TUI render loop does.
func sleepRenderSettle() { time.Sleep(renderSettleInterval) }
