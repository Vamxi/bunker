//! Drawing rows: text is shaped with Pango, snapped to the cell grid, and
//! drawn as GSK text nodes, which the GPU renderer caches as glyphs. Each
//! row's drawing is kept by row identity, so scrolling only draws the rows
//! that are new.

use std::collections::HashMap;

use bunker_vt::{Attrs, Cell, Palette, Rgb, Row, Style, StyleId, Terminal, Underline};
use gtk::prelude::*;
use gtk::{gdk, graphene, gsk, pango};

/// Font metrics in logical pixels, snapped to device pixels.
#[derive(Clone, Debug)]
pub struct Font {
    /// The regular face.
    pub desc: pango::FontDescription,
    /// Cell width.
    pub cell_w: f32,
    /// Cell height.
    pub cell_h: f32,
    /// Baseline offset from the top of a cell.
    pub ascent: f32,
    /// Underline offset below the baseline.
    pub underline_pos: f32,
    /// Line thickness for underlines, strikethrough, and overline.
    pub line: f32,
    /// Strikethrough offset above the baseline.
    pub strike_pos: f32,
    /// Device pixels per logical pixel (1.25 on a 125% display). Cell
    /// sizes are whole device pixels, so cell edges never fall inside a
    /// pixel, where touching backgrounds would leave a faint seam.
    pub scale: f32,
}

impl Font {
    /// Measures `desc` in `ctx`. `scale` is the device scale factor.
    pub fn measure(ctx: &pango::Context, desc: &pango::FontDescription, scale: f32) -> Self {
        let snap = |v: f32| (v * scale).round().max(1.0) / scale;
        let px = |v: i32| v as f32 / pango::SCALE as f32;
        let m = ctx.load_font(desc).map(|f| f.metrics(None));
        let (w, ascent, descent, height, ul_pos, ul_thick, st_pos) = match &m {
            Some(m) => (
                px(m.approximate_digit_width()),
                px(m.ascent()),
                px(m.descent()),
                px(m.height()),
                px(m.underline_position()),
                px(m.underline_thickness()),
                px(m.strikethrough_position()),
            ),
            None => (8.0, 13.0, 4.0, 17.0, -2.0, 1.0, 4.0),
        };
        let cell_h = snap(height.max(ascent + descent));
        let ascent = snap(ascent + (cell_h - ascent - descent) / 2.0);
        Self {
            desc: desc.clone(),
            cell_w: snap(w),
            cell_h,
            ascent,
            underline_pos: snap(-ul_pos).max(1.0 / scale),
            line: snap(ul_thick).max(1.0 / scale),
            strike_pos: snap(st_pos),
            scale,
        }
    }
}

/// Converts a terminal colour to GDK's.
pub fn rgba(c: Rgb, alpha: f32) -> gdk::RGBA {
    gdk::RGBA::new(f32::from(c.r) / 255.0, f32::from(c.g) / 255.0, f32::from(c.b) / 255.0, alpha)
}

/// The foreground and background a cell is drawn with; the background is
/// `None` when it is the default.
pub fn cell_colors(style: &Style, pal: &Palette, reverse_video: bool) -> (Rgb, Option<Rgb>) {
    let (dfg, dbg) = (pal.fg(), pal.bg());
    let mut fg = pal.resolve(style.fg, dfg);
    let mut bg = pal.resolve(style.bg, dbg);
    if style.attrs.contains(Attrs::REVERSE) != reverse_video {
        std::mem::swap(&mut fg, &mut bg);
    }
    if style.attrs.contains(Attrs::DIM) {
        fg = fg.mix(bg, 0.5);
    }
    (fg, (bg != dbg).then_some(bg))
}

/// The look of a run of text; cells with the same key are shaped together.
#[derive(Clone, Copy, PartialEq, Eq)]
struct RunKey {
    fg: Rgb,
    bold: bool,
    italic: bool,
    hidden: bool,
}

