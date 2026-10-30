//! The terminal: screen state driven by the parser, and what the frontend
//! reads to draw it.

use std::collections::HashMap;
use std::sync::Arc;

use crate::cell::{Cell, CellFlags, Style, StyleId, StyleTable};
use crate::colors::{Palette, Theme};
use crate::graphics::{CellSize, Codec, Delete, Graphics, Outcome, Placement};
use crate::grid::{Grid, Row, RowFlags, Scrollback};
use crate::parser::{Params, Parser, Perform};
use crate::unicode::{char_width, cluster_width, joins_cluster, may_join, simple_width};

/// The longest grapheme kept in one cell, in bytes; further joiners are dropped.
const MAX_GRAPHEME: usize = 256;

/// The deepest kitty keyboard stack; pushing more drops the oldest entry.
const MAX_KITTY_STACK: usize = 32;

/// The most distinct hyperlinks kept before unused ones are dropped.
const MAX_LINKS: usize = 1 << 16;

/// The longest title kept, in bytes.
const MAX_TITLE: usize = 4096;

/// Which mouse events a program asked for.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum MouseMode {
    /// No reporting.
    #[default]
    Off,
    /// Presses only (DECSET 9).
    X10,
    /// Presses and releases (DECSET 1000).
    Click,
    /// Also motion while a button is held (DECSET 1002).
    Drag,
    /// Also motion without a button (DECSET 1003).
    Motion,
}

/// The cursor shape set by DECSCUSR.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum CursorShape {
    /// A filled block.
    #[default]
    Block,
    /// A line under the cell.
    Underline,
    /// A vertical bar at the left of the cell.
    Bar,
}

/// Terminal modes the frontend needs.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub struct Modes {
    /// DECCKM: cursor keys send SS3 sequences.
    pub app_cursor: bool,
    /// DECKPAM: the keypad sends application sequences.
    pub app_keypad: bool,
    /// DECOM: cursor addressing is relative to the scroll region.
    pub origin: bool,
    /// DECAWM: text wraps at the right edge.
    pub autowrap: bool,
    /// IRM: printing shifts the rest of the line right.
    pub insert: bool,
    /// LNM: line feed also returns the carriage.
    pub newline: bool,
    /// DECTCEM: the cursor is shown.
    pub cursor_visible: bool,
    /// DECSET 12: the cursor blinks (also set by DECSCUSR).
    pub cursor_blink: bool,
    /// DECSCNM: the whole screen is drawn in reverse video.
    pub reverse_video: bool,
    /// Mouse reporting.
    pub mouse: MouseMode,
    /// DECSET 1006: SGR mouse encoding.
    pub mouse_sgr: bool,
    /// DECSET 1004: focus in and out are reported.
    pub focus_events: bool,
    /// DECSET 1007: the wheel sends cursor keys on the alternate screen.
    pub alternate_scroll: bool,
    /// DECSET 2004: pastes are bracketed.
    pub bracketed_paste: bool,
    /// DECSET 2026: the program is mid-update; don't draw.
    pub synchronized: bool,
    /// DECSET 2027: graphemes, not codepoints, get cells.
    pub grapheme_clusters: bool,
    /// DECSET 2031: report light/dark changes.
    pub color_scheme_reports: bool,
}

impl Default for Modes {
    fn default() -> Self {
        Self {
            app_cursor: false,
            app_keypad: false,
            origin: false,
            autowrap: true,
            insert: false,
            newline: false,
            cursor_visible: true,
            cursor_blink: false,
            reverse_video: false,
            mouse: MouseMode::Off,
            mouse_sgr: false,
            focus_events: false,
            alternate_scroll: true,
            bracketed_paste: false,
            synchronized: false,
            grapheme_clusters: true,
            color_scheme_reports: false,
        }
    }
}

/// Something the frontend should act on, in stream order.
#[derive(Clone, Debug, PartialEq, Eq)]
pub enum Event {
    /// BEL.
    Bell,
    /// The title changed (OSC 0/2).
    TitleChanged,
    /// The working directory changed (OSC 7).
    CwdChanged,
    /// A desktop notification (OSC 9, 777, 99).
    Notify {
        /// Title, possibly empty.
        title: String,
        /// Body text.
        body: String,
    },
    /// Progress (OSC 9;4): state 0 removes it, 1 normal, 2 error,
    /// 3 indeterminate, 4 paused.
    Progress {
        /// The ConEmu progress state.
        state: u8,
        /// Percent done.
        percent: u8,
    },
    /// The program wrote to the clipboard (OSC 52).
    Clipboard(String),
    /// A shell prompt started (OSC 133;A).
    PromptStart,
    /// A command started running (OSC 133;C).
    CommandStart,
    /// A command finished (OSC 133;D) with its exit status, if given.
    CommandFinished(Option<i32>),
    /// The scrollback was erased (ED 3 or RIS).
    ScrollbackCleared,
    /// Mouse or focus reporting was switched on or off.
    ReportModesChanged,
    /// The kitty keyboard flags changed.
    KeyboardModeChanged,
}

#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
enum Charset {
    #[default]
    Ascii,
    DecSpecial,
}

#[derive(Clone, Debug, Default)]
struct Cursor {
    x: usize,
    y: usize,
    pending_wrap: bool,
    pen: Style,
    pen_id: StyleId,
    blank: Cell,
}

#[derive(Clone, Debug, Default)]
struct Saved {
    x: usize,
    y: usize,
    pending_wrap: bool,
    pen: Style,
    origin: bool,
    charsets: [Charset; 4],
    gl: usize,
}

/// Hyperlink targets, deduplicated; id 0 means no link.
#[derive(Clone, Debug, Default)]
struct Links {
    uris: Vec<Box<str>>,
    index: HashMap<Box<str>, u32>,
}

impl Links {
    fn get(&self, id: u32) -> Option<&str> {
        id.checked_sub(1).and_then(|i| self.uris.get(i as usize)).map(|s| &**s)
    }
}

