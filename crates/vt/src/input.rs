//! Encoding keys, mouse events, and pastes into the bytes a program reads,
//! following the modes it set: application cursor and keypad keys, the
//! kitty keyboard protocol, and the mouse reporting modes.

use std::io::Write;

use crate::term::MouseMode;

/// A key on the main keyboard or the keypad.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Key {
    /// A key that types a character, as typed (Shift applied).
    Char(char),
    /// Return.
    Enter,
    /// Tab (Shift+Tab is Tab with Shift).
    Tab,
    /// Backspace.
    Backspace,
    /// Escape.
    Escape,
    /// Up arrow.
    Up,
    /// Down arrow.
    Down,
    /// Left arrow.
    Left,
    /// Right arrow.
    Right,
    /// Home.
    Home,
    /// End.
    End,
    /// Page Up.
    PageUp,
    /// Page Down.
    PageDown,
    /// Insert.
    Insert,
    /// Delete.
    Delete,
    /// F1 to F12.
    F(u8),
    /// A keypad key.
    Keypad(KeypadKey),
}

/// Keypad keys, which programs can tell apart from the main keyboard.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum KeypadKey {
    /// 0 to 9.
    Digit(u8),
    /// The decimal point.
    Decimal,
    /// /.
    Divide,
    /// *.
    Multiply,
    /// -.
    Subtract,
    /// +.
    Add,
    /// =.
    Equal,
    /// The thousands separator.
    Separator,
    /// Enter.
    Enter,
    /// Up (8 without Num Lock).
    Up,
    /// Down.
    Down,
    /// Left.
    Left,
    /// Right.
    Right,
    /// Home.
    Home,
    /// End.
    End,
    /// Page Up.
    PageUp,
    /// Page Down.
    PageDown,
    /// Insert.
    Insert,
    /// Delete.
    Delete,
    /// 5 without Num Lock.
    Begin,
}

/// Held modifiers.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct Mods {
    /// Shift.
    pub shift: bool,
    /// Alt.
    pub alt: bool,
    /// Control.
    pub ctrl: bool,
}

impl Mods {
    /// The xterm and kitty modifier parameter: 1 plus the modifier bits.
    pub fn param(self) -> u8 {
        1 + u8::from(self.shift) + 2 * u8::from(self.alt) + 4 * u8::from(self.ctrl)
    }
}

/// The modes that change how keys are encoded.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct KeyModes {
    /// DECCKM.
    pub app_cursor: bool,
    /// DECKPAM.
    pub app_keypad: bool,
    /// The active kitty keyboard flags.
    pub kitty: u8,
}

/// Kitty flag 1: disambiguate escape codes.
const KITTY_DISAMBIGUATE: u8 = 1;
/// Kitty flag 8: report all keys as escape codes.
const KITTY_ALL_KEYS: u8 = 8;

/// The kitty code and the SS3 final byte of a keypad key.
fn keypad_codes(k: KeypadKey) -> (u32, u8) {
    use KeypadKey::*;
    match k {
        Digit(d) => (57399 + u32::from(d), b'p' + d),
        Decimal => (57409, b'n'),
        Divide => (57410, b'o'),
        Multiply => (57411, b'j'),
        Subtract => (57412, b'm'),
        Add => (57413, b'k'),
        Enter => (57414, b'M'),
        Equal => (57415, b'X'),
        Separator => (57416, b'l'),
        Left => (57417, b't'),
        Right => (57418, b'v'),
        Up => (57419, b'x'),
        Down => (57420, b'r'),
        PageUp => (57421, b'y'),
        PageDown => (57422, b's'),
        Home => (57423, b'w'),
        End => (57424, b'q'),
        Insert => (57425, b'p'),
        Delete => (57426, b'n'),
        Begin => (57427, b'u'),
    }
}

