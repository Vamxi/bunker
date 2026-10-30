//! Rows, the screen grid, and the scrollback.

use std::collections::VecDeque;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

use crate::graphics::Placement;

use crate::cell::{Cell, CellFlags, StyleId};

static NEXT_ROW_ID: AtomicU64 = AtomicU64::new(1);

/// A fresh row identity. Every change to a row gives it a new one, so a
/// renderer can cache what it drew for a row by id and reuse it when the
/// row only moved.
pub fn next_row_id() -> u64 {
    NEXT_ROW_ID.fetch_add(1, Ordering::Relaxed)
}

/// Per-row flags.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub struct RowFlags(pub u8);

impl RowFlags {
    /// The row's text continues on the next row (a soft wrap).
    pub const WRAPPED: Self = Self(1);
    /// A shell prompt starts on this row (OSC 133;A).
    pub const PROMPT: Self = Self(1 << 1);
    /// A command's output starts on this row (OSC 133;C).
    pub const OUTPUT: Self = Self(1 << 2);
    /// The command whose output starts here failed (OSC 133;D with a
    /// non-zero status).
    pub const FAILED: Self = Self(1 << 3);

    /// Reports whether every bit of `other` is set.
    pub fn contains(self, other: Self) -> bool {
        self.0 & other.0 == other.0
    }

    /// Sets or clears the bits of `other`.
    pub fn set(&mut self, other: Self, on: bool) {
        if on { self.0 |= other.0 } else { self.0 &= !other.0 }
    }
}

/// One row of cells. Screen rows are always as wide as the screen;
/// scrollback rows are trimmed to their last used cell.
#[derive(Clone, Debug, Default)]
pub struct Row {
    pub(crate) cells: Vec<Cell>,
    /// The rest of each multi-codepoint grapheme, by column.
    pub(crate) graphemes: Vec<(u16, String)>,
    /// Soft-wrap and shell-integration flags.
    pub flags: RowFlags,
    /// Images whose top-left cell is on this row.
    pub(crate) images: Vec<Arc<Placement>>,
    id: u64,
    /// Changed since its identity was last renewed; see [`Row::commit`].
    dirty: bool,
}

impl Row {
    /// A blank row `cols` wide.
    pub fn new(cols: usize, blank: Cell) -> Self {
        Self {
            cells: vec![blank; cols],
            graphemes: Vec::new(),
            flags: RowFlags::default(),
            images: Vec::new(),
            id: next_row_id(),
            dirty: false,
        }
    }

    /// The row's identity; it changes whenever the row does.
    pub fn id(&self) -> u64 {
        self.id
    }

    /// Marks the row changed. Its identity is renewed by [`Row::commit`],
    /// once per batch of changes rather than once per character.
    pub(crate) fn touch(&mut self) {
        self.dirty = true;
    }

    /// Gives a changed row a new identity.
    pub(crate) fn commit(&mut self) {
        if self.dirty {
            self.id = next_row_id();
            self.dirty = false;
        }
    }

    /// The row's cells.
    pub fn cells(&self) -> &[Cell] {
        &self.cells
    }

    /// Images whose top-left cell is on this row.
    pub fn images(&self) -> &[Arc<Placement>] {
        &self.images
    }

    /// The cells after the first codepoint of the grapheme at `col`, if any.
    pub fn grapheme_tail(&self, col: usize) -> Option<&str> {
        self.graphemes.iter().find(|(c, _)| usize::from(*c) == col).map(|(_, s)| s.as_str())
    }

    /// Reports whether the row's text continues on the next row.
    pub fn wrapped(&self) -> bool {
        self.flags.contains(RowFlags::WRAPPED)
    }

    /// Writes the text of the cell at `col` (all of its grapheme) to `out`.
    pub fn push_cell_text(&self, col: usize, out: &mut String) {
        let cell = self.cells[col];
        if cell.is_spacer() {
            return;
        }
        out.push(cell.char());
        if cell.flags.contains(CellFlags::GRAPHEME)
            && let Some(tail) = self.grapheme_tail(col)
        {
            out.push_str(tail);
        }
    }

