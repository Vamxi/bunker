//! Window chrome colours derived from the terminal theme, so the window
//! reads as one surface.

use bunker_vt::{Rgb, Theme};
use gtk::gdk;

fn hex(c: Rgb) -> String {
    format!("#{:02x}{:02x}{:02x}", c.r, c.g, c.b)
}

/// The window's CSS for `theme`.
pub fn css(theme: &Theme) -> String {
    let (bg, fg) = (theme.bg, theme.fg);
    let mix = |a: Rgb, b: Rgb, t: f32| hex(a.mix(b, t));
    let vars = [
        ("@bg", hex(bg)),
        ("@sidebar", mix(bg, fg, 0.04)),
        ("@hover", mix(bg, fg, 0.08)),
        ("@line", mix(bg, fg, 0.10)),
        ("@selected", mix(bg, fg, 0.15)),
        ("@fg", hex(fg)),
        ("@text", mix(fg, bg, 0.08)),
        ("@muted", mix(fg, bg, 0.30)),
        ("@dim", mix(fg, bg, 0.45)),
        ("@ok", hex(theme.ansi[2])),
        ("@bad", hex(theme.ansi[1])),
        ("@warn", hex(theme.ansi[3])),
        ("@accent", hex(theme.ansi[4])),
    ];
    let mut css = TEMPLATE.to_string();
    for (k, v) in vars {
        css = css.replace(k, &v);
    }
    for c in TAG_COLOURS {
        css.push_str(&format!(
            ".bunker-swatch.swatch-{} {{ background-color: {}; }}\n",
            c.name,
            hex(theme.ansi[c.index])
        ));
    }
    css
}

/// A tab tag colour: its CSS name, menu label, and palette entry.
pub struct TagColour {
    /// Name used in CSS classes.
    pub name: &'static str,
    /// Menu label.
    pub label: &'static str,
    index: usize,
}

/// The colour of tag `name` in `theme`.
pub fn tag_rgb(theme: &Theme, name: &str) -> Option<Rgb> {
    TAG_COLOURS.iter().find(|c| c.name == name).map(|c| theme.ansi[c.index])
}

/// The tag colours, in menu order.
pub const TAG_COLOURS: [TagColour; 6] = [
    TagColour { name: "red", label: "Red", index: 1 },
    TagColour { name: "yellow", label: "Yellow", index: 3 },
    TagColour { name: "green", label: "Green", index: 2 },
    TagColour { name: "cyan", label: "Cyan", index: 6 },
    TagColour { name: "blue", label: "Blue", index: 4 },
    TagColour { name: "purple", label: "Purple", index: 5 },
];

