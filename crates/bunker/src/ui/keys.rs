//! GDK key presses as terminal keys.

use bunker_vt::{Key, KeypadKey, Mods};
use gtk::gdk;
use gtk::gdk::Key as K;
use gtk::glib::translate::IntoGlib;

/// The modifiers of a GDK event.
pub fn mods(state: gdk::ModifierType) -> Mods {
    Mods {
        shift: state.contains(gdk::ModifierType::SHIFT_MASK),
        alt: state.contains(gdk::ModifierType::ALT_MASK),
        ctrl: state.contains(gdk::ModifierType::CONTROL_MASK),
    }
}

/// The terminal key for a GDK key, or `None` for modifier-only and
/// unmapped keys.
pub fn key(keyval: gdk::Key, state: gdk::ModifierType) -> Option<Key> {
    let kp = |k| Some(Key::Keypad(k));
    let k = match keyval {
        K::Return => Key::Enter,
        K::KP_Enter => return kp(KeypadKey::Enter),
        K::Tab | K::ISO_Left_Tab => Key::Tab,
        K::BackSpace => Key::Backspace,
        K::Escape => Key::Escape,
        K::Up => Key::Up,
        K::Down => Key::Down,
        K::Left => Key::Left,
        K::Right => Key::Right,
        K::Home => Key::Home,
        K::End => Key::End,
        K::Page_Up => Key::PageUp,
        K::Page_Down => Key::PageDown,
        K::Insert => Key::Insert,
        K::Delete => Key::Delete,
        K::KP_Up => return kp(KeypadKey::Up),
        K::KP_Down => return kp(KeypadKey::Down),
        K::KP_Left => return kp(KeypadKey::Left),
        K::KP_Right => return kp(KeypadKey::Right),
        K::KP_Home => return kp(KeypadKey::Home),
        K::KP_End => return kp(KeypadKey::End),
        K::KP_Page_Up => return kp(KeypadKey::PageUp),
        K::KP_Page_Down => return kp(KeypadKey::PageDown),
        K::KP_Insert => return kp(KeypadKey::Insert),
        K::KP_Delete => return kp(KeypadKey::Delete),
        K::KP_Begin => return kp(KeypadKey::Begin),
        K::KP_Decimal => return kp(KeypadKey::Decimal),
        K::KP_Divide => return kp(KeypadKey::Divide),
        K::KP_Multiply => return kp(KeypadKey::Multiply),
        K::KP_Subtract => return kp(KeypadKey::Subtract),
        K::KP_Add => return kp(KeypadKey::Add),
        K::KP_Equal => return kp(KeypadKey::Equal),
        K::KP_Separator => return kp(KeypadKey::Separator),
        _ => {
            let raw = keyval.into_glib();
            let (f1, kp0) = (K::F1.into_glib(), K::KP_0.into_glib());
            if (f1..=K::F12.into_glib()).contains(&raw) {
                return Some(Key::F((raw - f1 + 1) as u8));
            }
            if (kp0..=K::KP_9.into_glib()).contains(&raw) {
                return kp(KeypadKey::Digit((raw - kp0) as u8));
            }
            let mut c = keyval.to_unicode()?;
            if c.is_control() {
                return None;
            }
            // Ctrl and Alt combinations use the unshifted letter.
            if state.intersects(gdk::ModifierType::CONTROL_MASK | gdk::ModifierType::ALT_MASK)
                && let Some(lower) = keyval.to_lower().to_unicode()
                && lower.is_ascii_lowercase()
            {
                c = lower;
            }
            Key::Char(c)
        }
    };
    Some(k)
}