    /// The number of cells up to and including the last one that is not
    /// blank and unstyled.
    pub fn used_len(&self) -> usize {
        // Most rows end in a run of empty cells (a scrolling log line uses
        // a few of a hundred), so the scan skips eight at a time: a default
        // cell is all zero bits, and OR-ing a chunk's fields compiles to a
        // few vector instructions instead of a branch per cell.
        // SAFETY: Cell is repr(C) and made of integers with no padding, so
        // every byte of the slice is initialised.
        let bytes = unsafe {
            std::slice::from_raw_parts(self.cells.as_ptr().cast::<u8>(), std::mem::size_of_val(&self.cells[..]))
        };
        let mut end = self.cells.len();
        while end >= 32 {
            let chunk = &bytes[(end - 32) * size_of::<Cell>()..end * size_of::<Cell>()];
            if chunk.iter().fold(0, |acc, b| acc | b) != 0 {
                break;
            }
            end -= 32;
        }
        self.cells[..end].iter().rposition(|c| !c.is_default()).map_or(0, |i| i + 1)
    }

    /// Fills the row with `blank` and clears its flags.
    pub(crate) fn reset(&mut self, cols: usize, blank: Cell) {
        self.cells.clear();
        self.cells.resize(cols, blank);
        self.graphemes.clear();
        self.images.clear();
        self.flags = RowFlags::default();
        self.touch();
    }

    /// Drops grapheme tails in `start..end`.
    pub(crate) fn clear_graphemes(&mut self, start: usize, end: usize) {
        if !self.graphemes.is_empty() {
            self.graphemes.retain(|(c, _)| !(start..end).contains(&usize::from(*c)));
        }
    }

    /// Adds `c` to the grapheme at `col`.
    pub(crate) fn push_grapheme(&mut self, col: usize, c: char) {
        match self.graphemes.iter_mut().find(|(g, _)| usize::from(*g) == col) {
            Some((_, tail)) => tail.push(c),
            None => self.graphemes.push((col as u16, c.to_string())),
        }
    }

    /// Fills `start..end` with `blank`, also blanking the halves of wide
    /// characters cut by the range.
    pub(crate) fn erase(&mut self, start: usize, end: usize, blank: Cell) {
        let end = end.min(self.cells.len());
        if start >= end {
            return;
        }
        self.fix_wide_edges(start, end, blank);
        self.cells[start..end].fill(blank);
        self.clear_graphemes(start, end);
        if start == 0 && end == self.cells.len() {
            self.images.clear();
        }
        self.touch();
    }

    /// Blanks the other half of any wide character that `start..end`
    /// splits, before the range is overwritten.
    pub(crate) fn fix_wide_edges(&mut self, start: usize, end: usize, blank: Cell) {
        if start > 0 && self.cells[start].flags.contains(CellFlags::SPACER) {
            self.cells[start - 1] = blank;
            self.clear_graphemes(start - 1, start);
        }
        if end < self.cells.len() && self.cells[end - 1].is_wide() {
            self.cells[end] = blank;
        }
    }

    /// Copies the used part of `src` into this row, reusing this row's
    /// allocation, and takes `src`'s grapheme tails.
    pub(crate) fn take_trimmed(&mut self, src: &mut Row) {
        let n = if src.wrapped() { src.cells.len() } else { src.used_len() };
        self.cells.clear();
        self.cells.extend_from_slice(&src.cells[..n]);
        self.graphemes.clear();
        std::mem::swap(&mut self.graphemes, &mut src.graphemes);
        self.images.clear();
        std::mem::swap(&mut self.images, &mut src.images);
        self.flags = src.flags;
        self.id = if src.dirty { next_row_id() } else { src.id };
        self.dirty = false;
    }

    /// Rewrites style ids after the style table was compacted.
    pub(crate) fn remap_styles(&mut self, map: &[StyleId]) {
        for c in &mut self.cells {
            c.style = map.get(usize::from(c.style.0)).copied().unwrap_or_default();
        }
    }
}

/// The visible screen: a fixed number of rows of a fixed width.
#[derive(Clone, Debug)]
pub struct Grid {
    pub(crate) rows: VecDeque<Row>,
    pub(crate) cols: usize,
}

impl Grid {
    /// A blank grid.
    pub fn new(cols: usize, lines: usize) -> Self {
        Self { rows: (0..lines).map(|_| Row::new(cols, Cell::default())).collect(), cols }
    }

    /// The row at `y`.
    pub fn row(&self, y: usize) -> &Row {
        &self.rows[y]
    }

    /// The row at `y`, marked as changed.
    pub(crate) fn row_mut(&mut self, y: usize) -> &mut Row {
        let row = &mut self.rows[y];
        row.touch();
        row
    }

    /// Renews the identity of every changed row.
    pub(crate) fn commit(&mut self) {
        for row in &mut self.rows {
            row.commit();
        }
    }

