//! Links under the pointer: OSC 8 hyperlinks (ls --hyperlink, Claude Code,
//! gcc) and URLs printed as plain text, followed across the rows they wrap
//! onto.

use crate::cell::Cell;
use crate::select::Point;
use crate::term::Terminal;

/// How many rows above and below a link is followed when it wraps.
const MAX_ROWS: u64 = 16;

/// Schemes recognised in plain text.
const SCHEMES: [&str; 4] = ["https://", "http://", "mailto:", "file://"];

/// A link and the cells it covers.
#[derive(Clone, Debug, PartialEq, Eq)]
pub struct Link {
    /// Where it points.
    pub uri: String,
    /// The cells, one run per row: absolute row, first column, and the
    /// column after the last.
    pub runs: Vec<(u64, usize, usize)>,
}

/// Whether `c` can be part of a URL printed as text.
fn url_char(c: char) -> bool {
    !c.is_whitespace() && !"<>\"'`".contains(c)
}

impl Terminal {
    /// The link at `p`: an OSC 8 hyperlink, or a URL printed as text.
    pub fn link_at(&self, p: Point) -> Option<Link> {
        let cell = *self.row_at(p.row)?.cells().get(p.col)?;
        let id = self.style(cell.style).link;
        if id != 0 {
            let uri = self.link(id)?.to_string();
            return Some(Link { uri, runs: self.hyperlink_runs(p, id) });
        }
        self.url_at(p)
    }

    fn cell_link(&self, c: &Cell) -> u32 {
        self.style(c.style).link
    }

    /// The cells of hyperlink `id` around `p`, onto the rows above and below
    /// while it runs across a row edge.
    fn hyperlink_runs(&self, p: Point, id: u32) -> Vec<(u64, usize, usize)> {
        let run = |abs: u64, col: usize| {
            let cells = self.row_at(abs).map_or(&[][..], |r| r.cells());
            let (mut c0, mut c1) = (col, col + 1);
            while c0 > 0 && self.cell_link(&cells[c0 - 1]) == id {
                c0 -= 1;
            }
            while c1 < cells.len() && self.cell_link(&cells[c1]) == id {
                c1 += 1;
            }
            (abs, c0, c1)
        };
        let mut runs = vec![run(p.row, p.col)];
        while runs[0].1 == 0 && p.row - runs[0].0 < MAX_ROWS {
            let above = runs[0].0.checked_sub(1).and_then(|a| Some((a, self.row_at(a)?.cells())));
            match above {
                Some((a, cells)) if cells.last().is_some_and(|c| self.cell_link(c) == id) => {
                    runs.insert(0, run(a, cells.len() - 1));
                }
                _ => break,
            }
        }
        loop {
            let &(abs, _, end) = runs.last().unwrap();
            let len = self.row_at(abs).map_or(0, |r| r.cells().len());
            if end < len || abs - p.row >= MAX_ROWS {
                break;
            }
            match self.row_at(abs + 1) {
                Some(below) if below.cells().first().is_some_and(|c| self.cell_link(c) == id) => {
                    runs.push(run(abs + 1, 0));
                }
                _ => break,
            }
        }
        runs
    }

    /// Whether row `abs` runs on into the next: it was soft-wrapped, or text
    /// fills its last column and carries on in the next row's first, which
    /// is how programs that place the cursor themselves lay out a long URL.
    fn continues_below(&self, abs: u64) -> bool {
        let (Some(row), Some(next)) = (self.row_at(abs), self.row_at(abs + 1)) else { return false };
        if row.wrapped() {
            return true;
        }
        let url_cell = |c: Option<&Cell>| c.is_some_and(|c| !c.is_wide() && !c.is_spacer() && url_char(c.char()));
        url_cell(row.cells().last()) && url_cell(next.cells().first())
    }

    /// A URL printed as text that covers `p`, joining the rows it wraps
    /// across.
    fn url_at(&self, p: Point) -> Option<Link> {
        let (mut top, mut bottom) = (p.row, p.row);
        while top > 0 && p.row - top < MAX_ROWS && self.continues_below(top - 1) {
            top -= 1;
        }
        while bottom - p.row < MAX_ROWS && self.continues_below(bottom) {
            bottom += 1;
        }
        // The rows' text, and the cell each byte of it came from.
        let mut text = String::new();
        let mut cell_of: Vec<(u64, usize)> = Vec::new();
        for abs in top..=bottom {
            let row = self.row_at(abs)?;
            for col in 0..row.cells().len() {
                row.push_cell_text(col, &mut text);
                cell_of.resize(text.len(), (abs, col));
            }
        }
        let end_cell = (bottom, self.row_at(bottom)?.cells().len());
        let at = (p.row, p.col);
        let bytes = text.as_bytes();
        let mut i = 0;
        while i < bytes.len() {
            let scheme = SCHEMES
                .iter()
                .find(|s| bytes[i..].len() >= s.len() && bytes[i..i + s.len()].eq_ignore_ascii_case(s.as_bytes()));
            // A scheme starts with an ASCII letter, so where one matches, i
            // is on a character boundary and the text before it can be
            // read; i itself steps through bytes, inside characters too.
            let Some(scheme) = scheme.filter(|_| !text[..i].chars().next_back().is_some_and(char::is_alphanumeric))
            else {
                i += 1;
                continue;
            };
            let after = i + scheme.len();
            let end = text[after..].find(|c: char| !url_char(c)).map_or(text.len(), |n| after + n);
            let end = trim_url_end(&text, i, end);
            if end > after {
                let first = cell_of[i];
                let last = cell_of.get(end).copied().unwrap_or(end_cell);
                if first <= at && at < last {
                    return Some(Link {
                        uri: text[i..end].to_string(),
                        runs: runs(first, last, |abs| self.row_at(abs).map_or(0, |r| r.cells().len())),
                    });
                }
            }
            i = end.max(i + 1);
        }
        None
    }
}

/// The runs of cells from `first` up to (not including) `last`.
fn runs(first: (u64, usize), last: (u64, usize), width: impl Fn(u64) -> usize) -> Vec<(u64, usize, usize)> {
    (first.0..=last.0)
        .map(|abs| {
            let c0 = if abs == first.0 { first.1 } else { 0 };
            let c1 = if abs == last.0 { last.1 } else { width(abs) };
            (abs, c0, c1)
        })
        .filter(|(_, c0, c1)| c0 < c1)
        .collect()
}

/// Drops trailing punctuation that ends a sentence rather than the URL,
/// keeping a closing bracket whose opening one is inside it.
fn trim_url_end(s: &str, start: usize, mut end: usize) -> usize {
    while end > start {
        let url = &s[start..end];
        let c = url.as_bytes()[url.len() - 1];
        let drop = match c {
            b'.' | b',' | b';' | b':' | b'!' | b'?' => true,
            b')' | b']' | b'}' => {
                let open = match c {
                    b')' => '(',
                    b']' => '[',
                    _ => '{',
                };
                url.matches(open).count() < url.matches(char::from(c)).count()
            }
            _ => false,
        };
        if !drop {
            break;
        }
        end -= 1;
    }
    end
}