/// Shapes `text` and returns one text node per font Pango chose, with each
/// cluster's advance set to the cells it covers. `cols` gives the column
/// of each cluster as (byte offset, column), plus a final entry for the end.
fn shape_run(ctx: &pango::Context, font: &Font, key: RunKey, text: &str, cols: &[(usize, usize)], out: &gtk::Snapshot) {
    let mut desc = font.desc.clone();
    if key.bold {
        desc.set_weight(pango::Weight::Bold);
    }
    if key.italic {
        desc.set_style(pango::Style::Italic);
    }
    let attrs = pango::AttrList::new();
    attrs.insert(pango::AttrFontDesc::new(&desc));
    let color = rgba(key.fg, 1.0);
    let cell = (font.cell_w * pango::SCALE as f32) as f64;
    // Column of the cluster containing `byte`, and of the first cluster
    // starting at or after it.
    let start_col = |byte: usize| match cols.binary_search_by_key(&byte, |&(b, _)| b) {
        Ok(i) => cols[i].1,
        Err(i) => cols[i.saturating_sub(1)].1,
    };
    let end_col = |byte: usize| match cols.binary_search_by_key(&byte, |&(b, _)| b) {
        Ok(i) => cols[i].1,
        Err(i) => cols[i.min(cols.len() - 1)].1,
    };
    for item in pango::itemize(ctx, text, 0, text.len() as i32, &attrs, None) {
        let off = item.offset() as usize;
        let len = item.length() as usize;
        let mut glyphs = pango::GlyphString::new();
        pango::shape(&text[off..off + len], item.analysis(), &mut glyphs);
        let clusters: Vec<i32> = glyphs.log_clusters().to_vec();
        let infos = glyphs.glyph_info_mut();
        let mut i = 0;
        while i < infos.len() {
            let mut j = i + 1;
            while j < infos.len() && clusters[j] == clusters[i] {
                j += 1;
            }
            let first = start_col(off + clusters[i] as usize);
            let next_byte = if j < infos.len() { off + clusters[j] as usize } else { off + len };
            let cells = end_col(next_byte).saturating_sub(first).max(1);
            let target = (cells as f64 * cell).round() as i32;
            let natural: i32 = infos[i..j].iter().map(|g| g.geometry().width()).sum();
            let last = infos[j - 1].geometry().width();
            infos[j - 1].geometry_mut().set_width(last + target - natural);
            i = j;
        }
        let x = start_col(off) as f32 * font.cell_w;
        if let Some(node) =
            gsk::TextNode::new(&item.analysis().font(), &glyphs, &color, &graphene::Point::new(x, font.ascent))
        {
            out.append_node(&node);
        }
    }
}

fn draw_underline(snap: &gtk::Snapshot, font: &Font, kind: Underline, x: f32, w: f32, color: &gdk::RGBA) {
    let y = font.ascent + font.underline_pos;
    let t = font.line;
    match kind {
        Underline::None => {}
        Underline::Single => snap.append_color(color, &graphene::Rect::new(x, y, w, t)),
        Underline::Double => {
            snap.append_color(color, &graphene::Rect::new(x, y - t, w, t));
            snap.append_color(color, &graphene::Rect::new(x, y + t, w, t));
        }
        Underline::Dotted | Underline::Dashed => {
            let (on, period) = if kind == Underline::Dotted { (t, 2.0 * t) } else { (3.0 * t, 5.0 * t) };
            let mut dx = 0.0;
            while dx < w {
                snap.append_color(color, &graphene::Rect::new(x + dx, y, on.min(w - dx), t));
                dx += period;
            }
        }
        Underline::Curly => {
            let amp = (font.cell_h - y).clamp(1.0, 2.0 * t);
            let b = gsk::PathBuilder::new();
            let half = font.cell_w / 2.0;
            b.move_to(x, y);
            let mut dx = 0.0;
            let mut up = true;
            while dx < w {
                let step = half.min(w - dx);
                let cy = if up { y - amp } else { y + amp };
                b.quad_to(x + dx + step / 2.0, cy, x + dx + step, y);
                dx += step;
                up = !up;
            }
            snap.append_stroke(&b.to_path(), &gsk::Stroke::new(t), color);
        }
    }
}