    /// Resizes without reflow: rows are cut or padded at the right and
    /// the bottom.
    pub(crate) fn resize_simple(&mut self, cols: usize, lines: usize) {
        for row in &mut self.rows {
            if row.cells.len() != cols {
                row.cells.resize(cols, Cell::default());
                if cols > 0 && row.cells[cols - 1].is_wide() {
                    row.cells[cols - 1] = Cell::default();
                }
                row.clear_graphemes(cols, usize::MAX);
                row.flags.set(RowFlags::WRAPPED, false);
                row.touch();
            }
        }
        self.rows.resize_with(lines, || Row::new(cols, Cell::default()));
        self.cols = cols;
    }
}

/// Rows that scrolled off the top, oldest first, with a line limit.
#[derive(Clone, Debug, Default)]
pub struct Scrollback {
    pub(crate) rows: VecDeque<Row>,
    pub(crate) limit: usize,
    /// Rows ever pushed, so positions stay stable as old rows are dropped.
    pub(crate) pushed: u64,
}

impl Scrollback {
    /// An empty scrollback holding up to `limit` rows.
    pub fn new(limit: usize) -> Self {
        Self { rows: VecDeque::new(), limit, pushed: 0 }
    }

    /// The number of rows held.
    pub(crate) fn len(&self) -> usize {
        self.rows.len()
    }

    /// Saves `row`'s contents, dropping the oldest row when full. The row
    /// is left to be reset.
    pub(crate) fn push(&mut self, row: &mut Row) {
        if self.limit == 0 {
            return;
        }
        let mut saved = if self.rows.len() >= self.limit { self.rows.pop_front().unwrap() } else { Row::default() };
        saved.take_trimmed(row);
        self.rows.push_back(saved);
        self.pushed += 1;
    }

    /// Drops every row.
    pub(crate) fn clear(&mut self) {
        self.rows.clear();
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    fn text_row(s: &str) -> Row {
        let mut r = Row::new(10, Cell::default());
        for (i, c) in s.chars().enumerate() {
            r.cells[i].ch = c as u32;
        }
        r
    }

    #[test]
    fn used_len_matches_a_cell_by_cell_scan() {
        // Any one field set makes a cell count, wherever it sits.
        let marks = [
            Cell { ch: 'x' as u32, ..Cell::default() },
            Cell { style: StyleId(3), ..Cell::default() },
            Cell { flags: CellFlags::SPACER, ..Cell::default() },
        ];
        for len in [0, 1, 7, 8, 9, 16, 17, 100, 253] {
            for at in (0..len).chain([usize::MAX]) {
                for mark in marks {
                    let mut row = Row::new(len, Cell::default());
                    if at < len {
                        row.cells[at] = mark;
                    }
                    let naive = row.cells.iter().rposition(|c| !c.is_default()).map_or(0, |i| i + 1);
                    assert_eq!(row.used_len(), naive, "len {len}, mark at {at}: {mark:?}");
                }
            }
        }
    }

    #[test]
    fn take_trimmed_drops_blank_tail() {
        let mut dst = Row::default();
        dst.take_trimmed(&mut text_row("ab"));
        assert_eq!(dst.cells.len(), 2);
    }

    #[test]
    fn wrapped_rows_keep_full_width() {
        let mut src = text_row("ab");
        src.flags.set(RowFlags::WRAPPED, true);
        let mut dst = Row::default();
        dst.take_trimmed(&mut src);
        assert_eq!(dst.cells.len(), 10);
    }

    #[test]
    fn scrollback_limit_drops_oldest() {
        let mut sb = Scrollback::new(2);
        for s in ["a", "b", "c"] {
            sb.push(&mut text_row(s));
        }
        assert_eq!(sb.len(), 2);
        assert_eq!(sb.rows[0].cells[0].ch, 'b' as u32);
        assert_eq!(sb.pushed, 3);
    }

    #[test]
    fn erase_splits_wide_characters() {
        let mut r = Row::new(4, Cell::default());
        r.cells[0] = Cell { ch: '漢' as u32, style: StyleId(0), flags: CellFlags::WIDE };
        r.cells[1] = Cell { ch: 0, style: StyleId(0), flags: CellFlags::SPACER };
        r.erase(1, 2, Cell::default());
        assert!(r.cells[0].is_default());
    }

    #[test]
    fn touch_changes_id() {
        let mut g = Grid::new(4, 2);
        let before = g.row(0).id();
        g.row_mut(0);
        assert_eq!(g.row(0).id(), before, "renewed only on commit");
        g.commit();
        assert_ne!(g.row(0).id(), before);
    }
}
