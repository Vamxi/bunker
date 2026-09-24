// config.go - configuration loading and theme registry.
//
// Config file location: ~/.config/bunker/config.toml (XDG_CONFIG_HOME respected).
//
// Built-in themes:
//
//	terminal       - uses your terminal's native colors (no overrides)
//	default        - yauhen.cc Tilix palette (dark, cyan accent)
//	solarized-dark - Solarized Dark by Ethan Schoonover
//	dracula        - Dracula by Zeno Rocha
//	nord           - Nord by Arctic Ice Studio
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"
	"github.com/gdamore/tcell/v2"
)

// ---------------------------------------------------------------------------
// TOML file structure
// ---------------------------------------------------------------------------

type fileConfig struct {
	Theme        string            `toml:"theme"`
	LogFile      string            `toml:"log_file"`
	LogLevel     string            `toml:"log_level"`
	CellAspect   float64           `toml:"cell_aspect"`   // cell pixel H/W ratio; 0 = auto-detect
	Scrollback   int               `toml:"scrollback"`    // max scrollback lines per pane; 0 = default
	ScrollbackMB float64           `toml:"scrollback_mb"` // optional per-pane memory cap; 0 = lines only
	Font         string            `toml:"font"`          // GUI: Pango font description, e.g. "JetBrains Mono 12"
	Window       windowConfig      `toml:"window"`        // GUI window options
	Tabs         tabsConfig        `toml:"tabs"`          // GUI tab bar options
	UI           uiOverride        `toml:"ui"`
	Keys         map[string]string `toml:"keys"` // action → key string, e.g. "split" → "f1"
}

type windowConfig struct {
	Padding int `toml:"padding"` // pixels around the terminal grid
}

type tabsConfig struct {
	Position string `toml:"position"` // left | right | top | bottom
	Width    int    `toml:"width"`    // sidebar width in pixels when left/right
}

// Tab bar positions accepted by [tabs] position.
var tabPositions = []string{"left", "right", "top", "bottom"}

const (
	defaultFont      = "Monospace 11"
	defaultPadding   = 8
	defaultTabsWidth = 220
	maxPadding       = 64
	minTabsWidth     = 120
	maxTabsWidth     = 600
	defaultTabsSide  = "left"
	defaultThemeName = "default"
	configDirName    = "bunker"
)

type uiOverride struct {
	ActiveBorder   string `toml:"active_border"`
	InactiveBorder string `toml:"inactive_border"`
	ScrollThumb    string `toml:"scrollbar_thumb"`
	ScrollTrack    string `toml:"scrollbar_track"`
}

// keybindingNames maps lowercase TOML key names to their tcell.Key constant.
// Package-level so it is allocated once.
var keybindingNames = map[string]tcell.Key{
	"f1": tcell.KeyF1, "f2": tcell.KeyF2, "f3": tcell.KeyF3,
	"f4": tcell.KeyF4, "f5": tcell.KeyF5, "f6": tcell.KeyF6,
	"f7": tcell.KeyF7, "f8": tcell.KeyF8, "f9": tcell.KeyF9,
	"f10": tcell.KeyF10, "f11": tcell.KeyF11, "f12": tcell.KeyF12,
	"up": tcell.KeyUp, "down": tcell.KeyDown,
	"left": tcell.KeyLeft, "right": tcell.KeyRight,
	"pgup": tcell.KeyPgUp, "pageup": tcell.KeyPgUp,
	"pgdn": tcell.KeyPgDn, "pagedown": tcell.KeyPgDn,
	"home": tcell.KeyHome, "end": tcell.KeyEnd,
	"enter": tcell.KeyEnter, "return": tcell.KeyEnter,
	"escape": tcell.KeyEsc, "esc": tcell.KeyEsc,
	"backspace": tcell.KeyBackspace2,
	"delete":    tcell.KeyDelete, "del": tcell.KeyDelete,
	"tab":    tcell.KeyTab,
	"insert": tcell.KeyInsert,
}