/// A terminal: feed it program output with [`Terminal::advance`], send
/// [`Terminal::take_replies`] back to the program, and draw its rows.
pub struct Terminal {
    parser: Parser,
    pub(crate) cols: usize,
    pub(crate) lines: usize,
    pub(crate) primary: Grid,
    pub(crate) alt: Grid,
    pub(crate) alt_active: bool,
    pub(crate) scrollback: Scrollback,
    pub(crate) styles: StyleTable,
    cursor: Cursor,
    saved: [Saved; 2],
    pub(crate) top: usize,
    pub(crate) bottom: usize,
    modes: Modes,
    tabs: Vec<bool>,
    charsets: [Charset; 4],
    gl: usize,
    title: String,
    title_stack: Vec<String>,
    cwd: Option<String>,
    links: Links,
    pub(crate) palette: Palette,
    dark: bool,
    replies: Vec<u8>,
    events: Vec<Event>,
    kitty: [Vec<u8>; 2],
    cursor_shape: CursorShape,
    /// Whether the program asked for a blinking cursor (DECSCUSR 1/3/5) or
    /// a steady one (2/4/6); None until it asks, or after DECSCUSR 0.
    cursor_blink_request: Option<bool>,
    pub(crate) display_offset: usize,
    cell_pixels: (u16, u16),
    last_printed: Option<char>,
    /// Absolute row where the running command's output started.
    output_start: Option<u64>,
    notification: (String, String),
    scratch: String,
    scratch2: String,
    graphics: Graphics,
    codec: Option<Box<dyn Codec>>,
    /// The most rows any image here covers, so drawing knows how far
    /// above the view an image can start and still show.
    tallest_image: usize,
}

impl Terminal {
    /// A blank terminal `cols` by `lines`, keeping up to `scrollback` rows
    /// of history.
    pub fn new(cols: usize, lines: usize, scrollback: usize) -> Self {
        let cols = cols.max(2);
        let lines = lines.max(1);
        Self {
            parser: Parser::default(),
            cols,
            lines,
            primary: Grid::new(cols, lines),
            alt: Grid::new(cols, lines),
            alt_active: false,
            scrollback: Scrollback::new(scrollback),
            styles: StyleTable::default(),
            cursor: Cursor::default(),
            saved: [Saved::default(), Saved::default()],
            top: 0,
            bottom: lines - 1,
            modes: Modes::default(),
            tabs: default_tabs(cols),
            charsets: [Charset::Ascii; 4],
            gl: 0,
            title: String::new(),
            title_stack: Vec::new(),
            cwd: None,
            links: Links::default(),
            palette: Palette::default(),
            dark: true,
            replies: Vec::new(),
            events: Vec::new(),
            kitty: [Vec::new(), Vec::new()],
            cursor_shape: CursorShape::Block,
            cursor_blink_request: None,
            display_offset: 0,
            cell_pixels: (8, 16),
            last_printed: None,
            output_start: None,
            notification: (String::new(), String::new()),
            scratch: String::new(),
            scratch2: String::new(),
            graphics: Graphics::default(),
            codec: None,
            tallest_image: 0,
        }
    }

    /// Sets the decoders for PNG, JPEG, GIF, and zlib images.
    pub fn set_codec(&mut self, codec: Box<dyn Codec>) {
        self.codec = Some(codec);
    }

    /// Images that show in the view, with the view row of their top edge
    /// (negative when it is above the view).
    pub fn images_in_view(&self) -> Vec<(isize, Arc<Placement>)> {
        let mut out = Vec::new();
        if self.tallest_image == 0 {
            return out;
        }
        let top = self.view_top();
        let first = top.saturating_sub(self.tallest_image as u64).max(self.first_row());
        for abs in first..top + self.lines as u64 {
            let Some(row) = self.row_at(abs) else { continue };
            for p in row.images() {
                let y = abs as isize - top as isize;
                if y + p.rows as isize > 0 {
                    out.push((y, p.clone()));
                }
            }
        }
        out
    }

    fn cell_size(&self) -> CellSize {
        CellSize {
            width: u32::from(self.cell_pixels.0),
            height: u32::from(self.cell_pixels.1),
            cols: self.cols,
            rows: self.lines,
        }
    }

    pub(crate) fn sixel(&mut self, data: &[u8], transparent: bool) {
        let out = self.graphics.sixel(data, transparent, self.cell_size());
        self.apply_graphics(out);
    }

    pub(crate) fn kitty_graphics(&mut self, body: &[u8]) {
        let cell = self.cell_size();
        let out = self.graphics.kitty(body, cell, self.codec.as_deref());
        self.apply_graphics(out);
    }

    pub(crate) fn iterm_image(&mut self, body: &[u8]) {
        let cell = self.cell_size();
        let out = self.graphics.iterm(body, cell, self.codec.as_deref());
        self.apply_graphics(out);
    }

    fn apply_graphics(&mut self, out: Outcome) {
        if let Some(r) = out.reply {
            self.reply(r.as_bytes());
        }
        if let Some(d) = out.delete {
            self.delete_images(d);
        }
        if let Some(p) = out.place {
            self.place_image(p, out.inline, out.moves);
        }
    }

    /// Shows an image at the cursor: the cells it covers are cleared, the
    /// image is stored on its top row, and the cursor moves past it.
    fn place_image(&mut self, mut p: Placement, inline: bool, moves: bool) {
        let (x, y) = (self.cursor.x, self.cursor.y);
        p.col = x;
        self.tallest_image = self.tallest_image.max(p.rows).min(crate::graphics::MAX_DIMENSION);
        let blank = self.cursor.blank;
        let end = (x + p.cols).min(self.cols);
        let rows = if moves { p.rows } else { p.rows.min(self.lines - y) };
        for r in 0..rows {
            if r > 0 {
                if moves {
                    self.index();
                    self.cursor.x = x;
                } else {
                    self.cursor.y += 1;
                }
            }
            let cy = self.cursor.y;
            self.grid_mut().row_mut(cy).erase(x, end, blank);
        }
        let anchor = self.scrollback.pushed + self.cursor.y as u64 - (rows.max(1) - 1) as u64;
        let placement = Arc::new(p);
        let cols = placement.cols;
        if let Some(row) = self.row_at_mut(anchor) {
            row.images.push(placement);
            row.touch();
        }
        if !moves {
            self.cursor.x = x;
            self.cursor.y = y;
        } else if inline {
            self.index();
            self.cursor.x = x;
        } else {
            self.cursor.x = x;
            self.advance_cursor(cols);
        }
    }

    fn row_at_mut(&mut self, abs: u64) -> Option<&mut Row> {
        let screen_top = self.scrollback.pushed;
        if abs >= screen_top {
            let y = (abs - screen_top) as usize;
            return (y < self.lines).then(|| self.grid_mut().row_mut(y));
        }
        let back = (screen_top - abs) as usize;
        if self.alt_active || back > self.scrollback.len() {
            return None;
        }
        let i = self.scrollback.len() - back;
        Some(&mut self.scrollback.rows[i])
    }

