//! The palette: theme colours plus the changes programs make with OSC 4,
//! 10, 11, and 12.

use crate::cell::Color;

/// An sRGB colour.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Rgb {
    /// Red.
    pub r: u8,
    /// Green.
    pub g: u8,
    /// Blue.
    pub b: u8,
}

impl Rgb {
    /// Builds a colour from its components.
    pub const fn new(r: u8, g: u8, b: u8) -> Self {
        Self { r, g, b }
    }

    /// Parses `#rgb`, `#rrggbb`, or X11 `rgb:r/g/b` with 1-4 hex digits per
    /// component.
    pub fn parse(s: &str) -> Option<Self> {
        let s = s.trim();
        if let Some(hex) = s.strip_prefix('#') {
            let d = match hex.len() {
                3 | 6 | 9 | 12 => hex.len() / 3,
                _ => return None,
            };
            let part = |i: usize| scale_hex(&hex[i * d..(i + 1) * d]);
            return Some(Self::new(part(0)?, part(1)?, part(2)?));
        }
        let rest = s.strip_prefix("rgb:")?;
        let mut it = rest.split('/');
        let (r, g, b) = (it.next()?, it.next()?, it.next()?);
        if it.next().is_some() {
            return None;
        }
        Some(Self::new(scale_hex(r)?, scale_hex(g)?, scale_hex(b)?))
    }

    /// The X11 form used in OSC replies: `rgb:rrrr/gggg/bbbb`.
    pub fn x11(&self) -> String {
        format!("rgb:{0:02x}{0:02x}/{1:02x}{1:02x}/{2:02x}{2:02x}", self.r, self.g, self.b)
    }

    /// Mixes towards `other` by `t` (0 keeps self, 1 gives other).
    pub fn mix(self, other: Self, t: f32) -> Self {
        let f = |a: u8, b: u8| (f32::from(a) + (f32::from(b) - f32::from(a)) * t).round() as u8;
        Self::new(f(self.r, other.r), f(self.g, other.g), f(self.b, other.b))
    }

    /// Reports whether the colour is dark (ITU-R BT.601 luminance).
    pub fn is_dark(&self) -> bool {
        u32::from(self.r) * 299 + u32::from(self.g) * 587 + u32::from(self.b) * 114 < 128_000
    }
}

/// Scales 1-4 hex digits to 8 bits, as XParseColor does.
fn scale_hex(s: &str) -> Option<u8> {
    if s.is_empty() || s.len() > 4 {
        return None;
    }
    let v = u32::from_str_radix(s, 16).ok()?;
    let max = (1u32 << (4 * s.len())) - 1;
    Some(((v * 255 + max / 2) / max) as u8)
}

/// The xterm colour for palette entries 16-255.
pub fn xterm_color(i: u8) -> Rgb {
    match i {
        16..=231 => {
            let i = i - 16;
            let level = |v: u8| if v == 0 { 0 } else { 55 + v * 40 };
            Rgb::new(level(i / 36), level((i / 6) % 6), level(i % 6))
        }
        232..=255 => {
            let v = 8 + (i - 232) * 10;
            Rgb::new(v, v, v)
        }
        _ => Rgb::default(),
    }
}

/// Theme colours: what the frontend gives the terminal.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Theme {
    /// Default foreground.
    pub fg: Rgb,
    /// Default background.
    pub bg: Rgb,
    /// Cursor colour; `None` draws the cursor in the foreground colour.
    pub cursor: Option<Rgb>,
    /// The 16 ANSI colours.
    pub ansi: [Rgb; 16],
}

impl Default for Theme {
    fn default() -> Self {
        // GNOME's dark palette, as Ptyxis ships it.
        let hex = [
            0x241f31, 0xc01c28, 0x2ec27e, 0xf5c211, 0x1e78e4, 0x9841bb, 0x0ab9dc, 0xc0bfbc, 0x5e5c64, 0xed333b,
            0x57e389, 0xf8e45c, 0x51a1ff, 0xc061cb, 0x4fd2fd, 0xf6f5f4,
        ];
        let ansi = hex.map(|h: u32| Rgb::new((h >> 16) as u8, (h >> 8) as u8, h as u8));
        Self { fg: Rgb::new(0xff, 0xff, 0xff), bg: Rgb::new(0x1e, 0x1e, 0x1e), cursor: None, ansi }
    }
}

