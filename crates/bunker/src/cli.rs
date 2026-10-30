//! Commands besides opening a window: `bunker config`, `bunker themes`,
//! and `bunker install-desktop`.

use std::path::{Path, PathBuf};

use crate::config::{self, Config};

/// The app id; the launcher and icons are named after it.
pub const APP_ID: &str = "dev.bunker.Bunker";

/// The icon in every size the hicolor theme uses.
const ICONS: [(u32, &[u8]); 7] = [
    (16, include_bytes!("../../../assets/icon/bunker-16.png")),
    (32, include_bytes!("../../../assets/icon/bunker-32.png")),
    (48, include_bytes!("../../../assets/icon/bunker-48.png")),
    (64, include_bytes!("../../../assets/icon/bunker-64.png")),
    (128, include_bytes!("../../../assets/icon/bunker-128.png")),
    (256, include_bytes!("../../../assets/icon/bunker-256.png")),
    (512, include_bytes!("../../../assets/icon/bunker-512.png")),
];

/// The documented config `bunker config init` writes.
const DEFAULT_CONFIG: &str = r#"# bunker configuration
#
# Preferences (Ctrl+,) edits this file in place and keeps your comments.
# Changes saved here from an editor apply to open windows immediately.
# "bunker config list" shows every setting; "bunker config set" changes one.

# "system" follows your desktop: GNOME's palette, light or dark with the
# desktop. "bunker themes" lists the others.
theme = "system"

# Font: a Pango font description, family then size.
font = "Monospace 11"

# Rows of history kept per tab.
scrollback = 10000

[window]
padding = 8          # pixels between the window edge and the text

[tabs]
position = "left"    # left | right | top | bottom
width    = 220       # sidebar width in pixels when left or right
autohide = false     # hide the tab bar while only one tab is open

# "system" blinks when the program asks for a blinking cursor, and
# otherwise follows GNOME's setting (Accessibility > Typing). "on" or "off"
# overrides both. It blinks only while the window has focus, and stops
# after a few seconds without typing.
[cursor]
blink = "system"

# Alerts. A tab you are not looking at lights up when a command that ran
# command_seconds or longer finishes (0 turns this off), when a program
# rings the bell, or when it asks for a notification (OSC 9, 777, 99;
# Claude Code can). desktop: also show a desktop notification while the
# window is in the background ("unfocused"), "always", or "never".
[notify]
desktop         = "unfocused"
command_seconds = 10
bell            = true

# Shortcuts: "mod+mod+key" with ctrl, alt, shift; keys are letters, digits,
# f1-f24, up/down/left/right, pgup/pgdn, home/end, enter, escape, tab,
# backspace, delete, insert, space, and names such as comma, minus, plus.
# "none" turns a shortcut off. Preferences > Keyboard edits these too.
[keys]
new_tab             = "ctrl+shift+t"
close_tab           = "ctrl+shift+w"
next_tab            = "ctrl+pgdn"
prev_tab            = "ctrl+pgup"
copy                = "ctrl+c"       # copies a selection; without one, Ctrl+C goes to the program
paste               = "ctrl+v"
copy_clipboard      = "ctrl+shift+c"
paste_clipboard     = "ctrl+shift+v"
paste_clipboard_alt = "shift+insert"
search              = "ctrl+f"
scroll_up           = "shift+pgup"
scroll_down         = "shift+pgdn"
font_bigger         = "ctrl+shift+plus"
font_smaller        = "ctrl+shift+minus"
font_reset          = "ctrl+shift+0"
passthrough         = "ctrl+f12"     # every key goes to the program (PASS)
preferences         = "ctrl+comma"
close_window        = "ctrl+shift+q"
"#;

/// What a setting holds, for checking `bunker config set`.
enum Kind {
    Text,
    OneOf(&'static [&'static str]),
    Int(i64, i64),
    Bool,
    Key,
}

/// A setting `bunker config` reads and writes.
struct Setting {
    name: String,
    table: Option<&'static str>,
    key: &'static str,
    kind: Kind,
}

const POSITIONS: &[&str] = &["left", "right", "top", "bottom"];
const DESKTOP: &[&str] = &["unfocused", "always", "never"];
const BLINK: &[&str] = &["system", "on", "off"];