const TEMPLATE: &str = r#"
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
window.bunker-window headerbar windowcontrols button > image { background-color: @hover; color: inherit; }
window.bunker-window headerbar windowcontrols button:hover > image { background-color: @selected; }
.bunker-tabs { background-color: @sidebar; color: @text; }
.bunker-tabs.left { border-right: 1px solid @line; }
.bunker-tabs.right { border-left: 1px solid @line; }
.bunker-tabs.top { border-bottom: 1px solid @line; }
.bunker-tabs.bottom { border-top: 1px solid @line; }
.bunker-tab-list { padding: 4px 6px 8px 6px; }
.bunker-tabs.top .bunker-tab-list, .bunker-tabs.bottom .bunker-tab-list { padding: 4px 6px; }
.bunker-tab { padding: 0 4px 0 2px; min-height: 46px; border-radius: 8px; color: @muted; margin-top: 4px; }
.bunker-tabs.top .bunker-tab, .bunker-tabs.bottom .bunker-tab { min-height: 32px; margin: 0 2px; }
.bunker-tab.group-mid, .bunker-tab.group-last { margin-top: 0; }
.bunker-tab:hover { background-color: @hover; }
.bunker-tab.active { background-color: @selected; color: @fg; }
.bunker-tab.dragging { background-color: @selected; box-shadow: 0 6px 16px alpha(black, 0.35); }
.bunker-tab-circle { margin-right: 6px; }
.bunker-tab-text { margin: 8px 0; }
.bunker-tabs.top .bunker-tab-text, .bunker-tabs.bottom .bunker-tab-text { margin: 4px 0; }
.bunker-tab-title { font-weight: 600; }
.bunker-tab-detail { font-size: 0.82em; color: @dim; margin-top: 1px; }
.bunker-tab-branch { font-size: 0.82em; color: @accent; margin-top: 1px; }
.bunker-tab-close { min-width: 22px; min-height: 22px; padding: 0; margin-left: 2px; opacity: 0; }
.bunker-tab:hover .bunker-tab-close, .bunker-tab.active .bunker-tab-close { opacity: 0.6; }
.bunker-tab-close:hover { opacity: 1; }
.bunker-tab entry.bunker-tab-rename { min-height: 24px; padding: 0 6px; margin: 2px 0; background-color: @bg; color: @fg; }
.bunker-tab-progress { min-height: 2px; margin-top: 4px; }
.bunker-tab-progress trough { min-height: 2px; background-color: alpha(@fg, 0.08); border-radius: 1px; }
.bunker-tab-progress progress { min-height: 2px; background-color: @accent; border-radius: 1px; }
.bunker-tab-progress.error progress { background-color: @bad; }
.bunker-search { background-color: @sidebar; color: @text; border: 1px solid @line; border-radius: 12px; padding: 4px 6px; margin: 10px 18px; box-shadow: 0 3px 10px alpha(black, 0.3); }
.bunker-search entry { min-height: 30px; }
.bunker-search button { min-width: 30px; min-height: 30px; padding: 0; color: inherit; }
.bunker-pass { background-color: @warn; color: @bg; font-weight: bold; font-size: 0.8em; letter-spacing: 1px; padding: 2px 8px; border-radius: 6px; margin: 10px 18px; }
.bunker-tabs-handle { min-width: 5px; background-color: transparent; }
.bunker-tabs-handle:hover { background-color: @line; }
popover.bunker-tab-menu > contents { padding: 6px; }
popover.bunker-tab-menu button { min-height: 30px; padding: 0 12px; border-radius: 6px; font-weight: normal; }
.bunker-swatch { min-width: 12px; min-height: 12px; border-radius: 6px; }
.bunker-swatch.swatch-none { box-shadow: inset 0 0 0 1px alpha(currentColor, 0.5); }
.bunker-menu-heading { font-size: 0.85em; opacity: 0.6; margin: 4px 12px 2px 12px; }
.bunker-page-title { font-size: 1.6em; font-weight: 800; }
.bunker-prefs-page { margin: 34px 32px 40px 32px; }
.bunker-prefs-side { background-color: alpha(currentColor, 0.03); }
.bunker-prefs-sidebar { padding: 10px 8px; background: none; }
.bunker-prefs-sidebar > row { padding: 9px 12px; border-radius: 8px; margin-bottom: 2px; }
.bunker-group-title { font-weight: bold; font-size: 1.02em; margin: 0 2px 2px 2px; }
.bunker-dim { opacity: 0.62; font-size: 0.92em; }
list.bunker-card { background-color: alpha(currentColor, 0.045); border: 1px solid alpha(currentColor, 0.09); border-radius: 12px; margin-top: 6px; }
list.bunker-card > row { border-bottom: 1px solid alpha(currentColor, 0.07); background: none; }
list.bunker-card > row:last-child { border-bottom: none; }
.bunker-row { padding: 12px 16px; min-height: 40px; }
.bunker-control { min-width: 96px; }
dropdown.bunker-control, button.bunker-control { min-width: 200px; }
.bunker-theme-card { padding: 6px; border-radius: 14px; }
.bunker-theme-card label { font-size: 0.9em; }
.bunker-theme-preview { border-radius: 10px; box-shadow: 0 1px 3px alpha(black, 0.25); }
.bunker-theme-card.picked .bunker-theme-preview { box-shadow: 0 0 0 3px @theme_selected_bg_color; }
.bunker-theme-card.picked label { font-weight: bold; }
button.bunker-keycap { min-width: 140px; padding: 4px 12px; border-radius: 8px; font-feature-settings: "tnum"; }
button.bunker-keycap.bunker-recording { outline: 2px solid @theme_selected_bg_color; outline-offset: -2px; }
button.bunker-keycap.bunker-keycap-off label { opacity: 0.55; font-style: italic; }
.bunker-clash { padding: 10px 14px; border-radius: 10px; background-color: alpha(@theme_selected_bg_color, 0.14); border: 1px solid alpha(@theme_selected_bg_color, 0.45); }
.bunker-settings-error { background-color: #c01c28; color: white; padding: 8px 14px; }
"#;

/// Installs the CSS for `theme` on the default display, replacing the
/// previous one.
pub fn apply(theme: &Theme) {
    use std::cell::RefCell;
    thread_local! {
        static PROVIDER: RefCell<Option<(gtk::CssProvider, String)>> = const { RefCell::new(None) };
    }
    let Some(display) = gdk::Display::default() else { return };
    let text = css(theme);
    PROVIDER.with(|p| {
        let mut p = p.borrow_mut();
        // Reloading CSS restyles every widget; skip it when nothing changed.
        if p.as_ref().is_some_and(|(_, last)| *last == text) {
            return;
        }
        let provider = p.get_or_insert_with(|| {
            let provider = gtk::CssProvider::new();
            gtk::style_context_add_provider_for_display(&display, &provider, gtk::STYLE_PROVIDER_PRIORITY_APPLICATION);
            (provider, String::new())
        });
        provider.0.load_from_string(&text);
        provider.1 = text;
    });
    // Switching GTK between light and dark restyles everything too.
    let scheme = if theme.bg.is_dark() { gtk::InterfaceColorScheme::Dark } else { gtk::InterfaceColorScheme::Light };
    if let Some(settings) = gtk::Settings::default()
        && settings.gtk_interface_color_scheme() != scheme
    {
        settings.set_gtk_interface_color_scheme(scheme);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn css_has_no_unreplaced_variables() {
        // GTK's own named colours are the only @ names left.
        let css = css(&Theme::default()).replace("@theme_selected_bg_color", "");
        assert!(!css.contains('@'), "{css}");
    }
}
