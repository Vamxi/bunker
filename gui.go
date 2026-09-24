// gui.go - bunker's GTK4 application shell.
//
// runGUI owns the GTK main loop. Panes, PTYs, and emulation are bunk's; the
// App struct is reused as the pane model (root, active, redraw, done) with no
// tcell screen attached. GTK calls stay on the main thread: background
// goroutines only reach it through glib.IdleAdd.
package main

import (
	"os"
	"time"

	coreglib "github.com/diamondburned/gotk4/pkg/core/glib"
	"github.com/diamondburned/gotk4/pkg/gio/v2"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
	"github.com/gdamore/tcell/v2"
)

const (
	guiAppID       = "dev.bunker.Bunker"
	guiDefaultFont = "Monospace 11"
)

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

	// The "terminal" theme inherits host colours; a GUI has no host.
	theme := cfg.Theme
	if theme.bg == tcell.ColorDefault || theme.fg == tcell.ColorDefault {
		theme = resolveTheme(BuiltinThemes["default"])
	}
	font := os.Getenv("BUNKER_FONT")
	if font == "" {
		font = guiDefaultFont
	}
	L.Info("bunker starting", "font", font, "log_level", logLevel)

	app := &App{
		theme:           theme,
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
	gapp.ConnectActivate(func() { guiActivate(gapp, app, font, command) })
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

func guiActivate(gapp *gtk.Application, app *App, font string, command []string) {
	if s := gtk.SettingsGetDefault(); s != nil {
		r, g, b := app.theme.bg.RGB()
		s.SetObjectProperty("gtk-application-prefer-dark-theme", r*299+g*587+b*114 < 128_000)
	}

	win := gtk.NewApplicationWindow(gapp)
	win.SetTitle("bunker")

	view := newTermView(app, font)
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

	win.SetChild(view)
	win.Present()
	view.GrabFocus()
	guiScheduleScreenshot(win)

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