/// The main-keyboard key a keypad key types when nothing asks for keypad
/// codes.
fn keypad_plain(k: KeypadKey) -> Option<Key> {
    use KeypadKey::*;
    Some(match k {
        Digit(d) => Key::Char(char::from(b'0' + d)),
        Decimal => Key::Char('.'),
        Divide => Key::Char('/'),
        Multiply => Key::Char('*'),
        Subtract => Key::Char('-'),
        Add => Key::Char('+'),
        Equal => Key::Char('='),
        Separator => Key::Char(','),
        Enter => Key::Enter,
        Up => Key::Up,
        Down => Key::Down,
        Left => Key::Left,
        Right => Key::Right,
        Home => Key::Home,
        End => Key::End,
        PageUp => Key::PageUp,
        PageDown => Key::PageDown,
        Insert => Key::Insert,
        Delete => Key::Delete,
        Begin => return None,
    })
}

/// xterm's control byte for Ctrl with a symbol or digit.
fn ctrl_symbol(c: char) -> Option<u8> {
    Some(match c {
        ' ' | '@' | '2' => 0x00,
        '[' | '3' => 0x1b,
        '\\' | '4' => 0x1c,
        ']' | '5' => 0x1d,
        '^' | '6' | '~' => 0x1e,
        '_' | '/' | '-' | '7' | '?' => 0x1f,
        '8' => 0x7f,
        _ => return None,
    })
}

fn with_alt(alt: bool, bytes: &[u8]) -> Vec<u8> {
    let mut out = Vec::with_capacity(bytes.len() + 1);
    if alt {
        out.push(0x1b);
    }
    out.extend_from_slice(bytes);
    out
}

fn csi_u(code: u32, m: u8) -> Vec<u8> {
    let mut out = Vec::new();
    if m > 1 {
        let _ = write!(out, "\x1b[{code};{m}u");
    } else {
        let _ = write!(out, "\x1b[{code}u");
    }
    out
}

/// A cursor or editing key: `CSI final`, `SS3 final`, or `CSI 1;m final`.
fn cursor_key(final_byte: u8, mods: Mods, app: bool) -> Vec<u8> {
    let m = mods.param();
    if m > 1 {
        let mut out = Vec::new();
        let _ = write!(out, "\x1b[1;{m}{}", char::from(final_byte));
        out
    } else if app {
        vec![0x1b, b'O', final_byte]
    } else {
        vec![0x1b, b'[', final_byte]
    }
}

/// A tilde key: `CSI code ~` or `CSI code;m ~`.
fn tilde_key(code: u8, mods: Mods) -> Vec<u8> {
    let m = mods.param();
    let mut out = Vec::new();
    if m > 1 {
        let _ = write!(out, "\x1b[{code};{m}~");
    } else {
        let _ = write!(out, "\x1b[{code}~");
    }
    out
}