// ---------------------------------------------------------------------------
// Keybinding type
// ---------------------------------------------------------------------------

// Keybinding represents one resolved key combination (key code + modifiers).
//
// For most special keys (F1, arrows, Esc, …) only key+mod matter.  For
// modifier+letter combinations the encoding depends on the modifier:
//
//   - Ctrl+letter is its own tcell.Key (KeyCtrlA … KeyCtrlZ) with mod=0.
//   - Alt+letter is delivered as KeyRune with the letter rune and ModAlt;
//     in that case `r` carries the letter so Matches can compare it.
type Keybinding struct {
	key tcell.Key
	mod tcell.ModMask
	r   rune   // only meaningful when key == tcell.KeyRune (e.g. alt+letter)
	raw string // original string, for display
}

// Matches returns true if ev matches this keybinding.
func (kb Keybinding) Matches(ev *tcell.EventKey) bool {
	if ev.Key() != kb.key {
		return false
	}
	// For rune-keys (alt+letter), the actual letter must match.  We normalise
	// to lowercase so users typing "alt+r" config still match Alt+r typed at
	// the terminal regardless of caps-lock state.
	if kb.key == tcell.KeyRune {
		evRune := ev.Rune()
		if evRune >= 'A' && evRune <= 'Z' {
			evRune += 'a' - 'A'
		}
		if evRune != kb.r {
			return false
		}
	}
	evMod := ev.Modifiers()
	// ctrl+letter keys (ctrl+a … ctrl+z) have the Ctrl baked into the key
	// code by tcell; the ModCtrl bit may still be set in Modifiers() depending
	// on the platform/terminal.  Strip it before comparing so that the stored
	// mod=0 (set by parseKey) still matches.
	if kb.key >= tcell.KeyCtrlA && kb.key <= tcell.KeyCtrlZ {
		evMod &^= tcell.ModCtrl
	}
	return evMod == kb.mod
}

// String returns the human-readable key description.
func (kb Keybinding) String() string { return kb.raw }

// parseKey converts a key description string such as "f1", "ctrl+q",
// "alt+up", or "shift+pgup" into a Keybinding.
//
// Modifier prefixes (case-insensitive): ctrl, alt, shift.
// Key names (case-insensitive):
//
//	f1-f12, up, down, left, right, pgup/pageup, pgdn/pagedown,
//	home, end, enter/return, escape/esc, backspace, delete/del, tab, insert.
//
// Ctrl+letter combinations (e.g. ctrl+c) are represented as tcell.KeyCtrlC
// with no extra modifier bits, matching what tcell actually produces.
func parseKey(s string) (Keybinding, error) {
	orig := s
	s = strings.ToLower(strings.TrimSpace(s))

	parts := strings.Split(s, "+")
	if len(parts) == 0 || (len(parts) == 1 && parts[0] == "") {
		return Keybinding{}, fmt.Errorf("empty key binding")
	}

	var mod tcell.ModMask
	keyName := parts[len(parts)-1]
	for _, p := range parts[:len(parts)-1] {
		switch p {
		case "ctrl":
			mod |= tcell.ModCtrl
		case "alt":
			mod |= tcell.ModAlt
		case "shift":
			mod |= tcell.ModShift
		default:
			return Keybinding{}, fmt.Errorf("unknown modifier %q in keybinding %q", p, orig)
		}
	}

	// ctrl+<letter>: tcell represents these as KeyCtrlA-KeyCtrlZ with no
	// separate ModCtrl bit.
	if mod&tcell.ModCtrl != 0 && len(keyName) == 1 && keyName[0] >= 'a' && keyName[0] <= 'z' {
		key := tcell.Key(keyName[0]-'a') + tcell.KeyCtrlA
		return Keybinding{key: key, mod: mod &^ tcell.ModCtrl, raw: orig}, nil
	}

	// alt+<letter> (and bare letter, though we have no defaults using it):
	// tcell delivers these as KeyRune with the letter rune and the Alt bit
	// in Modifiers().
	if len(keyName) == 1 && keyName[0] >= 'a' && keyName[0] <= 'z' {
		return Keybinding{key: tcell.KeyRune, r: rune(keyName[0]), mod: mod, raw: orig}, nil
	}

	key, ok := keybindingNames[keyName]
	if !ok {
		return Keybinding{}, fmt.Errorf("unknown key name %q in keybinding %q", keyName, orig)
	}
	return Keybinding{key: key, mod: mod, raw: orig}, nil
}