    /// Removes placements as a kitty delete selects them: by image across
    /// the history, or by position on the screen.
    fn delete_images(&mut self, d: Delete) {
        let covers = |p: &Placement, anchor_y: usize, x: usize, y: usize| {
            (p.col..p.col + p.cols).contains(&x) && (anchor_y..anchor_y + p.rows).contains(&y)
        };
        let lines = self.lines;
        if let Delete::Image(id, pid) = d {
            let matches = |p: &Arc<Placement>| p.kitty.is_some_and(|(i, q)| i == id && (pid == 0 || q == pid));
            let grid = if self.alt_active { &mut self.alt } else { &mut self.primary };
            for row in grid.rows.iter_mut().chain(self.scrollback.rows.iter_mut()) {
                if row.images.iter().any(matches) {
                    row.images.retain(|p| !matches(p));
                    row.touch();
                }
            }
            return;
        }
        for anchor_y in 0..lines {
            let row = self.grid_mut().row_mut(anchor_y);
            row.images.retain(|p| match d {
                Delete::All => false,
                Delete::Cell(x, y) => !covers(p, anchor_y, x.saturating_sub(1), y.saturating_sub(1)),
                Delete::Column(x) => !(p.col..p.col + p.cols).contains(&x.saturating_sub(1)),
                Delete::Row(y) => !(anchor_y..anchor_y + p.rows).contains(&y.saturating_sub(1)),
                Delete::Range(lo, hi) => !p.kitty.is_some_and(|(i, _)| (lo..=hi).contains(&i)),
                Delete::Image(..) => true,
            });
        }
    }

    /// Feeds program output.
    pub fn advance(&mut self, bytes: &[u8]) {
        let mut parser = std::mem::take(&mut self.parser);
        parser.advance(self, bytes);
        self.parser = parser;
        self.commit_rows();
    }

    /// Renews the identity of rows changed since the last call.
    pub(crate) fn commit_rows(&mut self) {
        self.primary.commit();
        self.alt.commit();
    }

    /// Bytes to write back to the program (query replies), in order.
    pub fn take_replies(&mut self) -> Vec<u8> {
        std::mem::take(&mut self.replies)
    }

    /// Events since the last call, in order.
    pub fn take_events(&mut self) -> Vec<Event> {
        std::mem::take(&mut self.events)
    }

    /// Columns.
    pub fn cols(&self) -> usize {
        self.cols
    }

    /// Rows.
    pub fn lines(&self) -> usize {
        self.lines
    }

    /// Current modes.
    pub fn modes(&self) -> &Modes {
        &self.modes
    }

    /// The window title the program set (OSC 0/2), possibly empty.
    pub fn title(&self) -> &str {
        &self.title
    }

    /// The working directory the shell reported (OSC 7).
    pub fn cwd(&self) -> Option<&str> {
        self.cwd.as_deref()
    }

    /// The cursor's column and row on the screen.
    pub fn cursor(&self) -> (usize, usize) {
        (self.cursor.x, self.cursor.y)
    }

    /// The cursor shape.
    pub fn cursor_shape(&self) -> CursorShape {
        self.cursor_shape
    }

    /// Whether the program asked for a blinking cursor, a steady one, or
    /// neither (None), with DECSCUSR. DECSET 12 does not count: terminfo's
    /// cnorm sends it to every program, which would override the desktop.
    pub fn cursor_blink(&self) -> Option<bool> {
        self.cursor_blink_request
    }

    /// Reports whether the alternate screen is shown.
    pub fn alt_screen(&self) -> bool {
        self.alt_active
    }

    /// The style a cell refers to.
    pub fn style(&self, id: StyleId) -> &Style {
        self.styles.get(id)
    }

    /// The target of hyperlink `id` (from [`Style::link`]).
    pub fn link(&self, id: u32) -> Option<&str> {
        self.links.get(id)
    }

    /// The effective colours.
    pub fn palette(&self) -> &Palette {
        &self.palette
    }

    /// Sets the theme colours; `dark` answers colour-scheme queries.
    pub fn set_theme(&mut self, theme: Theme) {
        let dark = theme.bg.is_dark();
        self.palette.set_theme(theme);
        if dark != self.dark {
            self.dark = dark;
            if self.modes.color_scheme_reports {
                self.report_color_scheme();
            }
        }
    }

    /// Sets the cell size in pixels, for size reports (XTWINOPS).
    pub fn set_cell_pixels(&mut self, width: u16, height: u16) {
        self.cell_pixels = (width.max(1), height.max(1));
    }

    /// The active kitty keyboard flags.
    pub fn kitty_flags(&self) -> u8 {
        self.kitty[usize::from(self.alt_active)].last().copied().unwrap_or(0)
    }

    /// Clears the kitty keyboard stacks, when the program that set them
    /// has left the foreground.
    pub fn reset_keyboard_mode(&mut self) {
        self.kitty = [Vec::new(), Vec::new()];
    }

    /// Turns off mouse and focus reporting, when the program that asked
    /// for it has left the foreground.
    pub fn reset_report_modes(&mut self) {
        self.modes.mouse = MouseMode::Off;
        self.modes.mouse_sgr = false;
        self.modes.focus_events = false;
    }

    /// Rows scrolled back from the live screen.
    pub fn display_offset(&self) -> usize {
        self.display_offset
    }

    /// Rows of history.
    pub fn history(&self) -> usize {
        if self.alt_active { 0 } else { self.scrollback.len() }
    }

    /// Scrolls the view by `delta` rows (positive is back in history).
    pub fn scroll_display(&mut self, delta: isize) {
        let max = self.history() as isize;
        self.display_offset = (self.display_offset as isize + delta).clamp(0, max) as usize;
    }

    /// Returns the view to the live screen.
    pub fn scroll_to_bottom(&mut self) {
        self.display_offset = 0;
    }

    /// Row `y` of the view (scrollback when scrolled back).
    pub fn view_row(&self, y: usize) -> &Row {
        let abs = self.view_top() + y as u64;
        self.row_at(abs).unwrap_or_else(|| self.grid().row(y.min(self.lines - 1)))
    }