/// The bytes for a key press; empty when the key sends nothing.
pub fn encode_key(key: Key, mods: Mods, modes: KeyModes) -> Vec<u8> {
    let kitty = modes.kitty & KITTY_DISAMBIGUATE != 0;
    let all_keys = modes.kitty & KITTY_ALL_KEYS != 0;
    let m = mods.param();
    match key {
        Key::Keypad(k) => {
            let (code, ss3) = keypad_codes(k);
            if kitty {
                let mut out = Vec::new();
                let _ = write!(out, "\x1b[{code};{m}u");
                return out;
            }
            if modes.app_keypad && !matches!(k, KeypadKey::Begin) {
                return vec![0x1b, b'O', ss3];
            }
            match keypad_plain(k) {
                Some(plain) => encode_key(plain, mods, modes),
                None => Vec::new(),
            }
        }
        Key::Char(c) => {
            let lower = c.to_ascii_lowercase();
            if kitty && (mods.ctrl || mods.alt || all_keys) {
                // Kitty reports the unshifted key with the modifiers.
                return csi_u(u32::from(if c.is_ascii_alphabetic() { lower } else { c }), m);
            }
            if mods.ctrl {
                if lower.is_ascii_lowercase() {
                    return with_alt(mods.alt, &[lower as u8 - b'a' + 1]);
                }
                if let Some(b) = ctrl_symbol(c) {
                    return with_alt(mods.alt, &[b]);
                }
            }
            let mut buf = [0u8; 4];
            with_alt(mods.alt, c.encode_utf8(&mut buf).as_bytes())
        }
        Key::Enter | Key::Tab | Key::Backspace | Key::Escape if kitty => {
            let (code, legacy): (u32, &[u8]) = match key {
                Key::Enter => (13, b"\r"),
                Key::Tab => (9, b"\t"),
                Key::Backspace => (127, b"\x7f"),
                _ => (27, b"\x1b"),
            };
            // Unmodified Enter, Tab, and Backspace stay legacy unless every
            // key is to be reported, so a shell still works after a program
            // exits without popping its flags.
            if m == 1 && !all_keys && code != 27 {
                return legacy.to_vec();
            }
            csi_u(code, m)
        }
        Key::Enter => with_alt(mods.alt, b"\r"),
        Key::Tab if mods.shift => b"\x1b[Z".to_vec(),
        Key::Tab => with_alt(mods.alt, b"\t"),
        Key::Backspace => with_alt(mods.alt, if mods.ctrl { b"\x08" } else { b"\x7f" }),
        Key::Escape => with_alt(mods.alt, b"\x1b"),
        Key::Up => cursor_key(b'A', mods, modes.app_cursor),
        Key::Down => cursor_key(b'B', mods, modes.app_cursor),
        Key::Right => cursor_key(b'C', mods, modes.app_cursor),
        Key::Left => cursor_key(b'D', mods, modes.app_cursor),
        Key::Home => cursor_key(b'H', mods, modes.app_cursor),
        Key::End => cursor_key(b'F', mods, modes.app_cursor),
        Key::PageUp => tilde_key(5, mods),
        Key::PageDown => tilde_key(6, mods),
        Key::Insert => tilde_key(2, mods),
        Key::Delete => tilde_key(3, mods),
        Key::F(n @ 1..=4) => cursor_key(b'P' + n - 1, mods, true),
        Key::F(n @ 5..=12) => tilde_key([15, 17, 18, 19, 20, 21, 23, 24][usize::from(n - 5)], mods),
        Key::F(_) => Vec::new(),
    }
}

/// A mouse button, or a wheel direction.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MouseButton {
    /// Primary.
    Left,
    /// Middle.
    Middle,
    /// Secondary.
    Right,
    /// Wheel up.
    WheelUp,
    /// Wheel down.
    WheelDown,
    /// Wheel left.
    WheelLeft,
    /// Wheel right.
    WheelRight,
    /// No button (motion only).
    None,
}

/// What happened.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum MouseAction {
    /// A button went down (or a wheel turned).
    Press,
    /// A button went up.
    Release,
    /// The pointer moved, with `button` held (or none).
    Motion,
}