// mustParseKey parses a key string and panics on error (used only for
// built-in defaults which are always valid).
func mustParseKey(s string) Keybinding {
	kb, err := parseKey(s)
	if err != nil {
		panic("bunk: bad built-in keybinding: " + err.Error())
	}
	return kb
}

// Keybindings holds one resolved Keybinding per named action.
type Keybindings struct {
	Split        Keybinding
	SplitContext Keybinding
	Quit         Keybinding
	Copy         Keybinding
	Paste        Keybinding
	Search       Keybinding
	Passthrough  Keybinding
	Zoom         Keybinding
	NavUp        Keybinding
	NavDown      Keybinding
	NavLeft      Keybinding
	NavRight     Keybinding
	ScrollUp     Keybinding
	ScrollDown   Keybinding
	SearchNext   Keybinding
	SearchPrev   Keybinding
	SearchExit   Keybinding
	ReinitHost   Keybinding
}

// keybindingDefaults is the single source of truth for action names, their
// struct field pointers, default key strings, and help descriptions.
// Used by resolveKeybindings() and keybindingsHelpText() — add new actions here only.
type kbEntry struct {
	action string
	field  func(*Keybindings) *Keybinding
	def    string
	desc   string // shown in --help output
}

var keybindingDefaults = []kbEntry{
	{"passthrough", func(k *Keybindings) *Keybinding { return &k.Passthrough }, "ctrl+f12", "Toggle keyboard passthrough for the active pane (PASS badge)"},
	{"split", func(k *Keybindings) *Keybinding { return &k.Split }, "f1", "Auto-split the active pane (vertical if wide, horizontal if tall)"},
	{"split_context", func(k *Keybindings) *Keybinding { return &k.SplitContext }, "alt+f1", "Split inheriting container / SSH / sudo context"},
	{"zoom", func(k *Keybindings) *Keybinding { return &k.Zoom }, "f12", "Toggle fullscreen zoom on the active pane"},
	{"nav_up", func(k *Keybindings) *Keybinding { return &k.NavUp }, "alt+up", "Move focus to the pane above"},
	{"nav_down", func(k *Keybindings) *Keybinding { return &k.NavDown }, "alt+down", "Move focus to the pane below"},
	{"nav_left", func(k *Keybindings) *Keybinding { return &k.NavLeft }, "alt+left", "Move focus to the pane on the left"},
	{"nav_right", func(k *Keybindings) *Keybinding { return &k.NavRight }, "alt+right", "Move focus to the pane on the right"},
	{"scroll_up", func(k *Keybindings) *Keybinding { return &k.ScrollUp }, "shift+pgup", "Scroll back through pane history"},
	{"scroll_down", func(k *Keybindings) *Keybinding { return &k.ScrollDown }, "shift+pgdn", "Scroll forward / return to live output"},
	{"search", func(k *Keybindings) *Keybinding { return &k.Search }, "ctrl+f", "Incremental search in the active pane"},
	{"search_next", func(k *Keybindings) *Keybinding { return &k.SearchNext }, "ctrl+n", "Next search match (also: Enter)"},
	{"search_prev", func(k *Keybindings) *Keybinding { return &k.SearchPrev }, "ctrl+p", "Previous search match"},
	{"search_exit", func(k *Keybindings) *Keybinding { return &k.SearchExit }, "escape", "Close search bar"},
	{"copy", func(k *Keybindings) *Keybinding { return &k.Copy }, "ctrl+c", "Copy selection; forwards Ctrl+C to shell if nothing selected"},
	{"paste", func(k *Keybindings) *Keybinding { return &k.Paste }, "ctrl+v", "Paste from system clipboard"},
	{"quit", func(k *Keybindings) *Keybinding { return &k.Quit }, "ctrl+q", "Quit bunk"},
	{"reinit_host", func(k *Keybindings) *Keybinding { return &k.ReinitHost }, "alt+r", "Re-initialise host terminal (use after binary corruption)"},
}

