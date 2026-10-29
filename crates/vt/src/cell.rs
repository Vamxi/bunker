//! Cells, colours, and the table of styles cells refer to.

use std::collections::HashMap;
use std::hash::{BuildHasherDefault, Hasher};

/// A fast hasher for small keys (FxHash, as in rustc). Style keys are not
/// attacker-controlled in a way that matters: a flood of styles only
/// triggers compaction.
#[derive(Default)]
pub(crate) struct FxHasher(u64);

impl Hasher for FxHasher {
    fn write(&mut self, bytes: &[u8]) {
        for chunk in bytes.chunks(8) {
            let mut b = [0u8; 8];
            b[..chunk.len()].copy_from_slice(chunk);
            self.add(u64::from_le_bytes(b));
        }
    }
    fn write_u8(&mut self, v: u8) {
        self.add(u64::from(v));
    }
    fn write_u16(&mut self, v: u16) {
        self.add(u64::from(v));
    }
    fn write_u32(&mut self, v: u32) {
        self.add(u64::from(v));
    }
    fn write_u64(&mut self, v: u64) {
        self.add(v);
    }
    fn write_usize(&mut self, v: usize) {
        self.add(v as u64);
    }
    fn finish(&self) -> u64 {
        self.0
    }
}

impl FxHasher {
    #[inline]
    fn add(&mut self, v: u64) {
        self.0 = (self.0.rotate_left(5) ^ v).wrapping_mul(0x51_7c_c1_b7_27_22_0a_95);
    }
}

/// A HashMap with [`FxHasher`].
pub(crate) type FxHashMap<K, V> = HashMap<K, V, BuildHasherDefault<FxHasher>>;

/// A colour as the program set it. Theme colours are resolved by the
/// frontend, so a theme change repaints existing text.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub enum Color {
    /// The theme's foreground or background, depending on where it is used.
    #[default]
    Default,
    /// One of the 256 palette entries.
    Indexed(u8),
    /// A 24-bit colour.
    Rgb(u8, u8, u8),
}

/// Text attributes (SGR) other than colours and the underline style.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Attrs(pub u16);

impl Attrs {
    /// SGR 1.
    pub const BOLD: Self = Self(1);
    /// SGR 2.
    pub const DIM: Self = Self(1 << 1);
    /// SGR 3.
    pub const ITALIC: Self = Self(1 << 2);
    /// SGR 5 and 6.
    pub const BLINK: Self = Self(1 << 3);
    /// SGR 7.
    pub const REVERSE: Self = Self(1 << 4);
    /// SGR 8.
    pub const HIDDEN: Self = Self(1 << 5);
    /// SGR 9.
    pub const STRIKE: Self = Self(1 << 6);
    /// SGR 53.
    pub const OVERLINE: Self = Self(1 << 7);

    /// Reports whether every bit of `other` is set.
    pub fn contains(self, other: Self) -> bool {
        self.0 & other.0 == other.0
    }

    /// Sets or clears the bits of `other`.
    pub fn set(&mut self, other: Self, on: bool) {
        if on { self.0 |= other.0 } else { self.0 &= !other.0 }
    }
}

/// The underline style (SGR 4, 4:N, and 21).
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub enum Underline {
    /// No underline.
    #[default]
    None,
    /// SGR 4 or 4:1.
    Single,
    /// SGR 21 or 4:2.
    Double,
    /// SGR 4:3.
    Curly,
    /// SGR 4:4.
    Dotted,
    /// SGR 4:5.
    Dashed,
}

/// Everything about how a cell is drawn apart from its character.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct Style {
    /// Foreground colour.
    pub fg: Color,
    /// Background colour.
    pub bg: Color,
    /// Underline colour (SGR 58); `Default` follows the foreground.
    pub underline_color: Color,
    /// SGR attribute bits.
    pub attrs: Attrs,
    /// Underline style.
    pub underline: Underline,
    /// OSC 8 hyperlink: an index into the terminal's link table, 0 for none.
    pub link: u32,
}

/// Index of a style in the terminal's [`StyleTable`]; 0 is the default style.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct StyleId(pub u16);

/// Per-cell flags.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, Hash)]
pub struct CellFlags(pub u16);

impl CellFlags {
    /// The cell holds the first half of a double-width character.
    pub const WIDE: Self = Self(1);
    /// The cell is the second half of the double-width character before it.
    pub const SPACER: Self = Self(1 << 1);
    /// The character continues in the row's grapheme table.
    pub const GRAPHEME: Self = Self(1 << 2);
    /// The cell was left empty because a wide character did not fit at the
    /// end of the row and wrapped to the next.
    pub const WRAP_SPACER: Self = Self(1 << 3);