/// The bytes reporting a mouse event, or `None` when the program did not
/// ask for this kind of event. `col` and `row` are zero-based.
pub fn encode_mouse(
    mode: MouseMode,
    sgr: bool,
    button: MouseButton,
    action: MouseAction,
    col: usize,
    row: usize,
    mods: Mods,
) -> Option<Vec<u8>> {
    let wheel = matches!(
        button,
        MouseButton::WheelUp | MouseButton::WheelDown | MouseButton::WheelLeft | MouseButton::WheelRight
    );
    let wanted = match (mode, action) {
        (MouseMode::Off, _) => false,
        (MouseMode::X10, MouseAction::Press) => true,
        (MouseMode::X10, _) => false,
        (_, MouseAction::Press) => true,
        (_, MouseAction::Release) => !wheel,
        (MouseMode::Drag, MouseAction::Motion) => button != MouseButton::None,
        (MouseMode::Motion, MouseAction::Motion) => true,
        (_, MouseAction::Motion) => false,
    };
    if !wanted {
        return None;
    }
    let mut code: u32 = match button {
        MouseButton::Left => 0,
        MouseButton::Middle => 1,
        MouseButton::Right => 2,
        MouseButton::None => 3,
        MouseButton::WheelUp => 64,
        MouseButton::WheelDown => 65,
        MouseButton::WheelLeft => 66,
        MouseButton::WheelRight => 67,
    };
    if action == MouseAction::Motion {
        code += 32;
    }
    if mode != MouseMode::X10 {
        code += 4 * u32::from(mods.shift) + 8 * u32::from(mods.alt) + 16 * u32::from(mods.ctrl);
    }
    let mut out = Vec::new();
    if sgr {
        let end = if action == MouseAction::Release { 'm' } else { 'M' };
        let _ = write!(out, "\x1b[<{code};{};{}{end}", col + 1, row + 1);
    } else {
        if action == MouseAction::Release {
            code = 3 + (code & !3);
        }
        let pos = |v: usize| (v + 33).min(255) as u8;
        out.extend_from_slice(&[0x1b, b'[', b'M', (32 + code).min(255) as u8, pos(col), pos(row)]);
    }
    Some(out)
}

/// The bytes for pasted text: newlines become carriage returns, and with
/// bracketed paste the text is wrapped and stripped of escapes, so a paste
/// cannot end the bracket early.
pub fn encode_paste(text: &str, bracketed: bool) -> Vec<u8> {
    let text = text.replace("\r\n", "\r").replace('\n', "\r");
    if !bracketed {
        return text.into_bytes();
    }
    let mut out = b"\x1b[200~".to_vec();
    out.extend(text.chars().filter(|&c| c != '\x1b' && c != '\u{9b}').collect::<String>().bytes());
    out.extend_from_slice(b"\x1b[201~");
    out
}

/// The focus report for a program that asked for one (DECSET 1004).
pub fn encode_focus(focused: bool) -> &'static [u8] {
    if focused { b"\x1b[I" } else { b"\x1b[O" }
}

#[cfg(test)]
mod tests {
    use super::*;

    const NONE: Mods = Mods { shift: false, alt: false, ctrl: false };
    const SHIFT: Mods = Mods { shift: true, alt: false, ctrl: false };
    const ALT: Mods = Mods { shift: false, alt: true, ctrl: false };
    const CTRL: Mods = Mods { shift: false, alt: false, ctrl: true };

    fn enc(key: Key, mods: Mods, modes: KeyModes) -> String {
        String::from_utf8(encode_key(key, mods, modes)).unwrap()
    }

    #[test]
    fn legacy_keys() {
        let m = KeyModes::default();
        let cases: &[(Key, Mods, &str)] = &[
            (Key::Char('a'), NONE, "a"),
            (Key::Char('a'), CTRL, "\x01"),
            (Key::Char('A'), Mods { shift: true, ctrl: true, alt: false }, "\x01"),
            (Key::Char('a'), ALT, "\x1ba"),
            (Key::Char('é'), NONE, "é"),
            (Key::Char(' '), CTRL, "\x00"),
            (Key::Char('['), CTRL, "\x1b"),
            (Key::Enter, NONE, "\r"),
            (Key::Enter, ALT, "\x1b\r"),
            (Key::Tab, SHIFT, "\x1b[Z"),
            (Key::Backspace, NONE, "\x7f"),
            (Key::Backspace, CTRL, "\x08"),
            (Key::Up, NONE, "\x1b[A"),
            (Key::Up, CTRL, "\x1b[1;5A"),
            (Key::Home, SHIFT, "\x1b[1;2H"),
            (Key::PageUp, NONE, "\x1b[5~"),
            (Key::Delete, CTRL, "\x1b[3;5~"),
            (Key::F(1), NONE, "\x1bOP"),
            (Key::F(1), SHIFT, "\x1b[1;2P"),
            (Key::F(5), NONE, "\x1b[15~"),
            (Key::F(12), ALT, "\x1b[24;3~"),
            (Key::Keypad(KeypadKey::Digit(7)), NONE, "7"),
            (Key::Keypad(KeypadKey::Enter), NONE, "\r"),
        ];
        for (key, mods, want) in cases {
            assert_eq!(enc(*key, *mods, m), *want, "{key:?} {mods:?}");
        }
    }