// keybindingsHelpText returns a formatted key-bindings table for --help.
// If kb is non-nil the effective (possibly user-overridden) key strings are
// shown; otherwise the built-in defaults are used (e.g. for --help before
// the config is loaded).
func keybindingsHelpText(kb *Keybindings) string {
	var b strings.Builder
	b.WriteString("Key bindings (configurable via ~/.config/bunker/config.toml):\n")
	for _, e := range keybindingDefaults {
		key := e.def
		if kb != nil {
			key = e.field(kb).raw
		}
		fmt.Fprintf(&b, "  %-16s  %s\n", key, e.desc)
	}
	return b.String()
}

// resolveKeybindings builds a Keybindings from built-in defaults, then applies
// user overrides from the [keys] TOML map. Unknown or invalid entries are
// logged and ignored.
func resolveKeybindings(kf map[string]string) Keybindings {
	var kb Keybindings
	known := make(map[string]struct{}, len(keybindingDefaults))
	for _, e := range keybindingDefaults {
		known[e.action] = struct{}{}
		s := e.def
		if override, ok := kf[e.action]; ok && override != "" {
			s = override
		}
		parsed, err := parseKey(s)
		if err != nil {
			L.Warn("keybinding: parse error, using default", "action", e.action, "value", s, "err", err)
			parsed = mustParseKey(e.def)
		}
		*e.field(&kb) = parsed
	}
	for action := range kf {
		if _, ok := known[action]; !ok {
			L.Warn("keybinding: unknown action, ignoring", "action", action)
		}
	}
	return kb
}

// ---------------------------------------------------------------------------
// Theme definition (hex strings)
// ---------------------------------------------------------------------------

// ThemeDef holds a complete named theme as hex color strings.
type ThemeDef struct {
	Background     string
	Foreground     string
	Palette        [16]string // ANSI colors 0-15 (see below)
	ActiveBorder   string     // accent for the focused pane border
	InactiveBorder string
	ScrollThumb    string
	ScrollTrack    string
}

// The 16-color ANSI palette is a universal convention across all terminal
// themes.  Each theme picks its own specific hex values, but the *semantic
// meaning* of each slot is standardised:
//
//	Index  Name        Typical usage
//	─────  ──────────  ──────────────────────────────
//	  0    Black       dark background, empty space
//	  1    Red         errors, danger, destructive ops
//	  2    Green       success, OK, confirmations
//	  3    Yellow      warnings, attention, highlights
//	  4    Blue        informational, links, navigation
//	  5    Magenta     special, syntax accents
//	  6    Cyan        secondary info, labels
//	  7    White       foreground text
//	 8-15  Bright      brighter variants of 0-7
//
// Badge colors in status.go use palette indices (e.g. palette[1] for the
// sudo badge) so they automatically adapt when the user switches themes.

// resolvedTheme holds the same colors pre-parsed as tcell.Color values.
// Created once at startup; passed through render functions by value.
type resolvedTheme struct {
	bg, fg         tcell.Color
	palette        [16]tcell.Color
	activeBorder   tcell.Color
	inactiveBorder tcell.Color
	scrollThumb    tcell.Color
	scrollTrack    tcell.Color
}

// Config is the fully resolved application configuration.
type Config struct {
	Path       string // file the config was read from (may not exist yet)
	ThemeName  string // name the theme was resolved from
	Font       string
	Padding    int
	Tabs       tabsConfig
	Theme      resolvedTheme
	LogFile    string
	LogLevel   string
	CellAspect float64 // 0 = auto-detect via TIOCGWINSZ
	Scrollback int     // max scrollback lines per pane
	// ScrollbackBytes caps scrollback memory per pane (0 = no byte cap).
	// The smaller of the two limits applies; see Pane.sbCapacity.
	ScrollbackBytes int
	Keybindings     Keybindings
}

