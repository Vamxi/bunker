//! Bundled Ptyxis palettes (themes/ptyxis) and the `system` theme, which
//! follows GNOME's light or dark style.

use std::collections::BTreeMap;

use bunker_vt::{Rgb, Theme};

const PALETTES: &[&str] = &[
    include_str!("../../../themes/ptyxis/Dark Pastel.palette"),
    include_str!("../../../themes/ptyxis/dracula.palette"),
    include_str!("../../../themes/ptyxis/gnome-high-contrast.palette"),
    include_str!("../../../themes/ptyxis/gnome.palette"),
    include_str!("../../../themes/ptyxis/Horizon.palette"),
    include_str!("../../../themes/ptyxis/nord.palette"),
    include_str!("../../../themes/ptyxis/solarized.palette"),
    include_str!("../../../themes/ptyxis/tango.palette"),
    include_str!("../../../themes/ptyxis/Vs Code.palette"),
];

/// A theme name for config files: lowercase words joined by dashes.
pub fn slug(name: &str) -> String {
    let mut out = String::new();
    for c in name.trim().to_lowercase().replace('+', " plus ").chars() {
        if c.is_ascii_alphanumeric() {
            out.push(c);
        } else if !out.ends_with('-') {
            out.push('-');
        }
    }
    out.trim_matches('-').to_string()
}

fn parse_variant(keys: &BTreeMap<String, String>) -> Option<Theme> {
    let color = |k: &str| keys.get(k).and_then(|v| Rgb::parse(v));
    let mut ansi = [Rgb::default(); 16];
    for (i, slot) in ansi.iter_mut().enumerate() {
        *slot = color(&format!("Color{i}"))?;
    }
    Some(Theme { fg: color("Foreground")?, bg: color("Background")?, cursor: color("Cursor"), ansi })
}

/// Parses a .palette file into its themes: one per variant, the dark one
/// named after the palette and the light one with `-light` appended.
fn parse_palette(text: &str) -> Vec<(String, Theme)> {
    let mut sections: BTreeMap<String, BTreeMap<String, String>> = BTreeMap::new();
    let mut section = String::new();
    for line in text.lines().map(str::trim) {
        if line.is_empty() || line.starts_with('#') || line.starts_with(';') {
            continue;
        }
        if let Some(name) = line.strip_prefix('[').and_then(|l| l.strip_suffix(']')) {
            section = name.trim().to_string();
            continue;
        }
        if let Some((k, v)) = line.split_once('=') {
            sections.entry(section.clone()).or_default().insert(k.trim().into(), v.trim().into());
        }
    }
    let Some(name) = sections.get("Palette").and_then(|p| p.get("Name")).map(|n| slug(n)) else {
        return Vec::new();
    };
    let mut out = Vec::new();
    match (sections.get("Dark"), sections.get("Light")) {
        (None, None) => {
            if let Some(t) = sections.get("Palette").and_then(parse_variant) {
                out.push((name, t));
            }
        }
        (dark, light) => {
            if let Some(t) = dark.and_then(parse_variant) {
                out.push((name.clone(), t));
            }
            if let Some(t) = light.and_then(parse_variant) {
                out.push((format!("{name}-light"), t));
            }
        }
    }
    out
}

/// Every bundled theme by name.
pub fn bundled() -> BTreeMap<String, Theme> {
    PALETTES.iter().flat_map(|p| parse_palette(p)).collect()
}

/// The theme for a config name; `system` picks GNOME's palette for the
/// desktop's style. Unknown names fall back to `system`.
pub fn resolve(name: &str, prefer_dark: bool) -> Theme {
    let themes = bundled();
    if name != "system"
        && let Some(t) = themes.get(name)
    {
        return t.clone();
    }
    let key = if prefer_dark { "gnome" } else { "gnome-light" };
    themes.get(key).cloned().unwrap_or_default()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn bundled_themes_parse() {
        let t = bundled();
        for name in ["dark-pastel", "gnome", "gnome-light", "tango", "tango-light", "vs-code"] {
            assert!(t.contains_key(name), "missing {name}: {:?}", t.keys().collect::<Vec<_>>());
        }
        assert_eq!(t["tango"].bg, Rgb::new(0x2e, 0x34, 0x36));
    }

    #[test]
    fn slugs() {
        assert_eq!(slug("Dark Pastel"), "dark-pastel");
        assert_eq!(slug("Vs Code"), "vs-code");
    }

    #[test]
    fn unknown_falls_back_to_system() {
        assert_eq!(resolve("nope", true), resolve("system", true));
    }
}
