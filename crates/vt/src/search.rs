//! Finding text in the scrollback and on the screen. A match may run
//! across soft-wrapped rows; it never crosses a hard line break.

use crate::select::Point;
use crate::term::Terminal;

/// A logical line as text, with the cell each byte of the text came from.
struct LineText {
    text: String,
    cells: Vec<Point>,
}

impl Terminal {
    /// The logical line starting at absolute row `first`, and the row after
    /// it ends.
    fn line_text(&self, first: u64, lower: bool) -> (LineText, u64) {
        let mut out = LineText { text: String::new(), cells: Vec::new() };
        let mut abs = first;
        let mut cell = String::new();
        loop {
            let Some(row) = self.row_at(abs) else { return (out, abs + 1) };
            for col in 0..row.cells().len() {
                if row.cells()[col].is_spacer() {
                    continue;
                }
                cell.clear();
                row.push_cell_text(col, &mut cell);
                if lower {
                    cell = cell.to_lowercase();
                }
                for _ in 0..cell.len() {
                    out.cells.push(Point::new(abs, col));
                }
                out.text.push_str(&cell);
            }
            if !row.wrapped() {
                return (out, abs + 1);
            }
            abs += 1;
        }
    }

    /// The first row of the logical line containing `abs`.
    fn line_start(&self, mut abs: u64) -> u64 {
        while abs > self.first_row() && self.row_at(abs - 1).is_some_and(|r| r.wrapped()) {
            abs -= 1;
        }
        abs
    }

    /// The last absolute row held.
    fn last_row(&self) -> u64 {
        self.view_top() + self.display_offset() as u64 + self.lines() as u64 - 1
    }

    /// Every match of `query` in a line, as (start, end inclusive).
    fn matches_in(line: &LineText, query: &str) -> Vec<(Point, Point)> {
        if query.is_empty() {
            return Vec::new();
        }
        line.text.match_indices(query).map(|(i, m)| (line.cells[i], line.cells[i + m.len() - 1])).collect()
    }

    /// Finds `query` searching from `from`: upwards (towards older output)
    /// for the first match starting before `from`, or downwards for the
    /// first starting after it, wrapping around once. Matching ignores case
    /// unless the query has a capital letter.
    pub fn find(&self, query: &str, from: Point, upwards: bool) -> Option<(Point, Point)> {
        if query.is_empty() {
            return None;
        }
        let lower = !query.chars().any(char::is_uppercase);
        let query = if lower { query.to_lowercase() } else { query.to_string() };
        let (first, last) = (self.first_row(), self.last_row());
        let start_line = self.line_start(from.row.clamp(first, last));
        let lines = last - first + 1;
        let mut line = start_line;
        // One pass over every line, plus the starting line again for
        // matches on the other side of `from`.
        for step in 0..=lines {
            let (text, next) = self.line_text(line, lower);
            let found = Self::matches_in(&text, &query);
            let pick = if upwards {
                found.into_iter().rev().find(|(s, _)| step > 0 || *s < from)
            } else {
                found.into_iter().find(|(s, _)| step > 0 || *s > from)
            };
            if pick.is_some() {
                return pick;
            }
            line = if upwards {
                if line <= first { self.line_start(last) } else { self.line_start(line - 1) }
            } else if next > last {
                first
            } else {
                next
            };
        }
        None
    }

    /// Every match of `query` that touches absolute rows `top..=bottom`.
    pub fn find_all(&self, query: &str, top: u64, bottom: u64) -> Vec<(Point, Point)> {
        if query.is_empty() {
            return Vec::new();
        }
        let lower = !query.chars().any(char::is_uppercase);
        let query = if lower { query.to_lowercase() } else { query.to_string() };
        let mut out = Vec::new();
        let mut line = self.line_start(top.max(self.first_row()));
        while line <= bottom.min(self.last_row()) {
            let (text, next) = self.line_text(line, lower);
            out.extend(Self::matches_in(&text, &query));
            line = next;
        }
        out
    }

    /// The number of matches of `query` held, for "n of m" counts.
    pub fn count_matches(&self, query: &str) -> usize {
        self.find_all(query, self.first_row(), self.last_row()).len()
    }
}

#[cfg(test)]
mod tests {
    use crate::select::Point;
    use crate::term::Terminal;

    fn term(text: &str) -> Terminal {
        let mut t = Terminal::new(10, 3, 100);
        t.advance(text.as_bytes());
        t
    }

    #[test]
    fn finds_upwards_and_wraps() {
        let t = term("foo one\r\nbar\r\nfoo two\r\nbaz\r\nfoo three");
        let end = Point::new(t.view_top() + 3, 0);
        let (a, _) = t.find("foo", end, true).unwrap();
        assert_eq!(t.text(a, Point::new(a.row, 8)), "foo three");
        let (b, _) = t.find("foo", a, true).unwrap();
        assert_eq!(t.text(b, Point::new(b.row, 6)), "foo two");
        let (c, _) = t.find("foo", b, true).unwrap();
        let (d, _) = t.find("foo", c, true).unwrap();
        assert_eq!(d, a, "searching past the oldest match wraps to the newest");
    }

    #[test]
    fn finds_downwards() {
        let t = term("foo one\r\nfoo two");
        let first = Point::new(t.first_row(), 0);
        let (a, _) = t.find("foo", first, false).unwrap();
        assert!(a > first);
    }

    #[test]
    fn smart_case() {
        let t = term("Error here\r\nerror there");
        let from = Point::new(t.view_top() + 2, 0);
        assert_eq!(t.count_matches("error"), 2);
        assert_eq!(t.count_matches("Error"), 1);
        assert!(t.find("ERROR", from, true).is_none());
    }

    #[test]
    fn across_soft_wraps() {
        let t = term("0123456789abcdef");
        let hits = t.find_all("89ab", t.first_row(), t.view_top() + 2);
        assert_eq!(hits.len(), 1);
        let (s, e) = hits[0];
        assert_eq!((s.col, e.col), (8, 1));
        assert_eq!(e.row, s.row + 1);
    }

    #[test]
    fn wide_characters_map_to_their_cells() {
        let t = term("漢字 ok");
        let hits = t.find_all("ok", t.view_top(), t.view_top());
        assert_eq!(hits[0].0.col, 5);
    }
}
