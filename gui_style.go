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

// guiStaticCSS styles the banner, the Preferences window, and the tab
// menu's Colour entries.
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
button.bunker-keycap {
	min-width: 96px;
	padding: 3px 12px;
	border-radius: 7px;
	font-feature-settings: "tnum";
}
button.bunker-keycap.bunker-recording {
	outline: 2px solid @theme_selected_bg_color;
	outline-offset: -2px;
}
button.bunker-keycap.bunker-keycap-off label {
	opacity: 0.55;
	font-style: italic;
}
.bunker-clash {
	padding: 10px 14px;
	border-radius: 10px;
	background-color: alpha(@theme_selected_bg_color, 0.14);
	border: 1px solid alpha(@theme_selected_bg_color, 0.45);
}
.bunker-settings-error {
	background-color: #c01c28;
	color: white;
	padding: 8px 14px;
}
.bunker-settings-sidebar {
	padding: 8px 0;
}
popover.menu button.bunker-colour-item {
	min-height: 30px;
	padding: 0 12px;
	border-radius: 6px;
	font-weight: normal;
}
.bunker-swatch {
	min-width: 12px;
	min-height: 12px;
	border-radius: 6px;
}
.bunker-swatch.swatch-none {
	box-shadow: inset 0 0 0 1px alpha(currentColor, 0.5);
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
	css := strings.NewReplacer(
		"@bg", mix(bg, fg, 0),
		"@sidebar", mix(bg, fg, 0.04),
		"@hover", mix(bg, fg, 0.08),
		"@line", mix(bg, fg, 0.10),
		"@selected", mix(bg, fg, 0.15),
		"@fg", mix(fg, bg, 0),
		"@text", mix(fg, bg, 0.08),
		"@muted", mix(fg, bg, 0.30),
		"@dim", mix(fg, bg, 0.45),
		// Alert colours come from the theme's own green, red, and yellow.
		"@ok", mix(rgb(rt.palette[2]), bg, 0),
		"@bad", mix(rgb(rt.palette[1]), bg, 0),
		"@warn", mix(rgb(rt.palette[3]), bg, 0),
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
.bunker-tab { padding: 3px 2px 3px 8px; border: 2px solid transparent; border-radius: 7px; min-height: 26px; color: @muted; }
.bunker-tabs.left .bunker-tab:not(:first-child), .bunker-tabs.right .bunker-tab:not(:first-child) { margin-top: 2px; }
.bunker-tabs.top .bunker-tab:not(:first-child), .bunker-tabs.bottom .bunker-tab:not(:first-child) { margin-left: 2px; }
.bunker-tab:hover { background-color: @hover; }
.bunker-tab.active { background-color: @selected; color: @fg; }
.bunker-tab .bunker-tab-close { min-width: 22px; min-height: 22px; padding: 0; opacity: 0; }
.bunker-tab:hover .bunker-tab-close, .bunker-tab.active .bunker-tab-close { opacity: 0.75; }
.bunker-tab.collapsed { padding: 5px 0; border-width: 0; }
.bunker-tab-short { font-weight: bold; border-radius: 6px; margin: 0 6px; padding: 1px 0; }
.bunker-tab.attn { color: @fg; }
.bunker-tab-dot { min-width: 8px; min-height: 8px; border-radius: 4px; margin: 0 3px; }
.bunker-tab.attn-done .bunker-tab-dot { background-color: @ok; }
.bunker-tab.attn-alert .bunker-tab-dot { background-color: @warn; }
.bunker-tab.attn-failed .bunker-tab-dot { background-color: @bad; }
.bunker-tab.collapsed.attn-done .bunker-tab-short { background-color: @ok; color: @bg; }
.bunker-tab.collapsed.attn-alert .bunker-tab-short { background-color: @warn; color: @bg; }
.bunker-tab.collapsed.attn-failed .bunker-tab-short { background-color: @bad; color: @bg; }
.bunker-tab-info { color: @muted; font-size: 0.85em; }
window.bunker-window headerbar .bunker-subtitle { color: @muted; font-size: 0.82em; }
.bunker-tab-list.collapsed { padding: 6px 4px; }
.bunker-tab entry.bunker-tab-entry { min-height: 24px; padding: 0 6px; background-color: @bg; color: @fg; }
.bunker-tabs .bunker-tab.group-mid:not(.collapsed), .bunker-tabs .bunker-tab.group-last:not(.collapsed) { margin: 0; }
.bunker-tabs.left .bunker-tab.group-first:not(.collapsed), .bunker-tabs.right .bunker-tab.group-first:not(.collapsed) { border-bottom-width: 0; padding-bottom: 5px; border-bottom-left-radius: 0; border-bottom-right-radius: 0; }
.bunker-tabs.left .bunker-tab.group-mid:not(.collapsed), .bunker-tabs.right .bunker-tab.group-mid:not(.collapsed) { border-top-width: 0; border-bottom-width: 0; padding-top: 5px; padding-bottom: 5px; border-radius: 0; }
.bunker-tabs.left .bunker-tab.group-last:not(.collapsed), .bunker-tabs.right .bunker-tab.group-last:not(.collapsed) { border-top-width: 0; padding-top: 5px; border-top-left-radius: 0; border-top-right-radius: 0; }
.bunker-tabs.top .bunker-tab.group-first, .bunker-tabs.bottom .bunker-tab.group-first { border-right-width: 0; padding-right: 4px; border-top-right-radius: 0; border-bottom-right-radius: 0; }
.bunker-tabs.top .bunker-tab.group-mid, .bunker-tabs.bottom .bunker-tab.group-mid { border-left-width: 0; border-right-width: 0; padding-left: 10px; padding-right: 4px; border-radius: 0; }
.bunker-tabs.top .bunker-tab.group-last, .bunker-tabs.bottom .bunker-tab.group-last { border-left-width: 0; padding-left: 10px; border-top-left-radius: 0; border-bottom-left-radius: 0; }
`)
	// Tab tags: a border around the row, shared by neighbours of the same
	// colour (group-first/mid/last drop the inner edges and keep the content
	// in place), and a ring around the collapsed number. The ring stands off
	// the number, so an alert's fill in the same colour still shows it.
	var tags strings.Builder
	for _, c := range tabColours {
		fmt.Fprintf(&tags, `
.bunker-tab.tag-%[1]s:not(.collapsed) { border-color: %[2]s; }
.bunker-tab.collapsed.tag-%[1]s .bunker-tab-short { outline: 2px solid %[2]s; outline-offset: 1px; }
.bunker-swatch.swatch-%[1]s { background-color: %[2]s; }
`, c.name, mix(rgb(rt.palette[c.index]), bg, 0))
	}
	return css + tags.String()
}