    /// The absolute row number at the top of the view. Rows are numbered
    /// from the first row ever pushed to the scrollback, so a number keeps
    /// pointing at the same row as output scrolls.
    pub fn view_top(&self) -> u64 {
        self.scrollback.pushed - self.display_offset as u64
    }

    /// The row with absolute number `abs`, if it is still held.
    pub fn row_at(&self, abs: u64) -> Option<&Row> {
        let screen_top = self.scrollback.pushed;
        if abs >= screen_top {
            let y = (abs - screen_top) as usize;
            return (y < self.lines).then(|| self.grid().row(y));
        }
        let back = (screen_top - abs) as usize;
        if self.alt_active || back > self.scrollback.len() {
            return None;
        }
        Some(&self.scrollback.rows[self.scrollback.len() - back])
    }

    /// The oldest absolute row still held.
    pub fn first_row(&self) -> u64 {
        self.scrollback.pushed - self.history() as u64
    }

    pub(crate) fn grid(&self) -> &Grid {
        if self.alt_active { &self.alt } else { &self.primary }
    }

    pub(crate) fn grid_mut(&mut self) -> &mut Grid {
        if self.alt_active { &mut self.alt } else { &mut self.primary }
    }

    pub(crate) fn reply(&mut self, bytes: &[u8]) {
        self.replies.extend_from_slice(bytes);
    }

    pub(crate) fn event(&mut self, e: Event) {
        self.events.push(e);
    }

    // ----------------------------------------------------------------
    // Styles
    // ----------------------------------------------------------------

    pub(crate) fn pen(&self) -> &Style {
        &self.cursor.pen
    }

    pub(crate) fn set_pen(&mut self, pen: Style) {
        if pen == self.cursor.pen && self.styles.get(self.cursor.pen_id) == &pen {
            return;
        }
        let bg_changed = pen.bg != self.cursor.pen.bg;
        self.cursor.pen = pen;
        self.cursor.pen_id = self.intern(pen);
        if bg_changed || self.styles.get(self.cursor.blank.style).bg != pen.bg {
            let blank = Style { bg: pen.bg, ..Style::default() };
            self.cursor.blank = Cell::blank(self.intern(blank));
        }
    }

    fn intern(&mut self, style: Style) -> StyleId {
        if let Some(id) = self.styles.intern(style) {
            return id;
        }
        self.compact_styles();
        self.styles.intern(style).unwrap_or_default()
    }

    /// Drops styles no cell uses any more, and links no style uses.
    fn compact_styles(&mut self) {
        let mut used = vec![false; self.styles.len()];
        let mut mark = |row: &Row| {
            for c in row.cells() {
                used[usize::from(c.style.0)] = true;
            }
        };
        self.primary.rows.iter().for_each(&mut mark);
        self.alt.rows.iter().for_each(&mut mark);
        self.scrollback.rows.iter().for_each(&mut mark);
        used[usize::from(self.cursor.pen_id.0)] = true;
        used[usize::from(self.cursor.blank.style.0)] = true;
        let map = self.styles.compact(&used);
        for row in self.primary.rows.iter_mut().chain(self.alt.rows.iter_mut()).chain(self.scrollback.rows.iter_mut()) {
            row.remap_styles(&map);
        }
        self.cursor.pen_id = map[usize::from(self.cursor.pen_id.0)];
        self.cursor.blank.style = map[usize::from(self.cursor.blank.style.0)];
    }

    fn link_id(&mut self, uri: &str) -> u32 {
        if let Some(&id) = self.links.index.get(uri) {
            return id;
        }
        if self.links.uris.len() >= MAX_LINKS {
            return 0;
        }
        self.links.uris.push(uri.into());
        let id = self.links.uris.len() as u32;
        self.links.index.insert(uri.into(), id);
        id
    }

    // ----------------------------------------------------------------
    // Printing
    // ----------------------------------------------------------------

    fn map_charset(&self, c: char) -> char {
        if self.charsets[self.gl] != Charset::DecSpecial {
            return c;
        }
        const DEC: [char; 32] = [
            ' ', '◆', '▒', '␉', '␌', '␍', '␊', '°', '±', '␤', '␋', '┘', '┐', '┌', '└', '┼', '⎺', '⎻', '─', '⎼', '⎽',
            '├', '┤', '┴', '┬', '│', '≤', '≥', 'π', '≠', '£', '·',
        ];
        match c {
            '_'..='~' => DEC[c as usize - 0x5f],
            _ => c,
        }
    }

    /// Where the cluster before the cursor is, for joining.
    fn prev_cell(&self) -> Option<usize> {
        let x = self.cursor.x;
        let row = self.grid().row(self.cursor.y);
        let px = if self.cursor.pending_wrap {
            x
        } else if x == 0 {
            return None;
        } else if row.cells[x - 1].flags.contains(CellFlags::SPACER) && x >= 2 {
            x - 2
        } else {
            x - 1
        };
        (row.cells[px].ch != 0).then_some(px)
    }

    fn print_char(&mut self, c: char) {
        let c = self.map_charset(c);
        let w = char_width(c);
        if let Some(px) = self.prev_cell() {
            let row = self.grid().row(self.cursor.y);
            let cell = row.cells[px];
            let last = match cell.flags.contains(CellFlags::GRAPHEME) {
                true => row.grapheme_tail(px).and_then(|t| t.chars().last()).unwrap_or(cell.char()),
                false => cell.char(),
            };
            let candidate = if self.modes.grapheme_clusters { may_join(last, c) } else { w == 0 };
            if candidate {
                let mut prev = std::mem::take(&mut self.scratch);
                prev.clear();
                self.grid().row(self.cursor.y).push_cell_text(px, &mut prev);
                let joins = !self.modes.grapheme_clusters || joins_cluster(&prev, c, &mut self.scratch2);
                if joins {
                    self.join_cluster(px, &prev, c);
                }
                self.scratch = prev;
                if joins {
                    return;
                }
            }
        }
        if w == 0 {
            return;
        }
        self.last_printed = Some(c);
        self.write_char(c, w);
    }