/// The actions keys can be bound to: config name, default key (the Go
/// version's defaults, so one config serves both), and what it does.
pub const ACTIONS: [(&str, &str, &str); 18] = [
    ("new_tab", "ctrl+shift+t", "New tab in the current directory"),
    ("close_tab", "ctrl+shift+w", "Close tab"),
    ("next_tab", "ctrl+pgdn", "Next tab"),
    ("prev_tab", "ctrl+pgup", "Previous tab"),
    ("copy", "ctrl+c", "Copy the selection; without one, Ctrl+C goes to the program"),
    ("paste", "ctrl+v", "Paste"),
    ("copy_clipboard", "ctrl+shift+c", "Copy the selection"),
    ("paste_clipboard", "ctrl+shift+v", "Paste"),
    ("paste_clipboard_alt", "shift+insert", "Paste (alternative key)"),
    ("search", "ctrl+f", "Find in the terminal"),
    ("scroll_up", "shift+pgup", "Scroll back a page"),
    ("scroll_down", "shift+pgdn", "Scroll forward a page"),
    ("font_bigger", "ctrl+shift+plus", "Make text bigger"),
    ("font_smaller", "ctrl+shift+minus", "Make text smaller"),
    ("font_reset", "ctrl+shift+0", "Reset text size"),
    ("passthrough", "ctrl+f12", "Send every key to the program (PASS)"),
    ("preferences", "ctrl+comma", "Preferences"),
    ("close_window", "ctrl+shift+q", "Close window"),
];

/// Writing a shortcut that switches it off.
pub const NONE: &str = "none";

/// A key with its modifiers.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Combo {
    keyval: gdk::Key,
    ctrl: bool,
    alt: bool,
    shift: bool,
}

/// Names for keys that are not a letter, digit, or function key.
const NAMED: &[(&str, K)] = &[
    ("up", K::Up),
    ("down", K::Down),
    ("left", K::Left),
    ("right", K::Right),
    ("pgup", K::Page_Up),
    ("pgdn", K::Page_Down),
    ("home", K::Home),
    ("end", K::End),
    ("enter", K::Return),
    ("escape", K::Escape),
    ("backspace", K::BackSpace),
    ("delete", K::Delete),
    ("tab", K::Tab),
    ("insert", K::Insert),
    ("space", K::space),
    ("comma", K::comma),
    ("period", K::period),
    ("slash", K::slash),
    ("backslash", K::backslash),
    ("semicolon", K::semicolon),
    ("apostrophe", K::apostrophe),
    ("grave", K::grave),
    ("minus", K::minus),
    ("equal", K::equal),
    ("plus", K::plus),
    ("underscore", K::underscore),
    ("bracketleft", K::bracketleft),
    ("bracketright", K::bracketright),
];

const ALIASES: &[(&str, &str)] = &[
    ("esc", "escape"),
    ("return", "enter"),
    ("pageup", "pgup"),
    ("pagedown", "pgdn"),
    ("del", "delete"),
    ("ins", "insert"),
];

/// Turns a lowercased GTK accelerator ("<control><shift>t", as gsettings
/// writes them) into [keys] syntax.
fn from_gtk_accel(s: &str) -> String {
    let mut rest = s;
    let mut parts = Vec::new();
    while let Some(after) = rest.strip_prefix('<') {
        let Some(end) = after.find('>') else { break };
        parts.push(match &after[..end] {
            "control" | "ctrl" | "primary" => "ctrl".to_string(),
            "mod1" | "alt" => "alt".to_string(),
            other => other.to_string(),
        });
        rest = &after[end + 1..];
    }
    let key = match rest {
        "page_up" | "prior" => "pgup",
        "page_down" | "next" => "pgdn",
        "iso_left_tab" => "tab",
        k => k,
    };
    parts.push(key.to_string());
    parts.join("+")
}

fn keyval_for(name: &str) -> Option<gdk::Key> {
    if let Some((_, k)) = NAMED.iter().find(|(n, _)| *n == name) {
        return Some(*k);
    }
    if let Some(n) = name.strip_prefix('f').and_then(|n| n.parse::<u32>().ok())
        && (1..=24).contains(&n)
    {
        return gdk::Key::from_name(format!("F{n}"));
    }
    let mut chars = name.chars();
    match (chars.next(), chars.next()) {
        (Some(c), None) if c.is_ascii_lowercase() || c.is_ascii_digit() => gdk::Key::from_name(c.to_string()),
        _ => None,
    }
}

fn name_for(keyval: gdk::Key) -> Option<String> {
    let k = keyval.to_lower();
    if let Some((n, _)) = NAMED.iter().find(|(_, v)| *v == k) {
        return Some((*n).to_string());
    }
    let raw = k.into_glib();
    if (K::F1.into_glib()..=K::F24.into_glib()).contains(&raw) {
        return Some(format!("f{}", raw - K::F1.into_glib() + 1));
    }
    k.to_unicode().filter(|c| c.is_ascii_lowercase() || c.is_ascii_digit()).map(|c| c.to_string())
}

