//! The config file, ~/.config/bunker/config.toml, shared with the Go
//! version of bunker. Unknown keys are left alone.

use std::path::{Path, PathBuf};

use toml_edit::{DocumentMut, Item};

/// Where the tab list sits.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum TabPosition {
    /// A sidebar on the left.
    Left,
    /// A sidebar on the right.
    Right,
    /// A bar above the terminal.
    Top,
    /// A bar below the terminal.
    Bottom,
}

/// Settings bunker reads.
#[derive(Clone, Debug, PartialEq)]
pub struct Config {
    /// `system` or a bundled theme name.
    pub theme: String,
    /// Pango font description.
    pub font: String,
    /// Rows of history per pane.
    pub scrollback: usize,
    /// Pixels between the window edge and the text.
    pub padding: u32,
    /// Tab list position.
    pub tab_position: TabPosition,
    /// Sidebar width in pixels.
    pub tab_width: u32,
    /// Hide the tab list while only one tab is open.
    pub tab_autohide: bool,
    /// How long a command must run before its end lights up its tab;
    /// 0 never does.
    pub command_seconds: u64,
    /// The bell lights up its tab.
    pub notify_bell: bool,
    /// When alerts become desktop notifications: unfocused, always, never.
    pub notify_desktop: String,
    /// Shortcuts from [keys], by action, as written.
    pub keys: std::collections::BTreeMap<String, String>,
    /// The file the settings came from, where changes are saved.
    pub path: Option<PathBuf>,
}

impl Default for Config {
    fn default() -> Self {
        Self {
            theme: "system".into(),
            font: "Monospace 11".into(),
            scrollback: 10_000,
            padding: 8,
            tab_position: TabPosition::Left,
            tab_width: 220,
            tab_autohide: false,
            command_seconds: 10,
            notify_bell: true,
            notify_desktop: "unfocused".into(),
            keys: Default::default(),
            path: None,
        }
    }
}

/// The default config path.
pub fn default_path() -> PathBuf {
    let base = std::env::var_os("XDG_CONFIG_HOME")
        .map(PathBuf::from)
        .filter(|p| p.is_absolute())
        .unwrap_or_else(|| PathBuf::from(std::env::var_os("HOME").unwrap_or_default()).join(".config"));
    base.join("bunker").join("config.toml")
}

fn str_at<'a>(doc: &'a DocumentMut, table: Option<&str>, key: &str) -> Option<&'a str> {
    item_at(doc, table, key)?.as_str()
}

fn item_at<'a>(doc: &'a DocumentMut, table: Option<&str>, key: &str) -> Option<&'a Item> {
    match table {
        Some(t) => doc.get(t)?.get(key),
        None => doc.get(key),
    }
}

fn int_at(doc: &DocumentMut, table: Option<&str>, key: &str) -> Option<i64> {
    item_at(doc, table, key)?.as_integer()
}

fn bool_at(doc: &DocumentMut, table: Option<&str>, key: &str) -> Option<bool> {
    item_at(doc, table, key)?.as_bool()
}

/// Parses config text; missing keys keep their defaults.
pub fn parse(text: &str) -> Result<Config, String> {
    let doc: DocumentMut = text.parse().map_err(|e| format!("{e}"))?;
    let mut c = Config::default();
    if let Some(v) = str_at(&doc, None, "theme").filter(|v| !v.is_empty()) {
        c.theme = v.to_string();
    }
    if let Some(v) = str_at(&doc, None, "font").filter(|v| !v.is_empty()) {
        c.font = v.to_string();
    }
    if let Some(v) = int_at(&doc, None, "scrollback").filter(|&v| v > 0) {
        c.scrollback = v as usize;
    }
    if let Some(v) = int_at(&doc, Some("window"), "padding").filter(|&v| (0..=200).contains(&v)) {
        c.padding = v as u32;
    }
    if let Some(v) = str_at(&doc, Some("tabs"), "position") {
        c.tab_position = match v {
            "left" => TabPosition::Left,
            "right" => TabPosition::Right,
            "top" => TabPosition::Top,
            "bottom" => TabPosition::Bottom,
            other => return Err(format!("tabs.position: unknown position {other:?}")),
        };
    }
    if let Some(v) = int_at(&doc, Some("tabs"), "width").filter(|&v| (60..=1000).contains(&v)) {
        c.tab_width = v as u32;
    }
    if let Some(v) = bool_at(&doc, Some("tabs"), "autohide") {
        c.tab_autohide = v;
    }
    if let Some(v) = int_at(&doc, Some("notify"), "command_seconds") {
        c.command_seconds = v.max(0) as u64;
    }
    if let Some(v) = bool_at(&doc, Some("notify"), "bell") {
        c.notify_bell = v;
    }
    if let Some(v) = str_at(&doc, Some("notify"), "desktop") {
        c.notify_desktop = match v {
            "unfocused" | "always" | "never" => v.to_string(),
            other => return Err(format!("notify.desktop: unknown value {other:?}")),
        };
    }
    if let Some(keys) = doc.get("keys").and_then(Item::as_table_like) {
        for (action, value) in keys.iter() {
            if let Some(v) = value.as_str() {
                c.keys.insert(action.to_string(), v.to_string());
            }
        }
    }
    Ok(c)
}

