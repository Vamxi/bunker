// gui.go - bunker's GTK4 application shell.
//
// runGUI owns the GTK main loop. Panes, PTYs, and emulation are bunk's; the
// App struct is reused as the pane model (root, active, redraw, done) with no
// tcell screen attached. GTK calls stay on the main thread: background
// goroutines only reach it through glib.IdleAdd.
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
	app  *App
	win  *gtk.ApplicationWindow
	view *termView

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
// login shell in the first pane.
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

	app := &App{
		theme:           cfg.Theme,
		keys:            cfg.Keybindings,
		scrollback:      cfg.Scrollback,
		scrollbackBytes: cfg.ScrollbackBytes,
		redraw:          make(chan struct{}, 1),
		paneDead:        make(chan *Pane, 8),
		done:            make(chan struct{}),
		oscBuf:          newOSCBuffer(),
	}

	stopProfile := guiStartProfile()
	defer stopProfile()

	gapp := gtk.NewApplication(guiAppID, gio.ApplicationNonUnique)
	gapp.ConnectActivate(func() {
		gw := &guiWin{gapp: gapp, app: app, cfg: cfg, configPath: configPath, themeOverride: themeName}
		gw.build(command)
	})
	code := gapp.Run([]string{os.Args[0]})

	app.shutOnce.Do(func() {
		app.closeAllPanes()
		close(app.done)
	})
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
	app := gw.app
	guiInstallCSS()
	gw.applyDarkPreference()

	win := gtk.NewApplicationWindow(gw.gapp)
	win.SetTitle("bunker")
	gw.win = win
	gw.installActions()
	win.SetTitlebar(gw.headerBar())

	view := newTermView(app, gw.cfg)
	gw.view = view
	view.win = win
	view.onTitle = func(title string) {
		if title == "" {
			title = "bunker"
		}
		win.SetTitle(title)
	}
	view.onSpawn = func(cols, rows int) (*Pane, error) {
		// Pane widths include bunk's reserved scrollbar column.
		p, err := NewPane(app.nextID, 0, 0, cols+1, rows, app.scrollback, app.scrollbackBytes, "", command,
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
		return p, nil
	}

	// The banner floats over the terminal for config errors.
	overlay := gtk.NewOverlay()
	overlay.SetChild(view)
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
	win.Present()
	view.GrabFocus()
	if os.Getenv("BUNKER_OPEN") == "preferences" {
		gw.openSettings()
	}
	guiScheduleScreenshot(win, func() *gtk.Window {
		if gw.settings != nil && gw.settings.win.IsVisible() {
			return gw.settings.win
		}
		return &win.Window
	})
	gw.watchConfig()

	go guiRedrawBridge(app, view)
	go func() {
		select {
		case p := <-app.paneDead:
			L.Info("gui: shell exited, closing window", "pane", p.id)
			coreglib.IdleAdd(func() { win.Close() })
		case <-app.done:
		}
	}()
}

func (gw *guiWin) headerBar() *gtk.HeaderBar {
	menu := gio.NewMenu()
	menu.Append("Preferences", "win.preferences")
	menu.Append("Open Config File", "win.open-config")
	button := gtk.NewMenuButton()
	button.SetIconName("open-menu-symbolic")
	button.SetMenuModel(menu)
	button.SetTooltipText("Main Menu")
	button.SetFocusOnClick(false)

	bar := gtk.NewHeaderBar()
	bar.PackEnd(button)
	return bar
}

func (gw *guiWin) installActions() {
	prefs := gio.NewSimpleAction("preferences", nil)
	prefs.ConnectActivate(func(*glib.Variant) { gw.openSettings() })
	gw.win.AddAction(prefs)

	open := gio.NewSimpleAction("open-config", nil)
	open.ConnectActivate(func(*glib.Variant) { gw.openConfigFile() })
	gw.win.AddAction(open)

	gw.gapp.SetAccelsForAction("win.preferences", []string{"<Control>comma"})
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

// apply pushes a config into the running window.
func (gw *guiWin) apply(cfg Config) {
	themeChanged := cfg.Theme != gw.cfg.Theme
	gw.cfg = cfg

	app := gw.app
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
		gw.applyDarkPreference()
	}
	gw.view.applyConfig(cfg)
	if gw.settings != nil {
		gw.settings.load(cfg)
	}
	L.Info("config: applied", "theme", cfg.ThemeName, "font", cfg.Font, "padding", cfg.Padding)
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
.bunker-settings-heading {
	font-weight: bold;
	margin-top: 12px;
}
.bunker-settings-hint {
	opacity: 0.7;
	font-size: smaller;
}
`)
	gtk.StyleContextAddProviderForDisplay(gdk.DisplayGetDefault(), css, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
}

// guiRedrawBridge turns pane redraw signals into (coalesced) GTK draws. The
// short settle wait lets erase-then-redraw bursts land in one frame, as the
// TUI render loop does.
func guiRedrawBridge(app *App, view *termView) {
	for {
		select {
		case <-app.redraw:
			time.Sleep(renderSettleInterval)
			drainRedraw(app.redraw)
			view.requestDraw()
		case <-app.done:
			return
		}
	}
}