/// The effective colours: the theme with program overrides applied.
#[derive(Clone, Debug)]
pub struct Palette {
    theme: Theme,
    fg_override: Option<Rgb>,
    bg_override: Option<Rgb>,
    cursor_override: Option<Rgb>,
    overrides: Box<[Option<Rgb>; 256]>,
    table: Box<[Rgb; 256]>,
    /// Changes whenever any colour does, so cached drawing can be dropped.
    generation: u64,
}

impl Default for Palette {
    fn default() -> Self {
        let mut p = Self {
            theme: Theme::default(),
            fg_override: None,
            bg_override: None,
            cursor_override: None,
            overrides: Box::new([None; 256]),
            table: Box::new([Rgb::default(); 256]),
            generation: 0,
        };
        p.rebuild();
        p
    }
}

impl Palette {
    fn rebuild(&mut self) {
        for i in 0..256 {
            let base = if i < 16 { self.theme.ansi[i] } else { xterm_color(i as u8) };
            self.table[i] = self.overrides[i].unwrap_or(base);
        }
        self.generation += 1;
    }

    /// Replaces the theme, keeping program overrides.
    pub fn set_theme(&mut self, theme: Theme) {
        if self.theme != theme {
            self.theme = theme;
            self.rebuild();
        }
    }

    /// The theme the palette is based on.
    pub fn theme(&self) -> &Theme {
        &self.theme
    }

    /// A counter that changes whenever a colour changes.
    pub fn generation(&self) -> u64 {
        self.generation
    }

    /// Default foreground.
    pub fn fg(&self) -> Rgb {
        self.fg_override.unwrap_or(self.theme.fg)
    }

    /// Default background.
    pub fn bg(&self) -> Rgb {
        self.bg_override.unwrap_or(self.theme.bg)
    }

    /// Cursor colour, if one is set.
    pub fn cursor(&self) -> Option<Rgb> {
        self.cursor_override.or(self.theme.cursor)
    }

    /// Palette entry `i`.
    pub fn indexed(&self, i: u8) -> Rgb {
        self.table[usize::from(i)]
    }

    /// Resolves a cell colour; `Default` becomes `default`.
    pub fn resolve(&self, c: Color, default: Rgb) -> Rgb {
        match c {
            Color::Default => default,
            Color::Indexed(i) => self.indexed(i),
            Color::Rgb(r, g, b) => Rgb::new(r, g, b),
        }
    }

    pub(crate) fn set_indexed(&mut self, i: u8, c: Option<Rgb>) {
        self.overrides[usize::from(i)] = c;
        self.rebuild();
    }

    pub(crate) fn reset_all_indexed(&mut self) {
        *self.overrides = [None; 256];
        self.rebuild();
    }

    pub(crate) fn set_dynamic(&mut self, which: u16, c: Option<Rgb>) {
        match which {
            10 => self.fg_override = c,
            11 => self.bg_override = c,
            12 => self.cursor_override = c,
            _ => return,
        }
        self.generation += 1;
    }

    pub(crate) fn reset(&mut self) {
        self.fg_override = None;
        self.bg_override = None;
        self.cursor_override = None;
        self.reset_all_indexed();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn parse_colors() {
        let cases = [
            ("#fff", Some(Rgb::new(255, 255, 255))),
            ("#102030", Some(Rgb::new(0x10, 0x20, 0x30))),
            ("rgb:ffff/0000/8080", Some(Rgb::new(255, 0, 0x80))),
            ("rgb:f/0/8", Some(Rgb::new(255, 0, 0x88))),
            ("rgb:1/2", None),
            ("red", None),
        ];
        for (s, want) in cases {
            assert_eq!(Rgb::parse(s), want, "{s}");
        }
    }

    #[test]
    fn x11_round_trip() {
        let c = Rgb::new(0x12, 0xab, 0xff);
        assert_eq!(c.x11(), "rgb:1212/abab/ffff");
        assert_eq!(Rgb::parse(&c.x11()), Some(c));
    }

    #[test]
    fn cube_and_grays() {
        assert_eq!(xterm_color(16), Rgb::new(0, 0, 0));
        assert_eq!(xterm_color(231), Rgb::new(255, 255, 255));
        assert_eq!(xterm_color(196), Rgb::new(255, 0, 0));
        assert_eq!(xterm_color(232), Rgb::new(8, 8, 8));
    }

    #[test]
    fn overrides_survive_theme_change() {
        let mut p = Palette::default();
        p.set_indexed(1, Some(Rgb::new(1, 2, 3)));
        p.set_theme(Theme { fg: Rgb::new(9, 9, 9), ..Theme::default() });
        assert_eq!(p.indexed(1), Rgb::new(1, 2, 3));
        assert_eq!(p.fg(), Rgb::new(9, 9, 9));
    }
}