// ---------------------------------------------------------------------------
// Built-in theme registry
// ---------------------------------------------------------------------------

// BuiltinThemes is the registry of shipped themes.
// Keys are the names accepted by --theme and the config "theme" field.
var BuiltinThemes = map[string]ThemeDef{
	// Terminal: inherit every colour from the host terminal.
	// All fields are empty → hexColor returns tcell.ColorDefault for each.
	"terminal": {},

	// Default: the author's personal Tilix palette from yauhen.cc/posts/my-tillix
	"default": {
		Background: "#1C1C1F",
		Foreground: "#FFFFFF",
		Palette: [16]string{
			"#241F31", "#C01C28", "#2EC27E", "#F5C211",
			"#1E78E4", "#9841BB", "#0AB9DC", "#C0BFBC",
			"#5E5C64", "#ED333B", "#57E389", "#F8E45C",
			"#51A1FF", "#C061CB", "#4FD2FD", "#F6F5F4",
		},
		ActiveBorder:   "#C0BFBC", // light gray (same as scroll thumb)
		InactiveBorder: "#5E5C64",
		ScrollThumb:    "#C0BFBC",
		ScrollTrack:    "#3D3D40",
	},

	// Solarized Dark - Ethan Schoonover
	"solarized-dark": {
		Background: "#002B36",
		Foreground: "#839496",
		Palette: [16]string{
			"#073642", "#DC322F", "#859900", "#B58900",
			"#268BD2", "#D33682", "#2AA198", "#EEE8D5",
			"#002B36", "#CB4B16", "#586E75", "#657B83",
			"#839496", "#6C71C4", "#93A1A1", "#FDF6E3",
		},
		ActiveBorder:   "#93A1A1", // light gray-cyan
		InactiveBorder: "#073642",
		ScrollThumb:    "#657B83",
		ScrollTrack:    "#073642",
	},

	// Dracula - Zeno Rocha
	"dracula": {
		Background: "#282A36",
		Foreground: "#F8F8F2",
		Palette: [16]string{
			"#21222C", "#FF5555", "#50FA7B", "#F1FA8C",
			"#BD93F9", "#FF79C6", "#8BE9FD", "#F8F8F2",
			"#6272A4", "#FF6E6E", "#69FF94", "#FFFFA5",
			"#D6ACFF", "#FF92DF", "#A4FFFF", "#FFFFFF",
		},
		ActiveBorder:   "#6272A4", // comment gray-blue
		InactiveBorder: "#44475A",
		ScrollThumb:    "#6272A4",
		ScrollTrack:    "#21222C",
	},

	// Nord - Arctic Ice Studio
	"nord": {
		Background: "#2E3440",
		Foreground: "#D8DEE9",
		Palette: [16]string{
			"#3B4252", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#88C0D0", "#E5E9F0",
			"#4C566A", "#BF616A", "#A3BE8C", "#EBCB8B",
			"#81A1C1", "#B48EAD", "#8FBCBB", "#ECEFF4",
		},
		ActiveBorder:   "#D8DEE9", // snow storm light gray
		InactiveBorder: "#4C566A",
		ScrollThumb:    "#D8DEE9",
		ScrollTrack:    "#3B4252",
	},
}

// ---------------------------------------------------------------------------
// Loader
// ---------------------------------------------------------------------------