fn settings() -> Vec<Setting> {
    let s = |name: &str, table, key, kind| Setting { name: name.into(), table, key, kind };
    let mut out = vec![
        s("theme", None, "theme", Kind::Text),
        s("font", None, "font", Kind::Text),
        s("scrollback", None, "scrollback", Kind::Int(100, 10_000_000)),
        s("window.padding", Some("window"), "padding", Kind::Int(0, 200)),
        s("tabs.position", Some("tabs"), "position", Kind::OneOf(POSITIONS)),
        s("tabs.width", Some("tabs"), "width", Kind::Int(170, 560)),
        s("tabs.autohide", Some("tabs"), "autohide", Kind::Bool),
        s("cursor.blink", Some("cursor"), "blink", Kind::OneOf(BLINK)),
        s("notify.desktop", Some("notify"), "desktop", Kind::OneOf(DESKTOP)),
        s("notify.command_seconds", Some("notify"), "command_seconds", Kind::Int(0, 86_400)),
        s("notify.bell", Some("notify"), "bell", Kind::Bool),
    ];
    for (action, _, _) in crate::ui::ACTIONS {
        out.push(Setting { name: format!("keys.{action}"), table: Some("keys"), key: action, kind: Kind::Key });
    }
    out
}

/// The current value of setting `s`, as it reads in TOML.
fn current(s: &Setting, c: &Config) -> String {
    let quote = |v: &str| format!("{v:?}");
    match s.name.as_str() {
        "theme" => quote(&c.theme),
        "font" => quote(&c.font),
        "scrollback" => c.scrollback.to_string(),
        "window.padding" => c.padding.to_string(),
        "tabs.position" => quote(POSITIONS[c.tab_position as usize]),
        "tabs.width" => c.tab_width.to_string(),
        "tabs.autohide" => c.tab_autohide.to_string(),
        "cursor.blink" => quote(&c.cursor_blink),
        "notify.desktop" => quote(&c.notify_desktop),
        "notify.command_seconds" => c.command_seconds.to_string(),
        "notify.bell" => c.notify_bell.to_string(),
        _ => {
            let shortcuts = crate::ui::shortcuts(&c.keys);
            let combo = shortcuts.iter().find(|x| x.action == s.key).and_then(|x| x.config_key());
            quote(&combo.unwrap_or_else(|| "none".into()))
        }
    }
}

/// Checks `value` for setting `s` and returns it as a TOML value.
fn parse_value(s: &Setting, value: &str) -> Result<toml_edit::Value, String> {
    let value = value.trim().trim_matches(['"', '\'']);
    match &s.kind {
        Kind::Text if s.name == "theme" => {
            let themes = crate::theme::bundled();
            if value != "system" && !themes.contains_key(value) {
                return Err(format!("unknown theme {value:?}; \"bunker themes\" lists them"));
            }
            Ok(value.into())
        }
        Kind::Text => Ok(value.into()),
        Kind::OneOf(allowed) => {
            if allowed.contains(&value) {
                Ok(value.into())
            } else {
                Err(format!("{value:?} is not one of {}", allowed.join(", ")))
            }
        }
        Kind::Int(lo, hi) => match value.parse::<i64>() {
            Ok(v) if (*lo..=*hi).contains(&v) => Ok(v.into()),
            _ => Err(format!("{value:?} is not a whole number from {lo} to {hi}")),
        },
        Kind::Bool => match value {
            "true" => Ok(true.into()),
            "false" => Ok(false.into()),
            _ => Err(format!("{value:?} is not true or false")),
        },
        Kind::Key => crate::ui::canonical_key(value).map(Into::into),
    }
}

fn find(name: &str) -> Result<Setting, String> {
    settings()
        .into_iter()
        .find(|s| s.name == name)
        .ok_or_else(|| format!("unknown setting {name:?} (\"bunker config list\" shows them all)"))
}

/// The hicolor icons under `root` (…/hicolor/NxN/apps/<id>.png); files
/// already up to date are left alone.
pub fn write_icons(root: &Path) -> Result<(), String> {
    for (n, data) in ICONS {
        let dir = root.join("hicolor").join(format!("{n}x{n}")).join("apps");
        let path = dir.join(format!("{APP_ID}.png"));
        if std::fs::read(&path).is_ok_and(|old| old == data) {
            continue;
        }
        std::fs::create_dir_all(&dir).map_err(|e| format!("create {}: {e}", dir.display()))?;
        std::fs::write(&path, data).map_err(|e| format!("write {}: {e}", path.display()))?;
    }
    Ok(())
}

/// Where windows find the icon without an install: the user's cache.
pub fn icon_cache_dir() -> PathBuf {
    let base = std::env::var_os("XDG_CACHE_HOME")
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .unwrap_or_else(|| PathBuf::from(std::env::var_os("HOME").unwrap_or_default()).join(".cache"));
    base.join("bunker").join("icons")
}

fn data_home() -> PathBuf {
    std::env::var_os("XDG_DATA_HOME")
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .unwrap_or_else(|| PathBuf::from(std::env::var_os("HOME").unwrap_or_default()).join(".local/share"))
}

fn desktop_entry(exec: &str) -> String {
    format!(
        "[Desktop Entry]
Type=Application
Name=bunker
GenericName=Terminal
Comment=A fast GTK 4 terminal
Exec={exec}
Icon={APP_ID}
Terminal=false
Categories=System;TerminalEmulator;
Keywords=terminal;shell;prompt;command;commandline;
StartupNotify=true
StartupWMClass={APP_ID}
"
    )
}