/// Draws one row at the origin. `None` when the row is blank.
pub fn draw_row(ctx: &pango::Context, font: &Font, st: &Styles, row: &Row) -> Option<gsk::RenderNode> {
    let pal = &st.palette;
    let rv = st.reverse;
    let cells = row.cells();
    let snap = gtk::Snapshot::new();
    let cw = font.cell_w;

    // Backgrounds, merged into runs of one colour.
    let mut run: Option<(usize, Rgb)> = None;
    for (x, cell) in cells.iter().enumerate().chain(std::iter::once((cells.len(), &Cell::default()))) {
        let bg = if x < cells.len() { cell_colors(st.style(cell.style), pal, rv).1 } else { None };
        match (run, bg) {
            (Some((_, c)), Some(b)) if c == b => {}
            _ => {
                if let Some((start, c)) = run {
                    snap.append_color(
                        &rgba(c, 1.0),
                        &graphene::Rect::new(start as f32 * cw, 0.0, (x - start) as f32 * cw, font.cell_h),
                    );
                }
                run = bg.map(|b| (x, b));
            }
        }
    }

    // Text, in runs of one look.
    let mut text = String::new();
    let mut cols: Vec<(usize, usize)> = Vec::new();
    let mut key: Option<RunKey> = None;
    let flush = |key: Option<RunKey>, text: &mut String, cols: &mut Vec<(usize, usize)>, end: usize| {
        if let Some(k) = key
            && !k.hidden
            && !text.trim().is_empty()
        {
            cols.push((text.len(), end));
            shape_run(ctx, font, k, text, cols, &snap);
        }
        text.clear();
        cols.clear();
    };
    for (x, cell) in cells.iter().enumerate() {
        if cell.is_spacer() {
            continue;
        }
        let style = st.style(cell.style);
        let (fg, _) = cell_colors(style, pal, rv);
        let k = RunKey {
            fg,
            bold: style.attrs.contains(Attrs::BOLD),
            italic: style.attrs.contains(Attrs::ITALIC),
            hidden: style.attrs.contains(Attrs::HIDDEN),
        };
        let w = if cell.is_wide() { 2.0 * cw } else { cw };
        if !k.hidden
            && super::glyphs::draw(&snap, cell.char(), x as f32 * cw, w, font.cell_h, font.scale, rgba(fg, 1.0))
        {
            flush(key, &mut text, &mut cols, x);
            key = None;
            continue;
        }
        if key != Some(k) {
            flush(key, &mut text, &mut cols, x);
            key = Some(k);
        }
        cols.push((text.len(), x));
        row.push_cell_text(x, &mut text);
    }
    flush(key, &mut text, &mut cols, cells.len());

    // Lines over and under the text.
    for (x, cell) in cells.iter().enumerate() {
        let style = st.style(cell.style);
        let decorated = style.underline != Underline::None
            || style.attrs.contains(Attrs::STRIKE)
            || style.attrs.contains(Attrs::OVERLINE)
            || style.link != 0;
        if !decorated || cell.is_spacer() {
            continue;
        }
        let w = if cell.is_wide() { 2.0 * cw } else { cw };
        let (fg, _) = cell_colors(style, pal, rv);
        let x = x as f32 * cw;
        let color = rgba(fg, 1.0);
        if style.underline != Underline::None {
            let uc = rgba(pal.resolve(style.underline_color, fg), 1.0);
            draw_underline(&snap, font, style.underline, x, w, &uc);
        } else if style.link != 0 {
            draw_underline(&snap, font, Underline::Dotted, x, w, &rgba(fg, 0.6));
        }
        if style.attrs.contains(Attrs::STRIKE) {
            snap.append_color(&color, &graphene::Rect::new(x, font.ascent - font.strike_pos, w, font.line));
        }
        if style.attrs.contains(Attrs::OVERLINE) {
            snap.append_color(&color, &graphene::Rect::new(x, 0.0, w, font.line));
        }
    }
    snap.to_node()
}