impl Combo {
    /// Parses `ctrl+shift+t` (modifiers in any order, aliases allowed).
    /// `none` gives `None`.
    pub fn parse(s: &str) -> Result<Option<Self>, String> {
        let mut s = s.trim().to_lowercase();
        if s == NONE {
            return Ok(None);
        }
        if s.starts_with('<') {
            s = from_gtk_accel(&s);
        }
        let parts: Vec<&str> = s.split('+').collect();
        let (last, mods) = parts.split_last().ok_or_else(|| format!("empty key {s:?}"))?;
        let name = ALIASES.iter().find(|(a, _)| a == last).map_or(*last, |(_, n)| *n);
        let keyval = keyval_for(name).ok_or_else(|| format!("unknown key {last:?} in {s:?}"))?;
        let mut c = Self { keyval, ctrl: false, alt: false, shift: false };
        for m in mods {
            match *m {
                "ctrl" => c.ctrl = true,
                "alt" => c.alt = true,
                "shift" => c.shift = true,
                other => return Err(format!("unknown modifier {other:?} in {s:?}")),
            }
        }
        Ok(Some(c))
    }

    /// The combo of a key press, if the key can be bound.
    pub fn from_press(keyval: gdk::Key, state: gdk::ModifierType) -> Option<Self> {
        let m = mods(state);
        // The unshifted key: Shift+1 is recorded as shift+1, not "!".
        let keyval = match keyval.to_lower() {
            k if name_for(k).is_some() => k,
            _ => return None,
        };
        Some(Self { keyval, ctrl: m.ctrl, alt: m.alt, shift: m.shift })
    }

    /// The config spelling: lowercase, modifiers as ctrl, alt, shift.
    pub fn to_config(self) -> String {
        let mut out = Vec::new();
        for (on, name) in [(self.ctrl, "ctrl"), (self.alt, "alt"), (self.shift, "shift")] {
            if on {
                out.push(name.to_string());
            }
        }
        out.push(name_for(self.keyval).unwrap_or_default());
        out.join("+")
    }

    /// How the key reads in the interface: "Ctrl+Shift+T".
    pub fn label(self) -> String {
        let mut out: Vec<String> = Vec::new();
        for (on, name) in [(self.ctrl, "Ctrl"), (self.alt, "Alt"), (self.shift, "Shift")] {
            if on {
                out.push(name.into());
            }
        }
        let key = name_for(self.keyval).unwrap_or_default();
        out.push(match key.as_str() {
            "pgup" => "Page Up".into(),
            "pgdn" => "Page Down".into(),
            "comma" => ",".into(),
            "period" => ".".into(),
            "minus" => "-".into(),
            "plus" => "+".into(),
            "equal" => "=".into(),
            "slash" => "/".into(),
            k if k.len() == 1 => k.to_uppercase(),
            k => {
                let mut c = k.chars();
                c.next().map(|f| f.to_uppercase().chain(c).collect()).unwrap_or_default()
            }
        });
        out.join("+")
    }

    /// Reports whether a key press is this combo. Letters match in either
    /// case, and Shift with a symbol matches its shifted keyval too, so
    /// Ctrl+Shift+Plus works where plus needs Shift.
    pub fn matches(&self, keyval: gdk::Key, state: gdk::ModifierType) -> bool {
        let m = mods(state);
        if m.ctrl != self.ctrl || m.alt != self.alt {
            return false;
        }
        let kv = keyval.to_lower();
        if kv == self.keyval && m.shift == self.shift {
            return true;
        }
        self.shift && m.shift && (keyval == self.keyval || (self.keyval == K::plus && keyval == K::equal))
    }
}

/// An action and the key bound to it.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Shortcut {
    /// The action name.
    pub action: &'static str,
    /// The key, unless switched off.
    pub combo: Option<Combo>,
}

/// Checks a key for the config and returns its canonical spelling.
pub fn canonical_key(s: &str) -> Result<String, String> {
    Ok(Combo::parse(s)?.map_or_else(|| NONE.to_string(), Combo::to_config))
}

impl Shortcut {
    /// The key in config spelling, unless switched off.
    pub fn config_key(&self) -> Option<String> {
        self.combo.map(Combo::to_config)
    }