/// Installs the launcher and icons under `data_dir` (default
/// ~/.local/share), pointing at `exec` (default this binary).
fn install_desktop(data_dir: Option<PathBuf>, exec: Option<String>) -> Result<(), String> {
    let data = data_dir.unwrap_or_else(data_home);
    let exec = match exec {
        Some(e) => e,
        None => std::env::current_exe().map_err(|e| format!("find this binary: {e}"))?.display().to_string(),
    };
    write_icons(&data.join("icons"))?;
    let apps = data.join("applications");
    std::fs::create_dir_all(&apps).map_err(|e| format!("create {}: {e}", apps.display()))?;
    let path = apps.join(format!("{APP_ID}.desktop"));
    std::fs::write(&path, desktop_entry(&exec)).map_err(|e| format!("write {}: {e}", path.display()))?;
    println!("Installed {}", path.display());
    Ok(())
}

/// Runs a command. Returns `None` when `args` is not one, so a window
/// opens instead.
pub fn run(args: &[String], config_path: Option<&Path>) -> Option<Result<(), String>> {
    let path = config_path.map(Path::to_path_buf).unwrap_or_else(config::default_path);
    let load = || config::load(&path, config_path.is_some());
    let result = match args.iter().map(String::as_str).collect::<Vec<_>>().as_slice() {
        ["themes"] => {
            println!("system");
            for name in crate::theme::bundled().keys() {
                println!("{name}");
            }
            Ok(())
        }
        ["install-desktop", rest @ ..] => {
            let mut data = None;
            let mut exec = None;
            let mut it = rest.iter();
            loop {
                match it.next() {
                    Some(&"--data-dir") => data = it.next().map(PathBuf::from),
                    Some(&"--exec") => exec = it.next().map(|s| s.to_string()),
                    Some(other) => return Some(Err(format!("install-desktop: unknown argument {other:?}"))),
                    None => break,
                }
            }
            install_desktop(data, exec)
        }
        ["config", "init", rest @ ..] => {
            let force = rest.contains(&"--force");
            if path.exists() && !force {
                Err(format!("{} exists; --force overwrites it", path.display()))
            } else {
                path.parent()
                    .map(config::create_private_dir)
                    .transpose()
                    .and_then(|_| config::write_private(&path, DEFAULT_CONFIG.as_bytes()))
                    .map(|_| println!("Written: {}", path.display()))
            }
        }
        ["config", "list"] => load().map(|c| {
            for s in settings() {
                println!("{} = {}", s.name, current(&s, &c));
            }
        }),
        ["config", "get", name] => find(name).and_then(|s| load().map(|c| println!("{}", current(&s, &c)))),
        ["config", "set", name, value] => find(name).and_then(|s| {
            let v = parse_value(&s, value).map_err(|e| format!("{}: {e}", s.name))?;
            config::save(&path, s.table, s.key, v)?;
            let c = load()?;
            println!("{} = {}", s.name, current(&s, &c));
            Ok(())
        }),
        ["config", ..] => Err("usage: bunker config init [--force] | list | get SETTING | set SETTING VALUE".into()),
        ["tui", ..] => Err("bunker tui is gone; run bunk itself in a terminal".into()),
        _ => return None,
    };
    Some(result)
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn default_config_parses_to_defaults() {
        let c = config::parse(DEFAULT_CONFIG).unwrap();
        assert_eq!(Config { keys: Default::default(), ..c }, Config::default());
    }

    #[test]
    fn values_are_checked() {
        let cases = [
            ("tabs.position", "top", true),
            ("tabs.position", "middle", false),
            ("tabs.width", "300", true),
            ("tabs.width", "20", false),
            ("notify.bell", "false", true),
            ("notify.bell", "maybe", false),
            ("theme", "nord", true),
            ("theme", "nope", false),
            ("keys.new_tab", "Ctrl+T", true),
            ("keys.new_tab", "ctrl+nokey", false),
            ("keys.new_tab", "none", true),
        ];
        for (name, value, ok) in cases {
            let s = find(name).unwrap();
            assert_eq!(parse_value(&s, value).is_ok(), ok, "{name} = {value}");
        }
        assert!(find("nothing.here").is_err());
    }

    #[test]
    fn every_setting_reads() {
        let c = Config::default();
        for s in settings() {
            assert!(!current(&s, &c).is_empty(), "{}", s.name);
        }
    }

    #[test]
    fn desktop_entry_names_the_app() {
        let e = desktop_entry("/usr/bin/bunker");
        assert!(e.contains("Exec=/usr/bin/bunker\n") && e.contains(&format!("Icon={APP_ID}")));
    }
}
