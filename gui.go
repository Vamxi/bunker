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
	"runtime"
	"strings"
	"sync/atomic"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
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

	nextTabID int
	// Tests replace these: whether the window has focus, and sending a
	// desktop notification.
	focusedFn func() bool
	sendFn    func(id string, n *gio.Notification)
}

// runGUI starts the GTK application. command, when non-empty, replaces the
// login shell in the first tab.
func runGUI(configPath, themeName string, debug, trace bool, command []string) error {
	cfg, err := LoadConfig(configPath, themeName)
	if err != nil {
		return err
	}
	cleanup := initLogger(logOptions{Debug: debug, Trace: trace, TracePath: cfg.LogFile, Level: cfg.LogLevel, Stderr: os.Stderr})
	defer cleanup()

	cfg = guiConfig(cfg)
	L.Info("bunker starting", "config", cfg.Path, "font", cfg.Font)

	stopProfile := guiStartProfile()
	defer stopProfile()

	var windows []*guiWin
	gapp := gtk.NewApplication(guiAppID, gio.ApplicationNonUnique)
	gapp.ConnectActivate(func() {
		gw := &guiWin{gapp: gapp, cfg: cfg, configPath: configPath, themeOverride: themeName, collapsed: cfg.Tabs.Collapsed}
		windows = append(windows, gw)
		gw.build(command)
	})
	stopWatch := make(chan struct{})
	go watchUIThread(&uiHeartbeat, guiStallCheck, guiStallAfter, reportUIStall, stopWatch)
	code := gapp.Run([]string{os.Args[0]})
	close(stopWatch)

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
	switch {
	case cfg.ThemeName == systemThemeName:
		name := "gnome-light"
		if guiDesktopPrefersDark() {
			name = "gnome"
		}
		cfg.Theme = resolveTheme(BuiltinThemes[name])
	case cfg.Theme.bg == tcell.ColorDefault || cfg.Theme.fg == tcell.ColorDefault:
		cfg.Theme = resolveTheme(BuiltinThemes[defaultThemeName])
		cfg.ThemeName = defaultThemeName
	}
	if font := os.Getenv("BUNKER_FONT"); font != "" {
		cfg.Font = font
	}
	return cfg
}

func (gw *guiWin) build(command []string) {
	guiApplyTheme(gw.cfg.Theme)

	win := gtk.NewApplicationWindow(gw.gapp)
	win.SetTitle("bunker")
	win.AddCSSClass("bunker-window")
	gw.win = win
	gw.installActions()
	gw.installFocusTab()
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
	if gw.active != nil {
		gw.active.view.GrabFocus()
	}
	gw.runDebugHooks()
	gw.watchConfig()
	gw.pollTitles()
	gw.showKeyProblems(gw.cfg)
	// The system theme follows the desktop's light/dark switch live.
	if s := gtk.SettingsGetDefault(); s != nil {
		s.NotifyProperty("gtk-interface-color-scheme", func() {
			if gw.cfg.ThemeName == systemThemeName {
				gw.reload()
			}
		})
	}
}

// guiDesktopPrefersDark reports the desktop's colour scheme (GTK reads it
// from the settings portal). No preference means light, as on GNOME; a
// desktop without the setting gets dark, the usual terminal look.
func guiDesktopPrefersDark() bool {
	s := gtk.SettingsGetDefault()
	if s == nil {
		return true
	}
	scheme, _ := s.ObjectProperty("gtk-interface-color-scheme").(gtk.InterfaceColorScheme)
	switch scheme {
	case gtk.InterfaceColorSchemeLight, gtk.InterfaceColorSchemeDefault:
		return false
	}
	return true
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
	add := func(name string, fn func()) {
		a := gio.NewSimpleAction(name, nil)
		a.ConnectActivate(func(*glib.Variant) { fn() })
		gw.win.AddAction(a)
	}
	add("preferences", gw.openSettings)
	add("open-config", gw.openConfigFile)
	add("close-window", func() { gw.win.Close() })
	add("new-tab", func() { gw.newTab(gw.activeCwd(), nil) })
	add("close-tab", func() {
		if gw.active != nil {
			gw.closeTab(gw.active)
		}
	})
	add("toggle-tabs", func() { gw.setCollapsed(!gw.collapsed) })
	add("next-tab", func() { gw.selectRelative(1) })
	add("prev-tab", func() { gw.selectRelative(-1) })
	gw.applyAccels(gw.cfg)
}