    /// Reports whether a key press runs this shortcut.
    pub fn matches(&self, keyval: gdk::Key, state: gdk::ModifierType) -> bool {
        self.combo.is_some_and(|c| c.matches(keyval, state))
    }
}

/// The shortcuts with the config's [keys] applied; a key that does not
/// parse keeps the default and is reported.
pub fn shortcuts(keys: &std::collections::BTreeMap<String, String>) -> Vec<Shortcut> {
    ACTIONS
        .iter()
        .map(|(action, default, _)| {
            let combo = match keys.get(*action).map(|k| Combo::parse(k)) {
                Some(Ok(c)) => c,
                Some(Err(e)) => {
                    crate::warn!("[keys] {action}: {e}");
                    Combo::parse(default).ok().flatten()
                }
                None => Combo::parse(default).ok().flatten(),
            };
            Shortcut { action, combo }
        })
        .collect()
}

#[cfg(test)]
mod tests {
    use super::*;
    use gdk::ModifierType as M;

    #[test]
    fn maps_keys() {
        assert_eq!(key(K::a, M::empty()), Some(Key::Char('a')));
        assert_eq!(key(K::A, M::SHIFT_MASK), Some(Key::Char('A')));
        assert_eq!(key(K::A, M::SHIFT_MASK | M::CONTROL_MASK), Some(Key::Char('a')));
        assert_eq!(key(K::F5, M::empty()), Some(Key::F(5)));
        assert_eq!(key(K::KP_3, M::empty()), Some(Key::Keypad(KeypadKey::Digit(3))));
        assert_eq!(key(K::Shift_L, M::empty()), None);
    }

    #[test]
    fn shortcuts_match() {
        let all = shortcuts(&Default::default());
        let find = |kv, st| all.iter().find(|s| s.matches(kv, st)).map(|s| s.action);
        assert_eq!(find(K::T, M::CONTROL_MASK | M::SHIFT_MASK), Some("new_tab"));
        assert_eq!(find(K::c, M::CONTROL_MASK), Some("copy"));
        assert_eq!(find(K::C, M::CONTROL_MASK | M::SHIFT_MASK), Some("copy_clipboard"));
        assert_eq!(find(K::plus, M::CONTROL_MASK | M::SHIFT_MASK), Some("font_bigger"));
        assert_eq!(find(K::equal, M::CONTROL_MASK | M::SHIFT_MASK), Some("font_bigger"));
        assert_eq!(find(K::F12, M::CONTROL_MASK), Some("passthrough"));
        assert_eq!(find(K::a, M::CONTROL_MASK), None);
    }

    #[test]
    fn config_keys() {
        let mut keys = std::collections::BTreeMap::new();
        keys.insert("new_tab".to_string(), "Ctrl+T".to_string());
        keys.insert("close_tab".to_string(), "none".to_string());
        keys.insert("search".to_string(), "hyper+x".to_string());
        let all = shortcuts(&keys);
        let get = |a: &str| all.iter().find(|s| s.action == a).unwrap().combo;
        assert_eq!(get("new_tab").map(|c| c.to_config()).as_deref(), Some("ctrl+t"));
        assert_eq!(get("close_tab"), None);
        assert_eq!(get("search").map(|c| c.to_config()).as_deref(), Some("ctrl+f"), "a bad key keeps the default");
    }

    #[test]
    fn spellings() {
        let cases = [
            ("shift+ctrl+t", "ctrl+shift+t", "Ctrl+Shift+T"),
            ("ctrl+pagedown", "ctrl+pgdn", "Ctrl+Page Down"),
            ("ctrl+comma", "ctrl+comma", "Ctrl+,"),
            ("f12", "f12", "F12"),
            ("alt+esc", "alt+escape", "Alt+Escape"),
            ("<Control>Right", "ctrl+right", "Ctrl+Right"),
            ("<Primary><Shift>Page_Up", "ctrl+shift+pgup", "Ctrl+Shift+Page Up"),
        ];
        for (input, config, label) in cases {
            let c = Combo::parse(input).unwrap().unwrap();
            assert_eq!(c.to_config(), config, "{input}");
            assert_eq!(c.label(), label, "{input}");
        }
        assert!(Combo::parse("ctrl+nokey").is_err());
        assert_eq!(Combo::parse("none"), Ok(None));
    }
}