    /// Reports whether every bit of `other` is set.
    pub fn contains(self, other: Self) -> bool {
        self.0 & other.0 == other.0
    }
}

/// One cell of the grid: 8 bytes, so a row is a flat array.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
#[repr(C)]
pub struct Cell {
    /// The character, or 0 for an empty cell.
    pub ch: u32,
    /// How the cell is drawn.
    pub style: StyleId,
    /// Wide-character and grapheme flags.
    pub flags: CellFlags,
}

impl Cell {
    /// An empty cell drawn with `style` (its background shows).
    pub const fn blank(style: StyleId) -> Self {
        Self { ch: 0, style, flags: CellFlags(0) }
    }

    /// The cell's first character; empty cells read as a space.
    pub fn char(&self) -> char {
        if self.ch == 0 { ' ' } else { char::from_u32(self.ch).unwrap_or(char::REPLACEMENT_CHARACTER) }
    }

    /// Reports whether the cell is the second half of a wide character.
    pub fn is_spacer(&self) -> bool {
        self.flags.0 & (CellFlags::SPACER.0 | CellFlags::WRAP_SPACER.0) != 0
    }

    /// Reports whether the cell holds a double-width character.
    pub fn is_wide(&self) -> bool {
        self.flags.contains(CellFlags::WIDE)
    }

    /// Reports whether the cell is empty and unstyled.
    pub fn is_default(&self) -> bool {
        *self == Self::default()
    }
}

/// The capacity of a [`StyleTable`]; the terminal compacts it when full.
pub const MAX_STYLES: usize = u16::MAX as usize;

/// Deduplicated styles, so a cell stores a 16-bit index instead of the
/// whole style.
#[derive(Debug)]
pub struct StyleTable {
    styles: Vec<Style>,
    index: FxHashMap<Style, StyleId>,
}

impl Default for StyleTable {
    fn default() -> Self {
        let mut index = FxHashMap::default();
        index.insert(Style::default(), StyleId(0));
        Self { styles: vec![Style::default()], index }
    }
}

impl StyleTable {
    /// The style a cell refers to.
    pub fn get(&self, id: StyleId) -> &Style {
        self.styles.get(id.0 as usize).unwrap_or(&self.styles[0])
    }

    /// Returns the id of `style`, adding it if needed; `None` when full.
    pub fn intern(&mut self, style: Style) -> Option<StyleId> {
        if let Some(&id) = self.index.get(&style) {
            return Some(id);
        }
        if self.styles.len() >= MAX_STYLES {
            return None;
        }
        let id = StyleId(self.styles.len() as u16);
        self.styles.push(style);
        self.index.insert(style, id);
        Some(id)
    }

    /// The number of styles, including the default.
    pub(crate) fn len(&self) -> usize {
        self.styles.len()
    }

    /// Keeps only the styles marked in `used` and returns the old-to-new id
    /// mapping, so the caller can rewrite its cells.
    pub fn compact(&mut self, used: &[bool]) -> Vec<StyleId> {
        let mut map = vec![StyleId(0); self.styles.len()];
        let old = std::mem::take(&mut self.styles);
        self.index.clear();
        for (i, style) in old.into_iter().enumerate() {
            if i == 0 || used.get(i).copied().unwrap_or(false) {
                let id = StyleId(self.styles.len() as u16);
                self.styles.push(style);
                self.index.insert(style, id);
                map[i] = id;
            }
        }
        map
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn cell_is_eight_bytes() {
        assert_eq!(std::mem::size_of::<Cell>(), 8);
    }

    #[test]
    fn intern_deduplicates() {
        let mut t = StyleTable::default();
        let red = Style { fg: Color::Indexed(1), ..Style::default() };
        let a = t.intern(red).unwrap();
        let b = t.intern(red).unwrap();
        assert_eq!(a, b);
        assert_ne!(a, StyleId(0));
        assert_eq!(t.intern(Style::default()), Some(StyleId(0)));
    }

    #[test]
    fn compact_keeps_used_styles() {
        let mut t = StyleTable::default();
        let ids: Vec<_> =
            (0..4u8).map(|i| t.intern(Style { fg: Color::Indexed(i), ..Style::default() }).unwrap()).collect();
        let mut used = vec![false; t.len()];
        used[ids[2].0 as usize] = true;
        let map = t.compact(&used);
        assert_eq!(t.len(), 2);
        assert_eq!(t.get(map[ids[2].0 as usize]).fg, Color::Indexed(2));
    }
}