/// Loads the config. A missing file at the default path gives the
/// defaults; a missing explicit path or a malformed file is an error.
pub fn load(path: &Path, explicit: bool) -> Result<Config, String> {
    let mut c = match std::fs::read_to_string(path) {
        Ok(text) => parse(&text).map_err(|e| format!("{}: {e}", path.display()))?,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound && !explicit => Config::default(),
        Err(e) => return Err(format!("read {}: {e}", path.display())),
    };
    c.path = Some(path.to_path_buf());
    Ok(c)
}

/// Returns `text` with `key` (in `table`, or at the top) set to `value`,
/// keeping comments, the key's trailing comment, and everything else.
pub fn set_value(text: &str, table: Option<&str>, key: &str, value: toml_edit::Value) -> Result<String, String> {
    let mut doc: DocumentMut = text.parse().map_err(|e| format!("{e}"))?;
    let t = match table {
        Some(name) => {
            if doc.get(name).is_none() {
                doc[name] = toml_edit::table();
            }
            doc[name].as_table_like_mut().ok_or_else(|| format!("{name} is not a table"))?
        }
        None => doc.as_table_mut() as &mut dyn toml_edit::TableLike,
    };
    match t.get_mut(key).and_then(Item::as_value_mut) {
        Some(v) => {
            let decor = v.decor().clone();
            *v = value;
            *v.decor_mut() = decor;
        }
        None => {
            t.insert(key, Item::Value(value));
        }
    }
    Ok(doc.to_string())
}

/// Saves one setting to the config file at `path`, replacing the file
/// atomically. Other settings and comments are kept.
pub fn save(path: &Path, table: Option<&str>, key: &str, value: toml_edit::Value) -> Result<(), String> {
    let text = match std::fs::read_to_string(path) {
        Ok(t) => t,
        Err(e) if e.kind() == std::io::ErrorKind::NotFound => String::new(),
        Err(e) => return Err(format!("read {}: {e}", path.display())),
    };
    let out = set_value(&text, table, key, value).map_err(|e| format!("{}: {e}", path.display()))?;
    if let Some(dir) = path.parent() {
        create_private_dir(dir)?;
    }
    let tmp = path.with_extension("toml.tmp");
    write_private(&tmp, out.as_bytes())?;
    std::fs::rename(&tmp, path).map_err(|e| format!("replace {}: {e}", path.display()))
}

/// Creates a directory (and its parents) only its owner can enter.
pub fn create_private_dir(dir: &Path) -> Result<(), String> {
    use std::os::unix::fs::DirBuilderExt;
    std::fs::DirBuilder::new()
        .recursive(true)
        .mode(0o700)
        .create(dir)
        .map_err(|e| format!("create {}: {e}", dir.display()))
}

/// Writes a file only its owner can read (mode 0600): a config may name
/// hosts and commands.
pub fn write_private(path: &Path, data: &[u8]) -> Result<(), String> {
    use std::io::Write;
    use std::os::unix::fs::OpenOptionsExt;
    let mut f = std::fs::OpenOptions::new()
        .write(true)
        .create(true)
        .truncate(true)
        .mode(0o600)
        .open(path)
        .map_err(|e| format!("write {}: {e}", path.display()))?;
    f.write_all(data).map_err(|e| format!("write {}: {e}", path.display()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parses_known_keys() {
        let c = parse(
            "theme = \"dark-pastel\"\nfont = \"Mono 12\"\n[window]\npadding = 4\n[tabs]\nposition = \"top\"\nwidth = 180\n[keys]\nsplit = \"f1\"\n",
        )
        .unwrap();
        assert_eq!(c.theme, "dark-pastel");
        assert_eq!(c.font, "Mono 12");
        assert_eq!(c.padding, 4);
        assert_eq!(c.tab_position, TabPosition::Top);
        assert_eq!(c.tab_width, 180);
        assert_eq!(c.keys.get("split").map(String::as_str), Some("f1"));
    }

    #[test]
    fn set_value_keeps_comments() {
        let text = "# top\ntheme = \"tango\" # colours\n[tabs]\nwidth = 220  # sidebar\n";
        assert_eq!(
            set_value(text, Some("tabs"), "width", 300.into()).unwrap(),
            "# top\ntheme = \"tango\" # colours\n[tabs]\nwidth = 300  # sidebar\n"
        );
        assert_eq!(
            set_value(text, None, "theme", "nord".into()).unwrap(),
            "# top\ntheme = \"nord\" # colours\n[tabs]\nwidth = 220  # sidebar\n"
        );
        assert_eq!(set_value("", Some("tabs"), "width", 180.into()).unwrap(), "[tabs]\nwidth = 180\n");
        assert_eq!(set_value("", None, "font", "Mono 12".into()).unwrap(), "font = \"Mono 12\"\n");
    }

    #[test]
    fn saved_config_is_private() {
        use std::os::unix::fs::PermissionsExt;
        let dir = std::env::temp_dir().join(format!("bunker-config-test-{}", std::process::id()));
        let path = dir.join("config.toml");
        save(&path, Some("tabs"), "width", 300.into()).unwrap();
        let mode = std::fs::metadata(&path).unwrap().permissions().mode() & 0o777;
        std::fs::remove_dir_all(&dir).unwrap();
        assert_eq!(mode, 0o600);
    }

    #[test]
    fn empty_is_default() {
        assert_eq!(parse("").unwrap(), Config::default());
    }

    #[test]
    fn rejects_bad_input() {
        assert!(parse("theme = ").is_err());
        assert!(parse("[tabs]\nposition = \"middle\"").is_err());
    }
}