    #[test]
    fn application_modes() {
        let m = KeyModes { app_cursor: true, app_keypad: true, kitty: 0 };
        assert_eq!(enc(Key::Up, NONE, m), "\x1bOA");
        assert_eq!(enc(Key::Up, SHIFT, m), "\x1b[1;2A");
        assert_eq!(enc(Key::Keypad(KeypadKey::Digit(1)), NONE, m), "\x1bOq");
        assert_eq!(enc(Key::Keypad(KeypadKey::Enter), NONE, m), "\x1bOM");
    }

    #[test]
    fn kitty_disambiguate() {
        let m = KeyModes { kitty: 1, ..KeyModes::default() };
        let cases: &[(Key, Mods, &str)] = &[
            (Key::Char('a'), NONE, "a"),
            (Key::Char('A'), SHIFT, "A"),
            (Key::Char('a'), CTRL, "\x1b[97;5u"),
            (Key::Char('a'), ALT, "\x1b[97;3u"),
            (Key::Enter, NONE, "\r"),
            (Key::Enter, SHIFT, "\x1b[13;2u"),
            (Key::Tab, SHIFT, "\x1b[9;2u"),
            (Key::Backspace, NONE, "\x7f"),
            (Key::Escape, NONE, "\x1b[27u"),
            (Key::Up, NONE, "\x1b[A"),
            (Key::Keypad(KeypadKey::Digit(0)), NONE, "\x1b[57399;1u"),
        ];
        for (key, mods, want) in cases {
            assert_eq!(enc(*key, *mods, m), *want, "{key:?} {mods:?}");
        }
        let all = KeyModes { kitty: 1 | 8, ..KeyModes::default() };
        assert_eq!(enc(Key::Enter, NONE, all), "\x1b[13u");
    }

    #[test]
    fn mouse() {
        use MouseAction::{Motion, Press, Release};
        use MouseButton::{Left, WheelUp};
        let none = MouseButton::None;
        let sgr = |mode, b, a, m| encode_mouse(mode, true, b, a, 4, 9, m).map(|v| String::from_utf8(v).unwrap());
        assert_eq!(sgr(MouseMode::Click, Left, Press, NONE).as_deref(), Some("\x1b[<0;5;10M"));
        assert_eq!(sgr(MouseMode::Click, Left, Release, NONE).as_deref(), Some("\x1b[<0;5;10m"));
        assert_eq!(sgr(MouseMode::Click, Left, Motion, NONE), None);
        assert_eq!(sgr(MouseMode::Drag, Left, Motion, NONE).as_deref(), Some("\x1b[<32;5;10M"));
        assert_eq!(sgr(MouseMode::Drag, none, Motion, NONE), None);
        assert_eq!(sgr(MouseMode::Motion, none, Motion, NONE).as_deref(), Some("\x1b[<35;5;10M"));
        assert_eq!(sgr(MouseMode::Click, WheelUp, Press, CTRL).as_deref(), Some("\x1b[<80;5;10M"));
        assert_eq!(sgr(MouseMode::Off, Left, Press, NONE), None);
        assert_eq!(sgr(MouseMode::X10, Left, Release, NONE), None);
        let legacy = encode_mouse(MouseMode::Click, false, Left, Release, 0, 0, NONE).unwrap();
        assert_eq!(legacy, b"\x1b[M#!!");
    }

    #[test]
    fn paste() {
        assert_eq!(encode_paste("a\nb\r\nc", false), b"a\rb\rc");
        assert_eq!(encode_paste("x\x1b[201~y", true), b"\x1b[200~x[201~y\x1b[201~");
    }
}
