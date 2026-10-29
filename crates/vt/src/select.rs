//! Reading text out of the terminal: selections, words, and lines.

use crate::term::Terminal;

/// A cell position. Rows are absolute (see [`Terminal::view_top`]), so a
/// position stays on the same text while output scrolls.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq, PartialOrd, Ord, Hash)]
pub struct Point {
    /// Absolute row.
    pub row: u64,
    /// Column.
    pub col: usize,
}

impl Point {
    /// A position.
    pub const fn new(row: u64, col: usize) -> Self {
        Self { row, col }
    }
}

/// Characters that are part of a word for double-click selection, besides
/// letters and digits, so paths and URLs select whole.
const WORD_CHARS: &str = "_-./~:@%+#?&=$";

fn is_word(c: char) -> bool {
    c.is_alphanumeric() || WORD_CHARS.contains(c)
}

impl Terminal {
    /// The text from `start` to `end` inclusive, reading across rows.
    /// Soft-wrapped rows join without a newline; trailing blanks of other
    /// rows are dropped.
    pub fn text(&self, start: Point, end: Point) -> String {
        let (start, end) = if start <= end { (start, end) } else { (end, start) };
        let mut out = String::new();
        for abs in start.row..=end.row {
            let Some(row) = self.row_at(abs) else { continue };
            let cells = row.cells();
            let from = if abs == start.row { start.col } else { 0 };
            let to = if abs == end.row { (end.col + 1).min(cells.len()) } else { cells.len() };
            let line_start = out.len();
            for col in from..to {
                row.push_cell_text(col, &mut out);
            }
            let last = abs == end.row;
            if !row.wrapped() || (last && to < cells.len()) {
                let trimmed = out[line_start..].trim_end_matches(' ').len();
                out.truncate(line_start + trimmed);
            }
            if !last && !row.wrapped() {
                out.push('\n');
            }
        }
        out
    }

    /// The text of the rectangle with corners `start` and `end`.
    pub fn block_text(&self, start: Point, end: Point) -> String {
        let (r0, r1) = (start.row.min(end.row), start.row.max(end.row));
        let (c0, c1) = (start.col.min(end.col), start.col.max(end.col));
        let mut out = String::new();
        for abs in r0..=r1 {
            let Some(row) = self.row_at(abs) else { continue };
            let line_start = out.len();
            for col in c0..=c1.min(row.cells().len().saturating_sub(1)) {
                row.push_cell_text(col, &mut out);
            }
            let trimmed = out[line_start..].trim_end_matches(' ').len();
            out.truncate(line_start + trimmed);
            if abs != r1 {
                out.push('\n');
            }
        }
        out
    }

    fn char_at(&self, p: Point) -> Option<char> {
        let row = self.row_at(p.row)?;
        let mut col = p.col.min(row.cells().len().checked_sub(1)?);
        if row.cells()[col].is_spacer() && col > 0 {
            col -= 1;
        }
        Some(row.cells()[col].char())
    }

    fn row_len(&self, abs: u64) -> usize {
        self.row_at(abs).map_or(0, |r| r.cells().len())
    }

    fn wraps(&self, abs: u64) -> bool {
        self.row_at(abs).is_some_and(|r| r.wrapped())
    }

    /// The word around `p`, following soft wraps.
    pub fn word_bounds(&self, p: Point) -> (Point, Point) {
        let Some(c) = self.char_at(p) else { return (p, p) };
        let word = is_word(c);
        let same = |q: Point| self.char_at(q).is_some_and(|c| if word { is_word(c) } else { c == ' ' });
        if !word && c != ' ' {
            return (p, p);
        }
        let mut start = p;
        loop {
            let prev = if start.col > 0 {
                Point::new(start.row, start.col - 1)
            } else if start.row > self.first_row() && self.wraps(start.row - 1) {
                Point::new(start.row - 1, self.row_len(start.row - 1) - 1)
            } else {
                break;
            };
            if !same(prev) {
                break;
            }
            start = prev;
        }
        let mut end = p;
        loop {
            let next = if end.col + 1 < self.row_len(end.row) {
                Point::new(end.row, end.col + 1)
            } else if self.wraps(end.row) {
                Point::new(end.row + 1, 0)
            } else {
                break;
            };
            if !same(next) {
                break;
            }
            end = next;
        }
        (start, end)
    }

    /// The whole logical line around `p`, across soft wraps.
    pub fn line_bounds(&self, p: Point) -> (Point, Point) {
        let mut first = p.row;
        while first > self.first_row() && self.wraps(first - 1) {
            first -= 1;
        }
        let mut last = p.row;
        while self.wraps(last) {
            last += 1;
        }
        (Point::new(first, 0), Point::new(last, self.row_len(last).saturating_sub(1)))
    }
}
