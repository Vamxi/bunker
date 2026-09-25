// cmd.go - cobra command definitions for bunk.
//
// main() in main.go calls Execute(), which hands off to the root command.
// The actual startup logic lives in run().
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"

	"github.com/gdamore/tcell/v2"
	"github.com/spf13/cobra"
)

var (
	flagConfig string
	flagTheme  string
	flagDebug  bool
	flagTrace  bool

	// Version is set at build time via -ldflags "-X main.Version=x.y.z".
	Version = "dev"
)

// bunkLogo returns the coloured ASCII art logo with the version appended.
func bunkLogo() string {
	const (
		cyan  = "\033[96m"
		blue  = "\033[94m"
		reset = "\033[0m"
	)
	return cyan + `   __                __   ` + reset + "\n" +
		cyan + `  / /_  __  ______  / /__ ` + reset + "\n" +
		blue + ` / __ \/ / / / __ \/ //_/ ` + reset + "\n" +
		blue + `/ /_/ / /_/ / / / / ,<    ` + reset + "\n" +
		blue + `\____/\__,_/_/ /_/_/|_|   ` + reset + " " + cyan + Version + reset + "\n"
}

func init() {
	rootCmd.AddCommand(tuiCmd)
	rootCmd.PersistentFlags().StringVarP(&flagConfig, "config", "c", "", "config file path (default: ~/.config/bunker/config.toml)")
	rootCmd.PersistentFlags().StringVar(&flagTheme, "theme", "", "theme name (see bunker themes)")
	// Declared here so cobra does not add a -v shorthand.
	rootCmd.Flags().Bool("version", false, "Print the version and exit.")
	rootCmd.PersistentFlags().BoolVarP(&flagDebug, "debug", "d", false, "Debug-level logging on stderr.")
	rootCmd.PersistentFlags().BoolVar(&flagTrace, "trace", false, "Trace-level logs to "+defaultTraceFile+" (truncated each run; includes terminal output).")

	// Override help to load the config first so effective (user-overridden)
	// keybindings are shown rather than built-in defaults.
	rootCmd.SetHelpFunc(func(cmd *cobra.Command, args []string) {
		cfg, err := LoadConfig(flagConfig, flagTheme)
		if err != nil {
			cmd.PrintErrln(err)
			return
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s\n%s\nUsage:\n  %s\n\nFlags:\n%s",
			bunkLogo(),
			keybindingsHelpText(&cfg.Keybindings),
			cmd.UseLine(),
			cmd.Flags().FlagUsages(),
		); err != nil {
			L.Log(context.Background(), LevelTrace, "help: write", "err", err)
		}
	})
}

var rootCmd = &cobra.Command{
	Use:          "bunker [-- command [args...]]",
	Version:      Version,
	Short:        "A light, fast GTK4 terminal",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return runGUI(flagConfig, flagTheme, flagDebug, flagTrace, args)
	},
}

// tuiCmd keeps bunk's terminal multiplexer available inside a terminal.
var tuiCmd = &cobra.Command{
	Use:          "tui",
	Short:        "Run the bunk terminal multiplexer in the current terminal",
	SilenceUsage: true,
	RunE: func(cmd *cobra.Command, args []string) error {
		return run(flagConfig, flagTheme, flagDebug, flagTrace)
	},
}

// Execute is called by main.  It runs the cobra command tree.
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