// DefaultConfigPath returns the XDG-compliant config file path.
func DefaultConfigPath() string {
	if xdg := os.Getenv("XDG_CONFIG_HOME"); xdg != "" {
		return filepath.Join(xdg, configDirName, "config.toml")
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", configDirName, "config.toml")
}

// LoadConfig reads the TOML config at path (empty = default), applies the
// optional themeOverride, and returns a resolved Config ready for use.
// A missing default config uses built-in defaults; explicit paths and invalid
// files return an error before the terminal is initialized.
func LoadConfig(path, themeOverride string) (Config, error) {
	explicit := path != ""
	if path == "" {
		path = DefaultConfigPath()
	}

	fc := fileConfig{
		Theme:    defaultThemeName,
		LogFile:  "/tmp/bunk.log",
		LogLevel: "info",
		Font:     defaultFont,
		Window:   windowConfig{Padding: defaultPadding},
		Tabs:     tabsConfig{Position: defaultTabsSide, Width: defaultTabsWidth},
	}
	if _, err := toml.DecodeFile(path, &fc); err != nil {
		if explicit || !os.IsNotExist(err) {
			return Config{}, fmt.Errorf("load config %q: %w", path, err)
		}
	}

	if themeOverride != "" {
		fc.Theme = themeOverride
	}

	def, ok := BuiltinThemes[fc.Theme]
	if !ok {
		L.Warn("config: unknown theme, using default", "theme", fc.Theme)
		fc.Theme = defaultThemeName
		def = BuiltinThemes[defaultThemeName]
	}
	if strings.TrimSpace(fc.Font) == "" {
		fc.Font = defaultFont
	}
	fc.Window.Padding = min(max(fc.Window.Padding, 0), maxPadding)
	if !slices.Contains(tabPositions, fc.Tabs.Position) {
		L.Warn("config: unknown tabs position, using left", "position", fc.Tabs.Position)
		fc.Tabs.Position = defaultTabsSide
	}
	fc.Tabs.Width = min(max(fc.Tabs.Width, minTabsWidth), maxTabsWidth)

	// Apply per-field [ui] overrides.
	if fc.UI.ActiveBorder != "" {
		def.ActiveBorder = fc.UI.ActiveBorder
	}
	if fc.UI.InactiveBorder != "" {
		def.InactiveBorder = fc.UI.InactiveBorder
	}
	if fc.UI.ScrollThumb != "" {
		def.ScrollThumb = fc.UI.ScrollThumb
	}
	if fc.UI.ScrollTrack != "" {
		def.ScrollTrack = fc.UI.ScrollTrack
	}

	scrollback := fc.Scrollback
	if scrollback <= 0 {
		scrollback = defaultScrollbackLines
	}

	return Config{
		Path:            path,
		ThemeName:       fc.Theme,
		Font:            fc.Font,
		Padding:         fc.Window.Padding,
		Tabs:            fc.Tabs,
		Theme:           resolveTheme(def),
		LogFile:         fc.LogFile,
		LogLevel:        fc.LogLevel,
		CellAspect:      fc.CellAspect,
		Scrollback:      scrollback,
		ScrollbackBytes: int(max(fc.ScrollbackMB, 0) * (1 << 20)),
		Keybindings:     resolveKeybindings(fc.Keys),
	}, nil
}

// resolveTheme parses all hex strings in a ThemeDef into tcell.Colors.
func resolveTheme(d ThemeDef) resolvedTheme {
	rt := resolvedTheme{
		bg:             hexColor(d.Background),
		fg:             hexColor(d.Foreground),
		activeBorder:   hexColor(d.ActiveBorder),
		inactiveBorder: hexColor(d.InactiveBorder),
		scrollThumb:    hexColor(d.ScrollThumb),
		scrollTrack:    hexColor(d.ScrollTrack),
	}
	for i, s := range d.Palette {
		rt.palette[i] = hexColor(s)
	}
	return rt
}

// hexColor parses a "#RRGGBB" hex string into a tcell.Color.
// Returns tcell.ColorDefault on any parse error.
func hexColor(s string) tcell.Color {
	s = strings.TrimPrefix(s, "#")
	if len(s) != 6 {
		return tcell.ColorDefault
	}
	r, e1 := strconv.ParseInt(s[0:2], 16, 32)
	g, e2 := strconv.ParseInt(s[2:4], 16, 32)
	b, e3 := strconv.ParseInt(s[4:6], 16, 32)
	if e1 != nil || e2 != nil || e3 != nil {
		return tcell.ColorDefault
	}
	return tcell.NewRGBColor(int32(r), int32(g), int32(b))
}

// ---------------------------------------------------------------------------
// Default config template
// ---------------------------------------------------------------------------

// DefaultConfigTOML returns a well-commented default config.toml as a string.
func DefaultConfigTOML() string {
	return fmt.Sprintf(`# bunker configuration
# %s
#
# Settings (Ctrl+,) edits this file in place and keeps your comments.
# Changes saved here from an editor apply to open windows immediately.

# Built-in themes: default, solarized-dark, dracula, nord
# ("terminal" inherits host colours and only applies to bunker tui).
theme = "default"

# Font: a Pango font description, family then size.
font = "Monospace 11"

# Logging.  Set log_file = "" to disable logging entirely.
log_file  = "/tmp/bunk.log"
log_level = "info"  # trace | debug | info | warn | error

# Cell pixel aspect ratio (cell height / cell width).
# Used to decide split direction (vertical vs horizontal) based on real pixels.
# Default 2.25 suits Noto Sans Mono 11pt on Ptyxis; adjust for your font.
# Examples: JetBrains Mono 12pt ≈ 2.2  |  Fira Code 11pt ≈ 2.15
#
# cell_aspect = 2.25   # uncomment and adjust if splits feel wrong

# Maximum scrollback lines per pane.  Higher values use more memory.
# Note: scrollback stores screen rows, not logical lines — narrower panes
# consume more rows per line of output due to wrapping.
# scrollback = 10000

# Optional memory cap per pane, in MiB.  When set, the smaller of the two
# limits wins: a pane keeps at most scrollback_mb MiB of history, so wider
# panes hold fewer rows.  Each cell costs 32 bytes (200 cols x 10000 rows
# ~= 61 MiB).  Leave unset to limit by lines only.
# scrollback_mb = 32

[window]
padding = 8          # pixels between the window edge and the text grid

[tabs]
position = "left"    # left | right | top | bottom
width    = 220       # sidebar width in pixels when left or right

# Optional UI color overrides - leave blank to use the theme's defaults.
# Values must be "#RRGGBB" hex strings.
[ui]
active_border   = ""  # border colour for the focused pane
inactive_border = ""  # border colour for unfocused panes
scrollbar_thumb = ""  # scrollbar handle
scrollbar_track = ""  # scrollbar background

# ---------------------------------------------------------------------------
# Key bindings
# ---------------------------------------------------------------------------
# Format: "f1", "ctrl+c", "alt+up", "shift+pgup", "escape", etc.
# Modifiers: ctrl, alt, shift.  Key names are case-insensitive.
# Leave a value empty (or remove the line) to keep the built-in default.
[keys]
split         = "f1"          # auto-split the active pane (host shell)
split_context = "alt+f1"     # auto-split inheriting container/ssh/sudo context
quit         = "ctrl+q"      # quit bunk
copy         = "ctrl+c"      # copy selection (if active); otherwise forwards to shell
paste        = "ctrl+v"      # paste from clipboard
search       = "ctrl+f"      # enter incremental search
passthrough  = "ctrl+f12"    # forward all keys except this toggle to the active pane
zoom         = "f12"         # toggle fullscreen zoom on the active pane
nav_up       = "alt+up"      # move focus to the pane above
nav_down     = "alt+down"    # move focus to the pane below
nav_left     = "alt+left"    # move focus to the pane on the left
nav_right    = "alt+right"   # move focus to the pane on the right
scroll_up    = "shift+pgup"  # scroll up in scrollback buffer
scroll_down  = "shift+pgdn"  # scroll down / return toward live output
reinit_host  = "alt+r"       # re-initialise host terminal after binary corruption

# Search mode keys (active only while Ctrl+F search bar is open).
search_next  = "ctrl+n"   # jump to next match  (Enter always works too)
search_prev  = "ctrl+p"   # jump to previous match
search_exit  = "escape"   # close search bar
`, DefaultConfigPath())
}