// windowAccelActions are the window shortcuts that are also application
// accelerators, so they work while a text field (tab rename) has focus.
// In the terminal the view matches them first (termView.windowShortcut).
var windowAccelActions = map[string]string{
	"new_tab": "win.new-tab", "close_tab": "win.close-tab", "next_tab": "win.next-tab",
	"prev_tab": "win.prev-tab", "preferences": "win.preferences", "close_window": "win.close-window",
}

func (gw *guiWin) applyAccels(cfg Config) {
	for action, gaction := range windowAccelActions {
		var accels []string
		if a := shortcutAccel(cfg.WindowKeys[action]); a != "" {
			accels = []string{a}
		}
		gw.gapp.SetAccelsForAction(gaction, accels)
	}
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
	gw.showKeyProblems(cfg)
}

// showKeyProblems puts invalid or doubled shortcuts in the banner.
func (gw *guiWin) showKeyProblems(cfg Config) {
	if len(cfg.KeyProblems) == 0 {
		return
	}
	for _, p := range cfg.KeyProblems {
		L.Warn("config: shortcut", "problem", p)
	}
	gw.showBanner("Shortcuts: " + strings.Join(cfg.KeyProblems, ". ") + ".")
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
		guiApplyTheme(cfg.Theme)
	}
	gw.applyTabsLayout()
	gw.applyAccels(cfg)
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

func (gw *guiWin) openConfigFile() {
	path := gw.path()
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err == nil {
			if err := os.WriteFile(path, []byte(DefaultConfigTOML()), 0o600); err != nil {
				L.Warn("gui: write default config", "path", path, "err", err)
			}
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

// sleepRenderSettle lets erase-then-redraw bursts land in one frame, as the
// TUI render loop does.
func sleepRenderSettle() { time.Sleep(renderSettleInterval) }

// The title poll beats uiHeartbeat once a second on the GTK thread. If it
// stops for guiStallAfter, something blocked the thread (a synchronous
// command, a lock held too long) and the window is frozen: the stacks
// logged then show what it is waiting on.
const (
	guiStallCheck = 2 * time.Second
	guiStallAfter = 5 * time.Second
)

var uiHeartbeat atomic.Int64

// watchUIThread reports each stall of the heartbeat once, with every
// goroutine's stack, and again when the thread recovers.
func watchUIThread(beat *atomic.Int64, every, stallAfter time.Duration, report func(stalled time.Duration, stacks []byte), done <-chan struct{}) {
	tick := time.NewTicker(every)
	defer tick.Stop()
	stalled := false
	for {
		select {
		case <-done:
			return
		case now := <-tick.C:
			last := beat.Load()
			if last == 0 {
				continue // the window has not started beating yet
			}
			behind := now.Sub(time.Unix(0, last))
			switch {
			case behind >= stallAfter && !stalled:
				stalled = true
				buf := make([]byte, 1<<20)
				report(behind, buf[:runtime.Stack(buf, true)])
			case behind < stallAfter && stalled:
				stalled = false
				report(0, nil)
			}
		}
	}
}

func reportUIStall(stalled time.Duration, stacks []byte) {
	if stacks == nil {
		L.Warn("gui: window responding again")
		return
	}
	L.Warn("gui: window not responding", "for", stalled.Round(time.Second), "goroutines", string(stacks))
}
