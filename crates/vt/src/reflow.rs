//! Resizing: the primary screen and its scrollback are rewrapped to the new
//! width from the cells themselves, with no replay of program output.

use crate::cell::{Cell, CellFlags};
use crate::grid::{Grid, Row, RowFlags};
use crate::term::Terminal;

/// One logical line: rows joined across soft wraps.
#[derive(Default)]
struct Line {
    cells: Vec<Cell>,
    graphemes: Vec<(usize, String)>,
    images: Vec<std::sync::Arc<crate::graphics::Placement>>,
    flags: RowFlags,
    /// The cursor's offset in the line, when it is on this line.
    cursor: Option<usize>,
}

impl Line {
    fn clear(&mut self) {
        self.cells.clear();
        self.graphemes.clear();
        self.images.clear();
        self.flags = RowFlags::default();
        self.cursor = None;
    }

    fn append(&mut self, row: &Row, cursor_x: Option<usize>) {
        let mut n = if row.wrapped() { row.cells.len() } else { row.used_len() };
        if row.wrapped() && n > 0 && row.cells[n - 1].flags.contains(CellFlags::WRAP_SPACER) {
            n -= 1;
        }
        if let Some(x) = cursor_x {
            n = n.max(x.min(row.cells.len()));
            self.cursor = Some(self.cells.len() + x);
        }
        let base = self.cells.len();
        if base == 0 {
            self.images.extend(row.images.iter().cloned());
        }
        self.cells.extend_from_slice(&row.cells[..n.min(row.cells.len())]);
        self.cells.resize(base + n, Cell::default());
        for (c, tail) in &row.graphemes {
            if usize::from(*c) < n {
                self.graphemes.push((base + usize::from(*c), tail.clone()));
            }
        }
    }
}

/// Wraps `line` into rows `cols` wide, appending them to `out`. Returns the
/// cursor's row and column when the cursor is on this line.
fn wrap(line: &Line, cols: usize, out: &mut Vec<Row>) -> Option<(usize, usize)> {
    let mut cursor = None;
    let mut row = Row::new(0, Cell::default());
    row.flags = RowFlags(line.flags.0 & !RowFlags::WRAPPED.0);
    row.images = line.images.clone();
    let mut col = 0;
    let mut i = 0;
    let n = line.cells.len();
    let mut graphemes = line.graphemes.iter().peekable();
    while i < n {
        let cell = line.cells[i];
        let w = if cell.is_wide() && i + 1 < n { 2 } else { 1 };
        if col + w > cols {
            if col < cols {
                row.cells.push(Cell { flags: CellFlags::WRAP_SPACER, ..Cell::default() });
            }
            row.flags.set(RowFlags::WRAPPED, true);
            out.push(std::mem::replace(&mut row, Row::new(0, Cell::default())));
            col = 0;
        }
        if let Some(c) = line.cursor
            && (i..i + w).contains(&c)
        {
            cursor = Some((out.len(), col + (c - i)));
        }
        row.cells.push(Cell { flags: CellFlags(cell.flags.0 & !CellFlags::WRAP_SPACER.0), ..cell });
        if w == 2 {
            row.cells.push(line.cells[i + 1]);
        }
        while let Some((gi, tail)) = graphemes.peek() {
            if *gi < i {
                graphemes.next();
            } else if *gi == i {
                row.graphemes.push((col as u16, tail.clone()));
                graphemes.next();
            } else {
                break;
            }
        }
        col += w;
        i += w;
    }
    if line.cursor == Some(n) {
        cursor = Some((out.len(), col));
    }
    out.push(row);
    cursor
}

/// A live row at a new width, cut or padded as xterm does.
fn cut_row(mut row: Row, cols: usize) -> Row {
    if row.cells.len() > cols {
        row.cells.truncate(cols);
        if row.cells.last().is_some_and(|c| c.is_wide()) {
            row.cells[cols - 1] = Cell::default();
        }
        row.clear_graphemes(cols, usize::MAX);
    }
    row.flags.set(RowFlags::WRAPPED, false);
    row.touch();
    row
}

