// gui_style.go - bunker's CSS: the static styles and the theme-derived
// window chrome.
//
// Both providers are display-wide and installed once; guiApplyTheme reloads
// the chrome when the terminal theme changes, and every bunker window
// follows it.
package main

import (
	"fmt"
	"strings"
	"sync"

	"github.com/diamondburned/gotk4/pkg/gdk/v4"
	"github.com/diamondburned/gotk4/pkg/gtk/v4"
)

var (
	guiStyleOnce  sync.Once
	guiThemeStyle *gtk.CSSProvider
)

// guiApplyTheme installs the styles on first use and colours the window
// chrome (and GTK's dark preference) from rt.
func guiApplyTheme(rt resolvedTheme) {
	guiStyleOnce.Do(func() {
		display := gdk.DisplayGetDefault()
		// The window icon: embedded, unpacked to the cache (see desktop.go).
		if dir := iconCacheDir(); writeIcons(dir) == nil {
			gtk.IconThemeGetForDisplay(display).AddSearchPath(dir)
		}
		gtk.WindowSetDefaultIconName(guiAppID)
		static := gtk.NewCSSProvider()
		static.LoadFromString(guiStaticCSS)
		gtk.StyleContextAddProviderForDisplay(display, static, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
		guiThemeStyle = gtk.NewCSSProvider()
		gtk.StyleContextAddProviderForDisplay(display, guiThemeStyle, gtk.STYLE_PROVIDER_PRIORITY_APPLICATION)
	})
	guiThemeStyle.LoadFromString(themeCSS(rt))
	if s := gtk.SettingsGetDefault(); s != nil {
		s.SetObjectProperty("gtk-application-prefer-dark-theme", isDarkColor(rt.bg))
	}
}

// isDarkColor uses perceived luminance (ITU-R BT.601 weights).
func isDarkColor(c interface{ RGB() (int32, int32, int32) }) bool {
	r, g, b := c.RGB()
	return r*299+g*587+b*114 < 128_000
}

// guiStaticCSS styles the banner and the Preferences window.
const guiStaticCSS = `
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
`

// themeCSS derives the window chrome (header bar, tab strip) from the
// terminal theme, so the window reads as one surface instead of terminal
// colours inside generic GTK grey. Scoped to .bunker-window so dialogs keep
// the system style.
func themeCSS(rt resolvedTheme) string {
	rgb := func(c interface{ RGB() (int32, int32, int32) }) [3]int32 {
		r, g, b := c.RGB()
		return [3]int32{r, g, b}
	}
	bg, fg := rgb(rt.bg), rgb(rt.fg)
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
	).Replace(`
window.bunker-window { background-color: @bg; }
window.bunker-window headerbar {
	background: @bg;
	color: @text;
	box-shadow: none;
	border-bottom: 1px solid @line;
}
window.bunker-window headerbar:backdrop { background: @bg; color: @dim; }
window.bunker-window headerbar button { color: inherit; background: transparent; box-shadow: none; border-color: transparent; }
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
.bunker-tab.collapsed { padding: 5px 0; }
.bunker-tab-short { font-weight: bold; }
.bunker-tab-info { color: @muted; font-size: 0.85em; }
window.bunker-window headerbar .bunker-subtitle { color: @muted; font-size: 0.82em; }
.bunker-tab-list.collapsed { padding: 6px 4px; }
.bunker-tab entry.bunker-tab-entry { min-height: 24px; padding: 0 6px; background-color: @bg; color: @fg; }
`)
}