/// Shapes the single cell at `x` in `color`, for the block cursor.
pub fn draw_cell_text(
    ctx: &pango::Context,
    font: &Font,
    st: &Styles,
    row: &Row,
    x: usize,
    color: Rgb,
) -> Option<gsk::RenderNode> {
    let cell = row.cells().get(x)?;
    if cell.is_spacer() || cell.ch == 0 {
        return None;
    }
    let style = st.style(cell.style);
    let mut text = String::new();
    row.push_cell_text(x, &mut text);
    let w = if cell.is_wide() { 2 } else { 1 };
    let key = RunKey {
        fg: color,
        bold: style.attrs.contains(Attrs::BOLD),
        italic: style.attrs.contains(Attrs::ITALIC),
        hidden: false,
    };
    let snap = gtk::Snapshot::new();
    let cell_w = w as f32 * font.cell_w;
    if !super::glyphs::draw(&snap, cell.char(), 0.0, cell_w, font.cell_h, font.scale, rgba(color, 1.0)) {
        shape_run(ctx, font, key, &text, &[(0, 0), (text.len(), w)], &snap);
    }
    snap.to_node()
}

/// The colours and styles drawing needs, copied from the terminal under its
/// lock, so rows are shaped after the lock is released.
pub struct Styles {
    /// The effective colours.
    pub palette: Palette,
    /// DECSCNM.
    pub reverse: bool,
    map: HashMap<StyleId, Style>,
}

impl Styles {
    /// Starts a copy with the terminal's colours.
    pub fn new(term: &Terminal) -> Self {
        Self { palette: term.palette().clone(), reverse: term.modes().reverse_video, map: HashMap::new() }
    }

    /// Copies the styles `row` uses.
    pub fn add_row(&mut self, term: &Terminal, row: &Row) {
        for c in row.cells() {
            self.map.entry(c.style).or_insert_with(|| *term.style(c.style));
        }
    }

    /// The style of a copied row's cell.
    pub fn style(&self, id: StyleId) -> &Style {
        static DEFAULT: Style = Style {
            fg: bunker_vt::Color::Default,
            bg: bunker_vt::Color::Default,
            underline_color: bunker_vt::Color::Default,
            attrs: Attrs(0),
            underline: Underline::None,
            link: 0,
        };
        self.map.get(&id).unwrap_or(&DEFAULT)
    }
}

/// Row drawings by row identity, valid for one font and palette.
#[derive(Default)]
pub struct RowCache {
    nodes: HashMap<u64, Option<gsk::RenderNode>>,
    used: HashMap<u64, Option<gsk::RenderNode>>,
    generation: (u64, u64, bool),
}

impl RowCache {
    /// Drops everything when the font, palette, or screen colours change.
    pub fn validate(&mut self, font_gen: u64, palette_gen: u64, reverse: bool) {
        let g = (font_gen, palette_gen, reverse);
        if g != self.generation {
            self.nodes.clear();
            self.used.clear();
            self.generation = g;
        }
    }

    /// Reports whether the drawing of row `id` is cached.
    pub fn has(&self, id: u64) -> bool {
        self.nodes.contains_key(&id) || self.used.contains_key(&id)
    }

    /// The drawing of row `id`, from the cache or made by `draw`.
    pub fn get(&mut self, id: u64, draw: impl FnOnce() -> Option<gsk::RenderNode>) -> Option<gsk::RenderNode> {
        let node = match self.nodes.remove(&id) {
            Some(n) => n,
            None => self.used.get(&id).cloned().unwrap_or_else(draw),
        };
        self.used.insert(id, node.clone());
        node
    }

    /// Keeps only the rows used since the last call.
    pub fn end_frame(&mut self) {
        self.nodes = std::mem::take(&mut self.used);
    }
}