impl Terminal {
    /// Resizes the terminal. The primary screen and scrollback are
    /// rewrapped to the new width; the alternate screen is cut or padded,
    /// since the program on it redraws.
    pub fn resize(&mut self, cols: usize, lines: usize) {
        let cols = cols.max(2);
        let lines = lines.max(1);
        if cols == self.cols && lines == self.lines {
            return;
        }
        let alt = self.alt_screen();
        let (cx, cy) = if alt { self.saved_primary_cursor() } else { self.cursor() };
        let (nx, ny) = self.reflow_primary(cols, lines, cx, cy);
        self.alt.resize_simple(cols, lines);
        self.cols = cols;
        self.lines = lines;
        self.top = 0;
        self.bottom = lines - 1;
        self.resize_tabs(cols);
        if alt {
            self.set_saved_primary_cursor(nx, ny);
            self.clamp_cursor();
        } else {
            self.clamp_cursor();
            self.set_cursor_pos(nx.min(cols - 1), ny.min(lines - 1));
        }
        self.display_offset = self.display_offset.min(self.history());
        self.commit_rows();
    }

    fn reflow_primary(&mut self, cols: usize, lines: usize, cx: usize, cy: usize) -> (usize, usize) {
        let screen = std::mem::replace(&mut self.primary, Grid::new(0, 0));
        let mut screen_rows: Vec<Row> = screen.rows.into();
        let content_end = screen_rows
            .iter()
            .rposition(|r| r.used_len() > 0 || r.flags != RowFlags::default())
            .map_or(0, |i| i + 1)
            .max(cy + 1)
            .min(screen_rows.len());
        screen_rows.truncate(content_end);
        // The cursor's line and everything below it are live: the program
        // there (a shell's line editor, a TUI drawing on the main screen)
        // redraws them after a resize, assuming the terminal kept its rows
        // as they were, as xterm does. Rewrapping them would make that
        // redraw land in the wrong place and leave pieces of old prompts.
        let mut live = cy.min(screen_rows.len().saturating_sub(1));
        while live > 0 && screen_rows[live - 1].wrapped() {
            live -= 1;
        }
        let history: Vec<Row> = std::mem::take(&mut self.scrollback.rows).into();
        let first_abs = self.scrollback.pushed - history.len() as u64;
        let history_len = history.len();

        let mut out: Vec<Row> = Vec::with_capacity(history_len + content_end);
        let mut cursor = (0, 0);
        let mut line = Line::default();
        let mut in_line = false;
        for (i, mut row) in history.into_iter().chain(screen_rows).enumerate() {
            if i >= history_len + live {
                if in_line && let Some(pos) = wrap(&line, cols, &mut out) {
                    cursor = pos;
                }
                in_line = false;
                if i == history_len + cy {
                    cursor = (out.len(), cx);
                }
                out.push(cut_row(row, cols));
                continue;
            }
            let cursor_x = (i == history_len + cy).then_some(cx);
            // A row that is a whole line and fits moves over unchanged.
            if !in_line && !row.wrapped() && cursor_x.is_none() && row.used_len() <= cols {
                let n = row.used_len();
                row.cells.truncate(n);
                row.clear_graphemes(n, usize::MAX);
                out.push(row);
                continue;
            }
            if !in_line {
                line.clear();
                line.flags = row.flags;
                in_line = true;
            }
            line.append(&row, cursor_x);
            if !row.wrapped() {
                if let Some(pos) = wrap(&line, cols, &mut out) {
                    cursor = pos;
                }
                in_line = false;
            }
        }
        if in_line && let Some(pos) = wrap(&line, cols, &mut out) {
            cursor = pos;
        }

        let screen_start = out.len().saturating_sub(lines).min(cursor.0);
        let mut screen: Vec<Row> = out.split_off(screen_start);
        for row in &mut screen {
            row.cells.resize(cols, Cell::default());
        }
        while screen.len() < lines {
            screen.push(Row::new(cols, Cell::default()));
        }
        // Rows beyond the bottom (the cursor was high on a tall screen
        // whose content is now taller than the window) are dropped.
        screen.truncate(lines);
        self.primary = Grid { rows: screen.into(), cols };

        self.scrollback.pushed = first_abs + out.len() as u64;
        let excess = out.len().saturating_sub(self.scrollback.limit);
        self.scrollback.rows = out.into_iter().skip(excess).collect();

        let (mut x, y) = (cursor.1, cursor.0 - screen_start);
        if x >= cols {
            x = cols - 1;
        }
        (x, y)
    }
}