    fn join_cluster(&mut self, px: usize, prev: &str, c: char) {
        let n = prev.len();
        if n + c.len_utf8() > MAX_GRAPHEME {
            return;
        }
        // A wide cluster stays wide; only a narrow one is measured again.
        let wide = self.grid().row(self.cursor.y).cells[px].is_wide() || {
            let mut buf = [0u8; MAX_GRAPHEME + 4];
            buf[..n].copy_from_slice(prev.as_bytes());
            let len = n + c.encode_utf8(&mut buf[n..]).len();
            // SAFETY: a str followed by an encoded char.
            cluster_width(unsafe { std::str::from_utf8_unchecked(&buf[..len]) }) == 2
        };
        let (cols, y, pending) = (self.cols, self.cursor.y, self.cursor.pending_wrap);
        let style = self.cursor.pen_id;
        let row = self.grid_mut().row_mut(y);
        row.cells[px].flags.0 |= CellFlags::GRAPHEME.0;
        row.push_grapheme(px, c);
        if wide && !row.cells[px].is_wide() && px + 1 < cols {
            row.fix_wide_edges(px + 1, px + 2, Cell::default());
            row.clear_graphemes(px + 1, px + 2);
            row.cells[px].flags.0 |= CellFlags::WIDE.0;
            row.cells[px + 1] = Cell { ch: 0, style, flags: CellFlags::SPACER };
            if !pending {
                let x = px + 2;
                if x >= cols {
                    self.cursor.x = cols - 1;
                    self.cursor.pending_wrap = self.modes.autowrap;
                } else {
                    self.cursor.x = x;
                }
            }
        }
    }

    fn write_char(&mut self, c: char, w: usize) {
        if self.cursor.pending_wrap && self.modes.autowrap {
            self.wrap_line();
        }
        let cols = self.cols;
        if w == 2 && self.cursor.x == cols - 1 {
            if !self.modes.autowrap {
                return;
            }
            let (x, y, blank) = (self.cursor.x, self.cursor.y, self.cursor.blank);
            let row = self.grid_mut().row_mut(y);
            row.fix_wide_edges(x, x + 1, blank);
            row.clear_graphemes(x, x + 1);
            row.cells[x] = Cell { ch: 0, style: blank.style, flags: CellFlags::WRAP_SPACER };
            self.wrap_line();
        }
        if self.modes.insert {
            self.insert_blanks(w);
        }
        let (x, y, blank, style) = (self.cursor.x, self.cursor.y, self.cursor.blank, self.cursor.pen_id);
        let row = self.grid_mut().row_mut(y);
        row.fix_wide_edges(x, x + w, blank);
        row.clear_graphemes(x, x + w);
        row.cells[x] = Cell { ch: c as u32, style, flags: if w == 2 { CellFlags::WIDE } else { CellFlags(0) } };
        if w == 2 {
            row.cells[x + 1] = Cell { ch: 0, style, flags: CellFlags::SPACER };
        }
        self.advance_cursor(w);
    }

    fn advance_cursor(&mut self, n: usize) {
        let x = self.cursor.x + n;
        if x >= self.cols {
            self.cursor.x = self.cols - 1;
            self.cursor.pending_wrap = self.modes.autowrap;
        } else {
            self.cursor.x = x;
        }
    }

    fn wrap_line(&mut self) {
        let y = self.cursor.y;
        self.grid_mut().row_mut(y).flags.set(RowFlags::WRAPPED, true);
        self.cursor.x = 0;
        self.cursor.pending_wrap = false;
        self.index();
    }

    fn print_ascii_fast(&mut self, mut text: &[u8]) {
        let cols = self.cols;
        while !text.is_empty() {
            if self.cursor.pending_wrap {
                if self.modes.autowrap {
                    self.wrap_line();
                } else {
                    // Without wrapping, every remaining character lands on
                    // the last column; only the final one stays.
                    text = &text[text.len() - 1..];
                    self.cursor.pending_wrap = false;
                }
            }
            let (x, y, blank, style) = (self.cursor.x, self.cursor.y, self.cursor.blank, self.cursor.pen_id);
            let n = text.len().min(cols - x);
            let row = self.grid_mut().row_mut(y);
            row.fix_wide_edges(x, x + n, blank);
            row.clear_graphemes(x, x + n);
            for (cell, &b) in row.cells[x..x + n].iter_mut().zip(text) {
                *cell = Cell { ch: u32::from(b), style, flags: CellFlags(0) };
            }
            self.last_printed = Some(char::from(text[n - 1]));
            text = &text[n..];
            if x + n >= cols {
                self.cursor.x = cols - 1;
                self.cursor.pending_wrap = true;
            } else {
                self.cursor.x = x + n;
            }
        }
        if !self.modes.autowrap {
            self.cursor.pending_wrap = false;
        }
    }

    // ----------------------------------------------------------------
    // Cursor movement and scrolling
    // ----------------------------------------------------------------

    pub(crate) fn index(&mut self) {
        self.cursor.pending_wrap = false;
        if self.cursor.y == self.bottom {
            self.scroll_up(1, true);
        } else if self.cursor.y + 1 < self.lines {
            self.cursor.y += 1;
        }
    }

    pub(crate) fn reverse_index(&mut self) {
        self.cursor.pending_wrap = false;
        if self.cursor.y == self.top {
            self.scroll_down(1);
        } else if self.cursor.y > 0 {
            self.cursor.y -= 1;
        }
    }

    /// Scrolls the scroll region up by `n`. Rows leaving the top of the
    /// primary screen go to the scrollback when `save` is set.
    pub(crate) fn scroll_up(&mut self, n: usize, save: bool) {
        let (top, bottom, cols, blank) = (self.top, self.bottom, self.cols, self.cursor.blank);
        let n = n.min(bottom - top + 1);
        let save = save && !self.alt_active && top == 0;
        let full = top == 0 && bottom == self.lines - 1;
        let grid = if self.alt_active { &mut self.alt } else { &mut self.primary };
        let sb = &mut self.scrollback;
        if full {
            for _ in 0..n {
                let mut row = grid.rows.pop_front().unwrap();
                if save {
                    sb.push(&mut row);
                }
                row.reset(cols, blank);
                grid.rows.push_back(row);
            }
        } else {
            let rows = grid.rows.make_contiguous();
            if save {
                for row in &mut rows[top..top + n] {
                    sb.push(row);
                }
            }
            rows[top..=bottom].rotate_left(n);
            for row in &mut rows[bottom + 1 - n..=bottom] {
                row.reset(cols, blank);
            }
        }
        if save && self.display_offset > 0 {
            self.display_offset = (self.display_offset + n).min(self.scrollback.len());
        }
    }