// run initialises the screen, spawns the first pane, and blocks until the
// user quits.  All terminal cleanup happens synchronously after the event
// loop returns so it is guaranteed to run before the process exits.
func run(configPath, themeName string, debug, trace bool) error {
	cfg, err := LoadConfig(configPath, themeName)
	if err != nil {
		return err
	}

	// Prevent nested sessions: BUNK=1 is set in every pane's environment.
	if os.Getenv("BUNK") != "" {
		fmt.Fprintf(os.Stderr, "%s\nAlready inside a bunk session (BUNK environment variable is set).\n\n%s\n",
			bunkLogo(), keybindingsHelpText(&cfg.Keybindings))
		os.Exit(1)
	}

	cleanup := initLogger(logOptions{Debug: debug, Trace: trace, TracePath: cfg.LogFile, Level: cfg.LogLevel})
	defer cleanup()

	L.Info("bunk starting", "theme", themeName)

	// Query cell aspect ratio BEFORE screen.Init() — after Init tcell owns
	// stdin and we must not read from it directly.
	cellAspect := queryCellAspect(cfg.CellAspect)
	L.Debug("startup: cell aspect ratio", "aspect", cellAspect)

	hostColors := hostOSCColors{}
	if needsHostOSCColorProbe(cfg.Theme) {
		hostColors = probeHostOSCColors()
	}

	screen, err := tcell.NewScreen()
	if err != nil {
		return fmt.Errorf("open terminal: %w", err)
	}
	if err := screen.Init(); err != nil {
		return fmt.Errorf("initialise terminal: %w", err)
	}
	screen.SetStyle(tcell.StyleDefault.Background(cfg.Theme.bg).Foreground(cfg.Theme.fg))
	screen.HideCursor()
	screen.Clear()

	app := &App{
		screen:          screen,
		theme:           cfg.Theme,
		hostOSCColors:   hostColors,
		cellAspect:      cellAspect,
		keys:            cfg.Keybindings,
		scrollback:      cfg.Scrollback,
		scrollbackBytes: cfg.ScrollbackBytes,
		redraw:          make(chan struct{}, 1),
		paneDead:        make(chan *Pane, 8),
		done:            make(chan struct{}),
		oscBuf:          newOSCBuffer(),
	}
	app.post = func(f func()) {
		ev := &uiFuncEvent{f: f}
		ev.SetEventNow()
		if err := screen.PostEvent(ev); err != nil {
			L.Warn("post to the event loop", "err", err)
		}
	}

	screen.EnableMouse(tcell.MouseMotionEvents)
	screen.EnablePaste()
	screen.EnableFocus()

	// Sync before querying size: Init() may capture a stale TIOCGWINSZ
	// snapshot if the terminal just went fullscreen.
	screen.Sync()
	w, h := screen.Size()
	L.Debug("startup: screen size", "w", w, "h", h)

	p, err := NewPane(
		app.nextID, 0, 0, w, h, app.scrollback, app.scrollbackBytes, "", nil,
		app.paneOSCColors(),
		app.redraw, app.paneDead, app.done, app.oscBuf,
		app.cellAspect,
	)
	if err != nil {
		screen.Fini()
		return fmt.Errorf("start the first pane: %w", err)
	}
	app.nextID++
	app.root = newLeaf(p, 0, 0, w, h)
	app.active = p

	go app.deathWatcher()
	app.renderWg.Add(1)
	go app.renderLoop()

	app.eventLoop()

	// Terminal cleanup runs HERE, synchronously in the main goroutine, AFTER
	// Fini() has already been called.  This is the only safe place: when the
	// user exits via Ctrl+D / `exit` (last pane dies), shutdown() is invoked
	// from a background goroutine whose remaining code is killed as soon as
	// main() returns.  Fini() causes PollEvent to return nil which unblocks
	// eventLoop, so by the time we reach this line Fini() is guaranteed done.
	const vtreset = "\033[?2004l" + // bracketed-paste off
		"\033[?1004l" + // focus-events off
		"\033[?1003l\033[?1002l\033[?1000l" + // all mouse modes off
		"\033[?1006l" + // SGR mouse extension off
		"\033[?1049l" + // exit alternate screen
		"\033[?25h" + // show cursor
		"\033]112\007" + // restore outer terminal cursor colour
		"\033[0m" + // reset all SGR attributes
		"\033[2J\033[H" // clear screen + cursor home
	out := os.Stdout
	if tty, err := os.OpenFile("/dev/tty", os.O_WRONLY, 0); err == nil {
		out = tty
		defer func() {
			if err := tty.Close(); err != nil {
				L.Log(context.Background(), LevelTrace, "restore terminal: close tty", "err", err)
			}
		}()
	}
	if _, err := out.WriteString(vtreset); err != nil {
		L.Log(context.Background(), LevelTrace, "restore terminal: reset", "err", err)
	}
	if err := exec.Command("stty", "sane").Run(); err != nil {
		L.Log(context.Background(), LevelTrace, "restore terminal: stty sane", "err", err)
	}
	return nil
}