    /// Scrolls the scroll region down by `n`, opening blank rows at its top.
    pub(crate) fn scroll_down(&mut self, n: usize) {
        let (top, bottom, cols, blank) = (self.top, self.bottom, self.cols, self.cursor.blank);
        let n = n.min(bottom - top + 1);
        let rows = self.grid_mut().rows.make_contiguous();
        rows[top..=bottom].rotate_right(n);
        for row in &mut rows[top..top + n] {
            row.reset(cols, blank);
        }
    }

    pub(crate) fn goto(&mut self, x: usize, y: usize) {
        let (min_y, max_y) = if self.modes.origin { (self.top, self.bottom) } else { (0, self.lines - 1) };
        self.cursor.x = x.min(self.cols - 1);
        self.cursor.y = (y + min_y).clamp(min_y, max_y);
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn goto_col(&mut self, x: usize) {
        self.cursor.x = x.min(self.cols - 1);
        self.cursor.pending_wrap = false;
    }

    /// Moves the cursor up (negative) or down, stopping at the scroll
    /// region's edge when it starts inside it.
    pub(crate) fn move_rows(&mut self, delta: isize) {
        let y = self.cursor.y;
        let (lo, hi) =
            if (self.top..=self.bottom).contains(&y) { (self.top, self.bottom) } else { (0, self.lines - 1) };
        self.cursor.y = (y as isize + delta).clamp(lo as isize, hi as isize) as usize;
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn move_cols(&mut self, delta: isize) {
        self.cursor.x = (self.cursor.x as isize + delta).clamp(0, self.cols as isize - 1) as usize;
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn backspace(&mut self) {
        if self.cursor.pending_wrap {
            self.cursor.pending_wrap = false;
        }
        self.cursor.x = self.cursor.x.saturating_sub(1);
    }

    pub(crate) fn tab(&mut self, n: usize) {
        for _ in 0..n {
            let x = self.cursor.x;
            self.cursor.x = (x + 1..self.cols).find(|&i| self.tabs[i]).unwrap_or(self.cols - 1);
        }
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn back_tab(&mut self, n: usize) {
        for _ in 0..n {
            let x = self.cursor.x;
            self.cursor.x = (0..x).rev().find(|&i| self.tabs[i]).unwrap_or(0);
        }
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn set_tab(&mut self) {
        let x = self.cursor.x;
        self.tabs[x] = true;
    }

    pub(crate) fn clear_tabs(&mut self, all: bool) {
        if all {
            self.tabs.fill(false);
        } else {
            let x = self.cursor.x;
            self.tabs[x] = false;
        }
    }

    pub(crate) fn set_margins(&mut self, top: usize, bottom: usize) {
        let bottom = if bottom == 0 { self.lines - 1 } else { (bottom - 1).min(self.lines - 1) };
        let top = top.saturating_sub(1);
        if top < bottom {
            self.top = top;
            self.bottom = bottom;
            self.goto(0, 0);
        }
    }

    pub(crate) fn repeat_last(&mut self, n: usize) {
        if let Some(c) = self.last_printed {
            let n = n.min(self.cols * self.lines);
            for _ in 0..n {
                self.print_char(c);
            }
        }
    }

    // ----------------------------------------------------------------
    // Editing
    // ----------------------------------------------------------------

    pub(crate) fn erase_line(&mut self, mode: u16) {
        let (x, y, cols, blank) = (self.cursor.x, self.cursor.y, self.cols, self.cursor.blank);
        self.cursor.pending_wrap = false;
        let row = self.grid_mut().row_mut(y);
        match mode {
            0 => {
                row.erase(x, cols, blank);
                row.flags.set(RowFlags::WRAPPED, false);
            }
            1 => row.erase(0, x + 1, blank),
            2 => {
                row.erase(0, cols, blank);
                row.flags.set(RowFlags::WRAPPED, false);
            }
            _ => {}
        }
    }

    pub(crate) fn erase_display(&mut self, mode: u16) {
        let (y, lines, cols, blank) = (self.cursor.y, self.lines, self.cols, self.cursor.blank);
        match mode {
            0 => {
                self.erase_line(0);
                for r in y + 1..lines {
                    self.grid_mut().row_mut(r).reset(cols, blank);
                }
            }
            1 => {
                for r in 0..y {
                    self.grid_mut().row_mut(r).reset(cols, blank);
                }
                self.erase_line(1);
            }
            2 => {
                for r in 0..lines {
                    self.grid_mut().row_mut(r).reset(cols, blank);
                }
                self.cursor.pending_wrap = false;
            }
            3 if !self.alt_active => {
                self.scrollback.clear();
                self.display_offset = 0;
                self.event(Event::ScrollbackCleared);
            }
            _ => {}
        }
    }

    pub(crate) fn erase_chars(&mut self, n: usize) {
        let (x, y, blank) = (self.cursor.x, self.cursor.y, self.cursor.blank);
        self.cursor.pending_wrap = false;
        self.grid_mut().row_mut(y).erase(x, x + n, blank);
    }

    pub(crate) fn insert_blanks(&mut self, n: usize) {
        let (x, y, cols, blank) = (self.cursor.x, self.cursor.y, self.cols, self.cursor.blank);
        let n = n.min(cols - x);
        self.cursor.pending_wrap = false;
        let row = self.grid_mut().row_mut(y);
        row.fix_wide_edges(x, x + 1, blank);
        // A wide character pushed off the end loses its second half.
        if cols - n > x && row.cells[cols - n - 1].is_wide() {
            row.cells[cols - n - 1] = blank;
        }
        row.cells[x..].rotate_right(n);
        row.cells[x..x + n].fill(blank);
        shift_graphemes(row, x, n as isize, cols);
        row.flags.set(RowFlags::WRAPPED, false);
    }

    pub(crate) fn delete_chars(&mut self, n: usize) {
        let (x, y, cols, blank) = (self.cursor.x, self.cursor.y, self.cols, self.cursor.blank);
        let n = n.min(cols - x);
        self.cursor.pending_wrap = false;
        let row = self.grid_mut().row_mut(y);
        row.fix_wide_edges(x, x + n, blank);
        row.clear_graphemes(x, x + n);
        row.cells[x..].rotate_left(n);
        row.cells[cols - n..].fill(blank);
        shift_graphemes(row, x + n, -(n as isize), cols);
        row.flags.set(RowFlags::WRAPPED, false);
    }

    pub(crate) fn insert_lines(&mut self, n: usize) {
        let y = self.cursor.y;
        if !(self.top..=self.bottom).contains(&y) {
            return;
        }
        let top = self.top;
        self.top = y;
        self.scroll_down(n);
        self.top = top;
        self.cursor.x = 0;
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn delete_lines(&mut self, n: usize) {
        let y = self.cursor.y;
        if !(self.top..=self.bottom).contains(&y) {
            return;
        }
        let top = self.top;
        self.top = y;
        self.scroll_up(n, false);
        self.top = top;
        self.cursor.x = 0;
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn screen_alignment(&mut self) {
        let (cols, lines) = (self.cols, self.lines);
        for y in 0..lines {
            let row = self.grid_mut().row_mut(y);
            row.reset(cols, Cell::default());
            for c in &mut row.cells {
                c.ch = 'E' as u32;
            }
        }
        self.top = 0;
        self.bottom = lines - 1;
        self.goto(0, 0);
    }

    // ----------------------------------------------------------------
    // Cursor save, screens, and resets
    // ----------------------------------------------------------------

    pub(crate) fn save_cursor(&mut self) {
        let i = usize::from(self.alt_active);
        self.saved[i] = Saved {
            x: self.cursor.x,
            y: self.cursor.y,
            pending_wrap: self.cursor.pending_wrap,
            pen: self.cursor.pen,
            origin: self.modes.origin,
            charsets: self.charsets,
            gl: self.gl,
        };
    }

    pub(crate) fn restore_cursor(&mut self) {
        let s = self.saved[usize::from(self.alt_active)].clone();
        self.set_pen(s.pen);
        self.modes.origin = s.origin;
        self.charsets = s.charsets;
        self.gl = s.gl;
        self.cursor.x = s.x.min(self.cols - 1);
        self.cursor.y = s.y.min(self.lines - 1);
        self.cursor.pending_wrap = s.pending_wrap;
    }

    pub(crate) fn enter_alt(&mut self, save: bool, clear: bool) {
        if self.alt_active {
            return;
        }
        if save {
            self.save_cursor();
        }
        self.alt_active = true;
        self.display_offset = 0;
        if clear {
            let (cols, lines, blank) = (self.cols, self.lines, self.cursor.blank);
            for y in 0..lines {
                self.alt.row_mut(y).reset(cols, blank);
            }
        }
    }

    pub(crate) fn leave_alt(&mut self, restore: bool) {
        if !self.alt_active {
            return;
        }
        self.alt_active = false;
        if restore {
            self.restore_cursor();
        }
    }

    pub(crate) fn soft_reset(&mut self) {
        self.modes.insert = false;
        self.modes.origin = false;
        self.modes.autowrap = true;
        self.modes.cursor_visible = true;
        self.modes.app_cursor = false;
        self.modes.app_keypad = false;
        self.top = 0;
        self.bottom = self.lines - 1;
        self.charsets = [Charset::Ascii; 4];
        self.gl = 0;
        self.set_pen(Style::default());
        self.saved = [Saved::default(), Saved::default()];
        self.cursor.pending_wrap = false;
    }

    pub(crate) fn full_reset(&mut self) {
        let (cols, lines) = (self.cols, self.lines);
        self.alt_active = false;
        self.primary = Grid::new(cols, lines);
        self.alt = Grid::new(cols, lines);
        self.scrollback.clear();
        self.display_offset = 0;
        self.modes = Modes::default();
        self.cursor = Cursor::default();
        self.soft_reset();
        self.tabs = default_tabs(cols);
        self.title.clear();
        self.title_stack.clear();
        self.palette.reset();
        self.kitty = [Vec::new(), Vec::new()];
        self.cursor_shape = CursorShape::Block;
        self.cursor_blink_request = None;
        self.last_printed = None;
        self.event(Event::ScrollbackCleared);
        self.event(Event::TitleChanged);
        self.event(Event::ReportModesChanged);
    }

    // ----------------------------------------------------------------
    // Settings used by the sequence handlers
    // ----------------------------------------------------------------

    pub(crate) fn modes_mut(&mut self) -> &mut Modes {
        &mut self.modes
    }

    pub(crate) fn set_title(&mut self, title: &str) {
        let mut t: String = title.chars().filter(|c| !c.is_control()).collect();
        if t.len() > MAX_TITLE {
            let mut end = MAX_TITLE;
            while !t.is_char_boundary(end) {
                end -= 1;
            }
            t.truncate(end);
        }
        if t != self.title {
            self.title = t;
            self.event(Event::TitleChanged);
        }
    }

    pub(crate) fn push_title(&mut self) {
        if self.title_stack.len() < 16 {
            self.title_stack.push(self.title.clone());
        }
    }

    pub(crate) fn pop_title(&mut self) {
        if let Some(t) = self.title_stack.pop() {
            self.set_title(&t);
        }
    }

    pub(crate) fn set_cwd(&mut self, cwd: String) {
        if self.cwd.as_deref() != Some(&cwd) {
            self.cwd = Some(cwd);
            self.event(Event::CwdChanged);
        }
    }

    pub(crate) fn set_link(&mut self, uri: &str) {
        let id = if uri.is_empty() { 0 } else { self.link_id(uri) };
        let pen = Style { link: id, ..self.cursor.pen };
        self.set_pen(pen);
    }

    pub(crate) fn designate_charset(&mut self, slot: usize, set: u8) {
        if slot < 4 {
            self.charsets[slot] = if set == b'0' { Charset::DecSpecial } else { Charset::Ascii };
        }
    }

    pub(crate) fn set_cursor_style(&mut self, ps: u16) {
        self.cursor_shape = match ps {
            3 | 4 => CursorShape::Underline,
            5 | 6 => CursorShape::Bar,
            _ => CursorShape::Block,
        };
        self.modes.cursor_blink = matches!(ps, 1 | 3 | 5);
        self.cursor_blink_request = match ps {
            1 | 3 | 5 => Some(true),
            2 | 4 | 6 => Some(false),
            _ => None,
        };
    }

    /// The DECSCUSR value for the current cursor (0 reads as 1).
    pub(crate) fn cursor_style_param(&self) -> u16 {
        let base = match self.cursor_shape {
            CursorShape::Block => 1,
            CursorShape::Underline => 3,
            CursorShape::Bar => 5,
        };
        if self.modes.cursor_blink { base } else { base + 1 }
    }

    pub(crate) fn kitty_stack(&mut self) -> &mut Vec<u8> {
        &mut self.kitty[usize::from(self.alt_active)]
    }

    pub(crate) fn push_kitty(&mut self, flags: u8) {
        let stack = self.kitty_stack();
        if stack.len() >= MAX_KITTY_STACK {
            stack.remove(0);
        }
        stack.push(flags);
        self.event(Event::KeyboardModeChanged);
    }

    pub(crate) fn cell_pixels(&self) -> (u16, u16) {
        self.cell_pixels
    }

    pub(crate) fn report_color_scheme(&mut self) {
        let mode = if self.dark { b"\x1b[?997;1n" } else { b"\x1b[?997;2n" };
        self.reply(mode);
    }

    pub(crate) fn mark_row(&mut self, flag: RowFlags) {
        let y = self.cursor.y;
        if !self.alt_active {
            self.grid_mut().row_mut(y).flags.set(flag, true);
        }
        if flag == RowFlags::OUTPUT {
            self.output_start = Some(self.scrollback.pushed + y as u64);
        }
    }

    pub(crate) fn finish_command(&mut self, exit: Option<i32>) {
        if let (Some(abs), Some(code)) = (self.output_start.take(), exit)
            && code != 0
        {
            let screen_top = self.scrollback.pushed;
            if abs >= screen_top && !self.alt_active {
                let y = (abs - screen_top) as usize;
                if y < self.lines {
                    self.primary.row_mut(y).flags.set(RowFlags::FAILED, true);
                }
            } else if let Some(i) = self.scrollback.len().checked_sub((screen_top - abs) as usize) {
                self.scrollback.rows[i].flags.set(RowFlags::FAILED, true);
            }
        }
        self.event(Event::CommandFinished(exit));
    }

    pub(crate) fn notification_mut(&mut self) -> &mut (String, String) {
        &mut self.notification
    }

    pub(crate) fn resize_tabs(&mut self, cols: usize) {
        let old = self.tabs.len();
        self.tabs.resize(cols, false);
        for i in old..cols {
            self.tabs[i] = i % 8 == 0;
        }
    }

    pub(crate) fn clamp_cursor(&mut self) {
        self.cursor.x = self.cursor.x.min(self.cols - 1);
        self.cursor.y = self.cursor.y.min(self.lines - 1);
        self.cursor.pending_wrap = false;
        for s in &mut self.saved {
            s.x = s.x.min(self.cols - 1);
            s.y = s.y.min(self.lines - 1);
        }
    }

    pub(crate) fn set_cursor_pos(&mut self, x: usize, y: usize) {
        self.cursor.x = x;
        self.cursor.y = y;
    }

    pub(crate) fn saved_primary_cursor(&self) -> (usize, usize) {
        (self.saved[0].x, self.saved[0].y)
    }

    pub(crate) fn set_saved_primary_cursor(&mut self, x: usize, y: usize) {
        self.saved[0].x = x.min(self.cols - 1);
        self.saved[0].y = y.min(self.lines - 1);
    }
}

fn default_tabs(cols: usize) -> Vec<bool> {
    (0..cols).map(|i| i % 8 == 0 && i > 0).collect()
}

/// Moves grapheme tails at or after `from` by `delta` columns, dropping
/// any that leave the row.
fn shift_graphemes(row: &mut Row, from: usize, delta: isize, cols: usize) {
    if row.graphemes.is_empty() {
        return;
    }
    row.graphemes.retain_mut(|(c, _)| {
        let col = usize::from(*c);
        if col < from {
            return true;
        }
        let n = col as isize + delta;
        if n < 0 || n as usize >= cols {
            return false;
        }
        *c = n as u16;
        true
    });
}

impl Perform for Terminal {
    fn print_ascii(&mut self, text: &[u8]) {
        if self.charsets[self.gl] != Charset::Ascii || self.modes.insert {
            for &b in text {
                self.print_char(char::from(b));
            }
        } else {
            self.print_ascii_fast(text);
        }
    }

    fn print(&mut self, c: char) {
        self.print_char(c);
    }

    fn print_str(&mut self, text: &str) {
        if self.charsets[self.gl] != Charset::Ascii || self.modes.insert || !self.modes.grapheme_clusters {
            for c in text.chars() {
                self.print_char(c);
            }
            return;
        }
        // Within a run the previous character is known, so a character
        // that cannot join it skips reading the row.
        let mut prev: Option<char> = None;
        for c in text.chars() {
            match prev.and_then(|p| simple_width(p, c)) {
                Some(w) => {
                    self.last_printed = Some(c);
                    self.write_char(c, w);
                }
                None => self.print_char(c),
            }
            prev = Some(c);
        }
    }

    fn execute(&mut self, byte: u8) {
        match byte {
            0x07 => self.event(Event::Bell),
            0x08 => self.backspace(),
            0x09 => self.tab(1),
            0x0a..=0x0c => {
                self.index();
                if self.modes.newline {
                    self.cursor.x = 0;
                }
            }
            0x0d => {
                self.cursor.x = 0;
                self.cursor.pending_wrap = false;
            }
            0x0e => self.gl = 1,
            0x0f => self.gl = 0,
            _ => {}
        }
    }

    fn csi(&mut self, params: &Params, intermediates: &[u8], action: u8) {
        crate::csi::dispatch(self, params, intermediates, action);
    }

    fn esc(&mut self, intermediates: &[u8], action: u8) {
        match (intermediates, action) {
            ([], b'7') => self.save_cursor(),
            ([], b'8') => self.restore_cursor(),
            ([], b'D') => self.index(),
            ([], b'E') => {
                self.index();
                self.cursor.x = 0;
            }
            ([], b'H') => self.set_tab(),
            ([], b'M') => self.reverse_index(),
            ([], b'c') => self.full_reset(),
            ([], b'=') => self.modes.app_keypad = true,
            ([], b'>') => self.modes.app_keypad = false,
            ([b'#'], b'8') => self.screen_alignment(),
            ([i @ (b'(' | b')' | b'*' | b'+')], set) => self.designate_charset(usize::from(i - b'('), set),
            _ => {}
        }
    }

    fn osc(&mut self, data: &[u8], bell_terminated: bool) {
        crate::osc::dispatch(self, data, bell_terminated);
    }

    fn dcs(&mut self, params: &Params, intermediates: &[u8], action: u8, data: &[u8]) {
        crate::osc::dcs(self, params, intermediates, action, data);
    }

    fn apc(&mut self, data: &[u8]) {
        if let Some(body) = data.strip_prefix(b"G") {
            self.kitty_graphics(body);
        }
    }
}
