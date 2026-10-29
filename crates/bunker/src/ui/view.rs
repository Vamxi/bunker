//! The terminal widget: draws a session and turns keyboard, mouse, and
//! clipboard events into its input.

use std::cell::{Cell, RefCell};
use std::rc::Rc;
use std::time::{Duration, Instant};

use bunker_vt::{
    CursorShape, KeyModes, MouseAction, MouseButton, MouseMode, Point, Row, Terminal, encode_focus, encode_key,
    encode_mouse, encode_paste,
};
use gtk::glib::{self, clone};
use gtk::prelude::*;
use gtk::subclass::prelude::*;
use gtk::{gdk, graphene, gsk, pango};

use super::keys::{self, Shortcut};
use super::render::{self, Font, RowCache, rgba};
use crate::session::Session;

/// Lines scrolled per wheel notch.
const WHEEL_LINES: f64 = 3.0;
/// How long a program may hold a synchronized update before drawing resumes.
const SYNC_TIMEOUT: Duration = Duration::from_secs(1);
/// How often a drag selection outside the view scrolls.
const EDGE_SCROLL_TICK: Duration = Duration::from_millis(50);

mod imp {
    use super::*;

    #[derive(Default)]
    pub struct TermView {
        pub state: std::cell::OnceCell<Rc<super::View>>,
    }

    #[glib::object_subclass]
    impl ObjectSubclass for TermView {
        const NAME: &'static str = "BunkerTermView";
        type Type = super::TermView;
        type ParentType = gtk::Widget;
    }

    impl ObjectImpl for TermView {}

    impl WidgetImpl for TermView {
        fn snapshot(&self, snapshot: &gtk::Snapshot) {
            if let Some(s) = self.state.get() {
                s.snapshot(&self.obj(), snapshot);
            }
        }

        fn size_allocate(&self, width: i32, height: i32, baseline: i32) {
            self.parent_size_allocate(width, height, baseline);
            if let Some(s) = self.state.get() {
                s.allocate(width, height);
            }
        }

        fn measure(&self, orientation: gtk::Orientation, _for_size: i32) -> (i32, i32, i32, i32) {
            let Some(s) = self.state.get() else { return (0, 0, -1, -1) };
            let f = s.font.borrow();
            let pad = 2.0 * s.padding.get();
            match orientation {
                gtk::Orientation::Horizontal => {
                    ((pad + 10.0 * f.cell_w) as i32, (pad + 80.0 * f.cell_w) as i32, -1, -1)
                }
                _ => ((pad + 3.0 * f.cell_h) as i32, (pad + 24.0 * f.cell_h) as i32, -1, -1),
            }
        }
    }
}

glib::wrapper! {
    /// The terminal widget.
    pub struct TermView(ObjectSubclass<imp::TermView>)
        @extends gtk::Widget,
        @implements gtk::Accessible, gtk::Buildable, gtk::ConstraintTarget;
}

#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum SelectBy {
    Cell,
    Word,
    Line,
}

#[derive(Clone, Copy, Debug)]
struct Selection {
    anchor: Point,
    head: Point,
    by: SelectBy,
    block: bool,
}

impl Selection {
    /// The selected range, ordered, expanded to words or lines.
    fn range(&self, t: &Terminal) -> (Point, Point) {
        let (a, b) = if self.anchor <= self.head { (self.anchor, self.head) } else { (self.head, self.anchor) };
        match self.by {
            SelectBy::Cell => (a, b),
            SelectBy::Word => (t.word_bounds(a).0, t.word_bounds(b).1),
            SelectBy::Line => (t.line_bounds(a).0, t.line_bounds(b).1),
        }
    }

    fn text(&self, t: &Terminal) -> String {
        if self.block {
            return t.block_text(self.anchor, self.head);
        }
        let (a, b) = self.range(t);
        t.text(a, b)
    }

    /// The selected columns of absolute row `r`, inclusive.
    fn cols(&self, t: &Terminal, r: u64, cols: usize) -> Option<(usize, usize)> {
        if self.block {
            let (r0, r1) = (self.anchor.row.min(self.head.row), self.anchor.row.max(self.head.row));
            let (c0, c1) = (self.anchor.col.min(self.head.col), self.anchor.col.max(self.head.col));
            return (r0..=r1).contains(&r).then_some((c0, c1));
        }
        let (a, b) = self.range(t);
        if r < a.row || r > b.row {
            return None;
        }
        let c0 = if r == a.row { a.col } else { 0 };
        let c1 = if r == b.row { b.col } else { cols - 1 };
        Some((c0, c1))
    }
}

/// What the view asks its window to do.
pub type ActionHandler = Box<dyn Fn(&str)>;

/// Frames drawn, their total and longest drawing time, and when counting
/// started.
type FrameStats = (u32, Duration, Duration, Option<Instant>);

/// Where a search match starts and ends.
type Match = (Point, Point);

/// The view's state; the widget delegates to it.
pub struct View {
    widget: glib::WeakRef<TermView>,
    /// The program shown.
    pub session: Session,
    font: RefCell<Font>,
    base_font: RefCell<pango::FontDescription>,
    font_step: Cell<i32>,
    font_gen: Cell<u64>,
    padding: Cell<f32>,
    cache: RefCell<RowCache>,
    selection: RefCell<Option<Selection>>,
    selecting: Cell<bool>,
    focused: Cell<bool>,
    grid: Cell<(usize, usize)>,
    im: gtk::IMMulticontext,
    scroll_acc: Cell<f64>,
    buttons: Cell<u8>,
    pointer: Cell<(f64, f64)>,
    last_cell: Cell<(usize, usize)>,
    last_frame: RefCell<Option<gsk::RenderNode>>,
    sync_since: Cell<Option<Instant>>,
    edge_timer: RefCell<Option<glib::SourceId>>,
    shortcuts: RefCell<Vec<Shortcut>>,
    on_action: RefCell<Option<ActionHandler>>,
    /// Frames drawn, total and longest drawing time, since the last report.
    frame_stats: Cell<FrameStats>,
    last_input: Cell<Option<Instant>>,
    /// The search query and the current match, while searching.
    search: RefCell<Option<(String, Option<Match>)>>,
    /// Every key but the toggle goes to the program (Ctrl+F12).
    passthrough: Cell<bool>,
    /// GPU textures of shown images, by image id.
    textures: RefCell<std::collections::HashMap<u64, gdk::Texture>>,
}

impl TermView {
    /// A view of `session`.
    pub fn new(session: Session, font: &pango::FontDescription, padding: u32) -> (Self, Rc<View>) {
        let widget: Self = glib::Object::new();
        widget.set_focusable(true);
        widget.set_hexpand(true);
        widget.set_vexpand(true);
        widget.set_cursor_from_name(Some("text"));
        let measured = Font::measure(&widget.pango_context(), font, widget.scale_factor() as f32);
        let view = Rc::new(View {
            widget: widget.downgrade(),
            session,
            font: RefCell::new(measured),
            base_font: RefCell::new(font.clone()),
            font_step: Cell::new(0),
            font_gen: Cell::new(1),
            padding: Cell::new(padding as f32),
            cache: RefCell::new(RowCache::default()),
            selection: RefCell::new(None),
            selecting: Cell::new(false),
            focused: Cell::new(false),
            grid: Cell::new((0, 0)),
            im: gtk::IMMulticontext::new(),
            scroll_acc: Cell::new(0.0),
            buttons: Cell::new(0),
            pointer: Cell::new((0.0, 0.0)),
            last_cell: Cell::new((usize::MAX, usize::MAX)),
            last_frame: RefCell::new(None),
            sync_since: Cell::new(None),
            edge_timer: RefCell::new(None),
            shortcuts: RefCell::new(keys::shortcuts(&Default::default())),
            on_action: RefCell::new(None),
            frame_stats: Cell::new((0, Duration::ZERO, Duration::ZERO, None)),
            last_input: Cell::new(None),
            search: RefCell::new(None),
            passthrough: Cell::new(false),
            textures: RefCell::new(std::collections::HashMap::new()),
        });
        let _ = widget.imp().state.set(view.clone());
        View::install(&view, &widget);
        widget.connect_scale_factor_notify(clone!(
            #[weak]
            view,
            move |_| view.remeasure()
        ));
        // The fractional scale (1.25 on a 125% display) is only known once
        // the widget is on a surface, and changes when the window moves to
        // another monitor.
        widget.connect_realize(clone!(
            #[weak]
            view,
            move |w| {
                if let Some(surface) = w.native().and_then(|n| n.surface()) {
                    surface.connect_scale_notify(clone!(
                        #[weak]
                        view,
                        move |_| view.remeasure()
                    ));
                }
                view.remeasure();
            }
        ));
        (widget, view)
    }
}

impl View {
    /// Sets what runs when a shortcut asks the window for something.
    pub fn set_action_handler(&self, f: ActionHandler) {
        *self.on_action.borrow_mut() = Some(f);
    }

    fn widget(&self) -> Option<TermView> {
        self.widget.upgrade()
    }

    /// Redraws.
    pub fn queue_draw(&self) {
        if let Some(w) = self.widget() {
            w.queue_draw();
        }
    }

    /// The cell size in logical pixels.
    pub fn cell_size(&self) -> (f32, f32) {
        let f = self.font.borrow();
        (f.cell_w, f.cell_h)
    }

    fn remeasure(&self) {
        let Some(w) = self.widget() else { return };
        let mut desc = self.base_font.borrow().clone();
        let step = self.font_step.get();
        if step != 0 {
            let size = (desc.size() + step * pango::SCALE).max(4 * pango::SCALE);
            desc.set_size(size);
        }
        *self.font.borrow_mut() = Font::measure(&w.pango_context(), &desc, device_scale(&w));
        self.font_gen.set(self.font_gen.get() + 1);
        self.grid.set((0, 0));
        w.queue_resize();
        w.queue_draw();
    }

    /// Changes the font, keeping any zoom.
    pub fn set_font(&self, desc: &pango::FontDescription) {
        if *self.base_font.borrow() != *desc {
            *self.base_font.borrow_mut() = desc.clone();
            self.remeasure();
        }
    }

    /// The padding, rounded to whole device pixels.
    fn pad(&self) -> f32 {
        let scale = self.font.borrow().scale;
        (self.padding.get() * scale).round() / scale
    }

    /// Changes the space around the text.
    pub fn set_padding(&self, padding: u32) {
        if self.padding.get() != padding as f32 {
            self.padding.set(padding as f32);
            self.grid.set((0, 0));
            if let Some(w) = self.widget() {
                w.queue_resize();
            }
        }
    }

    /// Drops cached drawing, after the colours change.
    pub fn redraw_all(&self) {
        self.font_gen.set(self.font_gen.get() + 1);
        self.queue_draw();
    }

    fn zoom(&self, step: i32) {
        self.font_step.set(if step == 0 { 0 } else { self.font_step.get() + step });
        self.remeasure();
    }

    fn allocate(&self, width: i32, height: i32) {
        let (cw, ch) = self.cell_size();
        let pad = self.pad();
        let cols = (((width as f32 - 2.0 * pad) / cw).floor() as usize).max(2);
        let lines = (((height as f32 - 2.0 * pad) / ch).floor() as usize).max(1);
        if self.grid.get() != (cols, lines) {
            self.grid.set((cols, lines));
            self.selection.replace(None);
            self.session.resize(cols, lines, cw.round() as u16, ch.round() as u16);
        }
    }

    /// The cell under a widget position, clamped to the grid.
    fn cell_at(&self, x: f64, y: f64) -> (usize, usize) {
        let (cw, ch) = self.cell_size();
        let pad = f64::from(self.pad());
        let (cols, lines) = self.grid.get();
        let col = ((x - pad) / f64::from(cw)).floor().clamp(0.0, (cols.max(1) - 1) as f64) as usize;
        let row = ((y - pad) / f64::from(ch)).floor().clamp(0.0, (lines.max(1) - 1) as f64) as usize;
        (col, row)
    }

    /// Rows above (negative) or below (positive) the grid.
    fn rows_outside(&self, y: f64) -> i64 {
        let (_, ch) = self.cell_size();
        let lines = self.grid.get().1 as f64;
        let r = (y - f64::from(self.pad())) / f64::from(ch);
        if r < 0.0 {
            -(-r).ceil() as i64
        } else if r >= lines {
            (r - lines) as i64 + 1
        } else {
            0
        }
    }

    // ------------------------------------------------------------------
    // Drawing
    // ------------------------------------------------------------------

    fn snapshot(&self, w: &TermView, snap: &gtk::Snapshot) {
        let start = Instant::now();
        self.draw(w, snap);
        if crate::log::enabled(crate::log::Level::Debug) {
            self.note_frame(start.elapsed());
        }
    }

    /// Logs frame counts and drawing times once a second.
    fn note_frame(&self, took: Duration) {
        let (n, total, max, since) = self.frame_stats.get();
        let since = since.unwrap_or_else(Instant::now);
        let (n, total, max) = (n + 1, total + took, max.max(took));
        if since.elapsed() >= Duration::from_secs(1) {
            crate::debug!(
                "frames {n}/s, drawing avg {:.2} ms, max {:.2} ms",
                total.as_secs_f64() * 1e3 / f64::from(n),
                max.as_secs_f64() * 1e3
            );
            self.frame_stats.set((0, Duration::ZERO, Duration::ZERO, Some(Instant::now())));
        } else {
            self.frame_stats.set((n, total, max, Some(since)));
        }
    }

    fn draw(&self, w: &TermView, snap: &gtk::Snapshot) {
        let (width, height) = (w.width() as f32, w.height() as f32);
        let ctx = w.pango_context();
        let font = self.font.borrow();
        let (cw, ch) = (font.cell_w, font.cell_h);
        let pad = (self.padding.get() * font.scale).round() / font.scale;
        let mut cache = self.cache.borrow_mut();

        // Under the lock: copy what changed. Shaping happens after it is
        // released, so the reader keeps parsing meanwhile.
        let term = self.session.term();
        let pal_bg = term.palette().bg();
        snap.append_color(&rgba(pal_bg, 1.0), &graphene::Rect::new(0.0, 0.0, width, height));
        if term.modes().synchronized {
            let since = *self.sync_since.get().get_or_insert_with(Instant::now);
            self.sync_since.set(Some(since));
            if since.elapsed() < SYNC_TIMEOUT {
                drop(term);
                if let Some(frame) = self.last_frame.borrow().as_ref() {
                    snap.append_node(frame);
                }
                let weak = self.widget.clone();
                glib::timeout_add_local_once(SYNC_TIMEOUT, move || {
                    if let Some(w) = weak.upgrade() {
                        w.queue_draw();
                    }
                });
                return;
            }
        } else {
            self.sync_since.set(None);
        }
        cache.validate(self.font_gen.get(), term.palette().generation(), term.modes().reverse_video);
        let mut styles = render::Styles::new(&term);
        let lines = term.lines();
        let rows: Vec<(u64, Option<Row>)> = (0..lines)
            .map(|y| {
                let row = term.view_row(y);
                if cache.has(row.id()) {
                    (row.id(), None)
                } else {
                    styles.add_row(&term, row);
                    (row.id(), Some(row.clone()))
                }
            })
            .collect();
        let selection: Vec<(usize, usize, usize)> = match *self.selection.borrow() {
            Some(sel) => {
                let top = term.view_top();
                (0..lines)
                    .filter_map(|y| sel.cols(&term, top + y as u64, term.cols()).map(|(a, b)| (y, a, b)))
                    .collect()
            }
            None => Vec::new(),
        };
        let (cx, cy) = term.cursor();
        let vy = cy + term.display_offset();
        let cursor_row = (term.modes().cursor_visible && vy < lines).then(|| {
            let row = term.view_row(vy).clone();
            styles.add_row(&term, &row);
            row
        });
        // Search matches in view: (row, first col, last col, current).
        let mut found: Vec<(usize, usize, usize, bool)> = Vec::new();
        if let Some((query, current)) = self.search.borrow().as_ref() {
            let top = term.view_top();
            let bottom = top + lines as u64 - 1;
            let cols = term.cols();
            for (start, end) in term.find_all(query, top, bottom) {
                let is_current = *current == Some((start, end));
                for abs in start.row.max(top)..=end.row.min(bottom) {
                    let c0 = if abs == start.row { start.col } else { 0 };
                    let c1 = if abs == end.row { end.col } else { cols - 1 };
                    found.push(((abs - top) as usize, c0, c1, is_current));
                }
            }
        }
        let images = term.images_in_view();
        let shape = term.cursor_shape();
        let (offset, history) = (term.display_offset(), term.history());
        drop(term);

        let pal = &styles.palette;
        let frame = gtk::Snapshot::new();
        frame.translate(&pixel_shift(w, font.scale));
        for (y, (id, row)) in rows.iter().enumerate() {
            let node = cache.get(*id, || row.as_ref().and_then(|r| render::draw_row(&ctx, &font, &styles, r)));
            if let Some(node) = node {
                frame.save();
                frame.translate(&graphene::Point::new(pad, pad + y as f32 * ch));
                frame.append_node(&node);
                frame.restore();
            }
        }
        cache.end_frame();

        // Images, over the text of the cells they cover. Their sizes are in
        // the cell pixels the program was told (rounded cell sizes).
        let (sx, sy) = (cw / cw.round().max(1.0), ch / ch.round().max(1.0));
        let mut textures = self.textures.borrow_mut();
        let mut shown = std::collections::HashSet::new();
        if !images.is_empty() {
            frame.push_clip(&graphene::Rect::new(pad, pad, width - 2.0 * pad, height - 2.0 * pad));
        }
        for (y, p) in &images {
            let img = &p.image;
            shown.insert(img.id);
            let texture = textures.entry(img.id).or_insert_with(|| {
                let bytes = glib::Bytes::from(&img.rgba[..]);
                gdk::MemoryTexture::new(
                    img.width as i32,
                    img.height as i32,
                    gdk::MemoryFormat::R8g8b8a8,
                    &bytes,
                    img.width as usize * 4,
                )
                .upcast()
            });
            let x0 = pad + p.col as f32 * cw + p.offset[0] as f32 * sx;
            let y0 = pad + *y as f32 * ch + p.offset[1] as f32 * sy;
            let (dw, dh) = (p.size[0] as f32 * sx, p.size[1] as f32 * sy);
            // The shown part of the image fills the placement: scale the
            // whole image to match and clip to the part.
            let (kx, ky) = (dw / p.src[2].max(1) as f32, dh / p.src[3].max(1) as f32);
            let whole = graphene::Rect::new(
                x0 - p.src[0] as f32 * kx,
                y0 - p.src[1] as f32 * ky,
                img.width as f32 * kx,
                img.height as f32 * ky,
            );
            frame.push_clip(&graphene::Rect::new(x0, y0, dw, dh));
            frame.append_scaled_texture(texture, gsk::ScalingFilter::Linear, &whole);
            frame.pop();
        }
        if !images.is_empty() {
            frame.pop();
        }
        textures.retain(|id, _| shown.contains(id));
        drop(textures);

        for (y, c0, c1, current) in found {
            let tint = rgba(pal.indexed(3), if current { 0.55 } else { 0.22 });
            let rect = graphene::Rect::new(pad + c0 as f32 * cw, pad + y as f32 * ch, (c1 + 1 - c0) as f32 * cw, ch);
            frame.append_color(&tint, &rect);
        }

        let color = rgba(pal.fg(), 0.28);
        for (y, c0, c1) in selection {
            let rect = graphene::Rect::new(
                pad + c0 as f32 * cw,
                pad + y as f32 * ch,
                (c1 + 1 - c0.min(c1 + 1)) as f32 * cw,
                ch,
            );
            frame.append_color(&color, &rect);
        }

        if let Some(row) = cursor_row {
            let wide = row.cells().get(cx).is_some_and(|c| c.is_wide());
            let w = if wide { 2.0 * cw } else { cw };
            let (x, y) = (pad + cx as f32 * cw, pad + vy as f32 * ch);
            let color = rgba(pal.cursor().unwrap_or(pal.fg()), 1.0);
            if self.focused.get() {
                match shape {
                    CursorShape::Block => {
                        frame.append_color(&color, &graphene::Rect::new(x, y, w, ch));
                        if let Some(node) = render::draw_cell_text(&ctx, &font, &styles, &row, cx, pal.bg()) {
                            frame.save();
                            frame.translate(&graphene::Point::new(x, y));
                            frame.append_node(&node);
                            frame.restore();
                        }
                    }
                    CursorShape::Bar => {
                        frame.append_color(&color, &graphene::Rect::new(x, y, (2.0 * font.line).max(2.0), ch))
                    }
                    CursorShape::Underline => {
                        let t = (2.0 * font.line).max(2.0);
                        frame.append_color(&color, &graphene::Rect::new(x, y + ch - t, w, t));
                    }
                }
            } else {
                let t = 1.0;
                for r in [
                    graphene::Rect::new(x, y, w, t),
                    graphene::Rect::new(x, y + ch - t, w, t),
                    graphene::Rect::new(x, y, t, ch),
                    graphene::Rect::new(x + w - t, y, t, ch),
                ] {
                    frame.append_color(&color, &r);
                }
            }
            let rect = gdk::Rectangle::new(x as i32, y as i32, w as i32, ch as i32);
            self.im.set_cursor_location(&rect);
        }

        // Scroll position while looking at history.
        if offset > 0 {
            let total = (history + lines) as f32;
            let track = height - 2.0 * pad;
            let thumb = (track * lines as f32 / total).max(24.0);
            let top = (history - offset) as f32 / (total - lines as f32).max(1.0) * (track - thumb);
            frame.append_color(&rgba(pal.fg(), 0.35), &graphene::Rect::new(width - 6.0, pad + top, 3.0, thumb));
        }

        if let Some(node) = frame.to_node() {
            snap.append_node(&node);
            *self.last_frame.borrow_mut() = Some(node);
        }
    }

    // ------------------------------------------------------------------
    // Input
    // ------------------------------------------------------------------

    fn install(view: &Rc<Self>, widget: &TermView) {
        let keys = gtk::EventControllerKey::new();
        keys.connect_key_pressed(clone!(
            #[weak]
            view,
            #[upgrade_or]
            glib::Propagation::Proceed,
            move |ctl, keyval, _code, state| view.key_pressed(ctl, keyval, state)
        ));
        widget.add_controller(keys);

        view.im.set_client_widget(Some(widget));
        view.im.connect_commit(clone!(
            #[weak]
            view,
            move |_, text| view.input(text.as_bytes().to_vec())
        ));

        let focus = gtk::EventControllerFocus::new();
        focus.connect_enter(clone!(
            #[weak]
            view,
            move |_| view.set_focused(true)
        ));
        focus.connect_leave(clone!(
            #[weak]
            view,
            move |_| view.set_focused(false)
        ));
        widget.add_controller(focus);

        let click = gtk::GestureClick::new();
        click.set_button(0);
        click.connect_pressed(clone!(
            #[weak]
            view,
            move |g, n, x, y| {
                if let Some(w) = view.widget() {
                    w.grab_focus();
                }
                view.button(g.current_button(), true, n, x, y, g.current_event_state());
            }
        ));
        click.connect_released(clone!(
            #[weak]
            view,
            move |g, n, x, y| view.button(g.current_button(), false, n, x, y, g.current_event_state())
        ));
        widget.add_controller(click);

        let motion = gtk::EventControllerMotion::new();
        motion.connect_motion(clone!(
            #[weak]
            view,
            move |ctl, x, y| view.motion(x, y, ctl.current_event_state())
        ));
        widget.add_controller(motion);

        let scroll = gtk::EventControllerScroll::new(gtk::EventControllerScrollFlags::VERTICAL);
        scroll.connect_scroll(clone!(
            #[weak]
            view,
            #[upgrade_or]
            glib::Propagation::Proceed,
            move |ctl, _dx, dy| {
                let lines = match ctl.unit() {
                    gdk::ScrollUnit::Wheel => dy * WHEEL_LINES,
                    _ => dy / f64::from(view.cell_size().1),
                };
                view.wheel(lines, ctl.current_event_state());
                glib::Propagation::Stop
            }
        ));
        widget.add_controller(scroll);
    }

    fn set_focused(&self, focused: bool) {
        self.focused.set(focused);
        if focused {
            self.im.focus_in();
        } else {
            self.im.focus_out();
        }
        let mut term = self.session.term();
        if term.modes().focus_events {
            self.last_input.set(Some(Instant::now()));
            self.session.write_report(&mut term, encode_focus(focused).to_vec());
        }
        drop(term);
        self.queue_draw();
    }

    /// Searches for `query` from the bottom of the history, or ends the
    /// search when it is `None`. Returns the match position and count.
    pub fn search(&self, query: Option<&str>) -> (Option<usize>, usize) {
        match query {
            None => {
                self.search.replace(None);
                self.queue_draw();
                (None, 0)
            }
            Some(q) => {
                *self.search.borrow_mut() = Some((q.to_string(), None));
                self.search_step(true)
            }
        }
    }

    /// Moves to the next match up (older output) or down, scrolling it into
    /// view. Returns its position among all matches, counted from the top,
    /// and the number of matches.
    pub fn search_step(&self, upwards: bool) -> (Option<usize>, usize) {
        let Some((query, current)) = self.search.borrow().clone() else { return (None, 0) };
        let mut t = self.session.term();
        let bottom = t.view_top() + t.display_offset() as u64 + t.lines() as u64;
        let from = current.map_or(Point::new(bottom, 0), |(s, _)| s);
        let next = t.find(&query, from, upwards);
        let all = t.find_all(&query, t.first_row(), bottom);
        let index = next.and_then(|m| all.iter().position(|a| *a == m));
        if let Some((start, _)) = next {
            // Bring the match into view, near the middle when it was out.
            let top = t.view_top();
            let lines = t.lines() as u64;
            if start.row < top || start.row >= top + lines {
                let live_top = t.view_top() + t.display_offset() as u64;
                let want_top = start.row.saturating_sub(lines / 2);
                let offset = live_top.saturating_sub(want_top) as isize;
                let delta = offset - t.display_offset() as isize;
                t.scroll_display(delta);
            }
        }
        drop(t);
        *self.search.borrow_mut() = Some((query, next));
        self.queue_draw();
        (index, all.len())
    }

    /// Sets the keys window and terminal actions run on.
    pub fn set_shortcuts(&self, shortcuts: Vec<Shortcut>) {
        *self.shortcuts.borrow_mut() = shortcuts;
    }

    /// Reports whether every key goes to the program.
    pub fn passthrough(&self) -> bool {
        self.passthrough.get()
    }

    /// When the user last typed, pasted, or sent a mouse or focus report.
    pub fn last_input(&self) -> Option<Instant> {
        self.last_input.get()
    }

    /// Sends typed input: the view returns to the live screen.
    fn input(&self, bytes: Vec<u8>) {
        if bytes.is_empty() {
            return;
        }
        self.last_input.set(Some(Instant::now()));
        {
            let mut t = self.session.term();
            t.scroll_to_bottom();
        }
        self.selection.replace(None);
        self.session.write(bytes);
        self.queue_draw();
    }

    fn action(&self, name: &str) {
        if let Some(f) = self.on_action.borrow().as_ref() {
            f(name);
        }
    }

    /// Runs a shortcut; false when it does not apply (Ctrl+C with no
    /// selection goes to the program).
    fn shortcut(&self, action: &str) -> bool {
        match action {
            "copy" => {
                if self.selection.borrow().is_none() {
                    return false;
                }
                self.copy();
                self.selection.replace(None);
                self.queue_draw();
            }
            "copy_clipboard" => self.copy(),
            "paste" | "paste_clipboard" => self.paste(false),
            "font_bigger" => self.zoom(1),
            "font_smaller" => self.zoom(-1),
            "font_reset" => self.zoom(0),
            "paste_clipboard_alt" => self.paste(false),
            "scroll_up" | "scroll_down" => {
                let mut t = self.session.term();
                if t.alt_screen() {
                    return false;
                }
                let page = t.lines().saturating_sub(1).max(1) as isize;
                t.scroll_display(if action == "scroll_up" { page } else { -page });
                drop(t);
                self.queue_draw();
            }
            other => self.action(other),
        }
        true
    }

    fn key_pressed(
        &self,
        ctl: &gtk::EventControllerKey,
        keyval: gdk::Key,
        state: gdk::ModifierType,
    ) -> glib::Propagation {
        let actions: Vec<&'static str> =
            self.shortcuts.borrow().iter().filter(|s| s.matches(keyval, state)).map(|s| s.action).collect();
        if actions.contains(&"passthrough") {
            self.passthrough.set(!self.passthrough.get());
            self.action("passthrough");
            return glib::Propagation::Stop;
        }
        // In passthrough, window and terminal shortcuts go to the program.
        for a in actions.into_iter().filter(|_| !self.passthrough.get()) {
            if self.shortcut(a) {
                return glib::Propagation::Stop;
            }
        }
        let m = keys::mods(state);
        if !m.ctrl
            && !m.alt
            && let Some(ev) = ctl.current_event()
            && self.im.filter_keypress(&ev)
        {
            return glib::Propagation::Stop;
        }
        let Some(key) = keys::key(keyval, state) else { return glib::Propagation::Proceed };
        let modes = {
            let t = self.session.term();
            KeyModes { app_cursor: t.modes().app_cursor, app_keypad: t.modes().app_keypad, kitty: t.kitty_flags() }
        };
        let bytes = encode_key(key, m, modes);
        if bytes.is_empty() {
            return glib::Propagation::Proceed;
        }
        self.input(bytes);
        glib::Propagation::Stop
    }

    fn copy(&self) {
        let text = {
            let t = self.session.term();
            self.selection.borrow().map(|s| s.text(&t)).unwrap_or_default()
        };
        if !text.is_empty()
            && let Some(w) = self.widget()
        {
            w.clipboard().set_text(&text);
        }
    }

    /// Pastes the clipboard, or the primary selection.
    pub fn paste(&self, primary: bool) {
        let Some(w) = self.widget() else { return };
        let clip = if primary { w.primary_clipboard() } else { w.clipboard() };
        let weak = self.widget.clone();
        glib::spawn_future_local(async move {
            let text = match clip.read_text_future().await {
                Ok(Some(t)) => t,
                Ok(None) => return,
                Err(e) => {
                    crate::warn!("read clipboard: {e}");
                    return;
                }
            };
            let Some(w) = weak.upgrade() else { return };
            let Some(view) = w.imp().state.get() else { return };
            let bracketed = view.session.term().modes().bracketed_paste;
            view.input(encode_paste(&text, bracketed));
        });
    }

    /// Reports whether mouse events go to the program. Shift keeps them
    /// for selection, as in xterm.
    fn reporting(&self, state: gdk::ModifierType) -> bool {
        !state.contains(gdk::ModifierType::SHIFT_MASK) && self.session.term().modes().mouse != MouseMode::Off
    }

    fn report(&self, button: MouseButton, action: MouseAction, x: f64, y: f64, state: gdk::ModifierType) {
        // Programs redraw on mouse reports; that output is not work.
        self.last_input.set(Some(Instant::now()));
        let (col, row) = self.cell_at(x, y);
        let mut t = self.session.term();
        let (mode, sgr) = (t.modes().mouse, t.modes().mouse_sgr);
        if let Some(bytes) = encode_mouse(mode, sgr, button, action, col, row, keys::mods(state)) {
            self.session.write_report(&mut t, bytes);
        }
    }

    fn held_button(&self) -> MouseButton {
        match self.buttons.get() {
            b if b & 1 != 0 => MouseButton::Left,
            b if b & 2 != 0 => MouseButton::Middle,
            b if b & 4 != 0 => MouseButton::Right,
            _ => MouseButton::None,
        }
    }

    fn button(&self, button: u32, pressed: bool, presses: i32, x: f64, y: f64, state: gdk::ModifierType) {
        let (b, bit) = match button {
            gdk::BUTTON_PRIMARY => (MouseButton::Left, 1),
            gdk::BUTTON_MIDDLE => (MouseButton::Middle, 2),
            gdk::BUTTON_SECONDARY => (MouseButton::Right, 4),
            _ => return,
        };
        self.buttons.set(if pressed { self.buttons.get() | bit } else { self.buttons.get() & !bit });
        self.pointer.set((x, y));
        if self.reporting(state) {
            // The program gets the click, but a selection made with Shift
            // still goes away, as it does when nothing reports the mouse.
            if pressed && b == MouseButton::Left && self.selection.replace(None).is_some() {
                self.queue_draw();
            }
            let action = if pressed { MouseAction::Press } else { MouseAction::Release };
            self.report(b, action, x, y, state);
            return;
        }
        match (b, pressed) {
            (MouseButton::Left, true) if state.contains(gdk::ModifierType::CONTROL_MASK) && self.open_link_at(x, y) => {
            }
            (MouseButton::Left, true) => {
                let (col, row) = self.cell_at(x, y);
                let p = Point::new(self.session.term().view_top() + row as u64, col);
                let by = match presses {
                    2 => SelectBy::Word,
                    n if n >= 3 => SelectBy::Line,
                    _ => SelectBy::Cell,
                };
                let extend = state.contains(gdk::ModifierType::SHIFT_MASK);
                let mut sel = self.selection.borrow_mut();
                match (*sel, extend) {
                    (Some(mut s), true) => {
                        s.head = p;
                        *sel = Some(s);
                    }
                    _ if by == SelectBy::Cell => *sel = None,
                    _ => {
                        let block = state.contains(gdk::ModifierType::CONTROL_MASK | gdk::ModifierType::ALT_MASK);
                        *sel = Some(Selection { anchor: p, head: p, by, block });
                    }
                }
                drop(sel);
                self.selecting.set(true);
                self.queue_draw();
            }
            (MouseButton::Left, false) => {
                self.selecting.set(false);
                self.stop_edge_scroll();
                let text = {
                    let t = self.session.term();
                    self.selection.borrow().map(|s| s.text(&t)).unwrap_or_default()
                };
                if !text.is_empty()
                    && let Some(w) = self.widget()
                {
                    w.primary_clipboard().set_text(&text);
                }
            }
            (MouseButton::Middle, true) => self.paste(true),
            _ => {}
        }
    }

    /// The link under a position: an OSC 8 hyperlink, or a URL in the text.
    fn link_at(&self, x: f64, y: f64) -> Option<String> {
        let (col, row) = self.cell_at(x, y);
        let t = self.session.term();
        let r = t.view_row(row);
        let cell = r.cells().get(col)?;
        let id = t.style(cell.style).link;
        if id != 0 {
            return t.link(id).map(str::to_string);
        }
        let (a, b) = t.word_bounds(Point::new(t.view_top() + row as u64, col));
        let word = t.text(a, b);
        let word = word.trim_end_matches(['.', ',', ';', ':', ')', ']', '\'', '"']);
        (word.starts_with("https://") || word.starts_with("http://")).then(|| word.to_string())
    }

    fn open_link_at(&self, x: f64, y: f64) -> bool {
        let Some(uri) = self.link_at(x, y).filter(|u| safe_link(u)) else { return false };
        let Some(w) = self.widget() else { return false };
        let window = w.root().and_downcast::<gtk::Window>();
        gtk::UriLauncher::new(&uri).launch(window.as_ref(), gtk::gio::Cancellable::NONE, move |res| {
            if let Err(e) = res {
                crate::warn!("open {uri}: {e}");
            }
        });
        true
    }

    fn motion(&self, x: f64, y: f64, state: gdk::ModifierType) {
        self.pointer.set((x, y));
        let cell = self.cell_at(x, y);
        if self.reporting(state) {
            if cell != self.last_cell.get() {
                self.last_cell.set(cell);
                self.report(self.held_button(), MouseAction::Motion, x, y, state);
            }
            return;
        }
        if !self.selecting.get() || self.buttons.get() & 1 == 0 {
            return;
        }
        self.extend_selection(x, y, state);
        if self.rows_outside(y) != 0 {
            self.start_edge_scroll();
        } else {
            self.stop_edge_scroll();
        }
    }

    fn extend_selection(&self, x: f64, y: f64, state: gdk::ModifierType) {
        let (col, row) = self.cell_at(x, y);
        let p = Point::new(self.session.term().view_top() + row as u64, col);
        let mut sel = self.selection.borrow_mut();
        match sel.as_mut() {
            Some(s) => s.head = p,
            None => {
                let block = state.contains(gdk::ModifierType::CONTROL_MASK | gdk::ModifierType::ALT_MASK);
                *sel = Some(Selection { anchor: p, head: p, by: SelectBy::Cell, block });
            }
        }
        drop(sel);
        self.queue_draw();
    }

    /// Scrolls while a selection is dragged above or below the view,
    /// faster the further out the pointer is.
    fn start_edge_scroll(&self) {
        if self.edge_timer.borrow().is_some() {
            return;
        }
        let weak = self.widget.clone();
        let id = glib::timeout_add_local(EDGE_SCROLL_TICK, move || {
            let Some(w) = weak.upgrade() else { return glib::ControlFlow::Break };
            let Some(view) = w.imp().state.get() else { return glib::ControlFlow::Break };
            let (x, y) = view.pointer.get();
            let out = view.rows_outside(y);
            if out == 0 || !view.selecting.get() {
                view.edge_timer.replace(None);
                return glib::ControlFlow::Break;
            }
            let lines = out.signum() * out.abs().min(30);
            view.session.term().scroll_display(-lines as isize);
            view.extend_selection(x, y, gdk::ModifierType::empty());
            glib::ControlFlow::Continue
        });
        *self.edge_timer.borrow_mut() = Some(id);
    }

    fn stop_edge_scroll(&self) {
        if let Some(id) = self.edge_timer.borrow_mut().take() {
            id.remove();
        }
    }

    fn wheel(&self, lines: f64, state: gdk::ModifierType) {
        let acc = self.scroll_acc.get() + lines;
        let whole = acc.trunc();
        self.scroll_acc.set(acc - whole);
        if whole == 0.0 {
            return;
        }
        let n = whole.abs() as usize;
        let up = whole < 0.0;
        let (x, y) = self.pointer.get();
        if self.reporting(state) {
            let b = if up { MouseButton::WheelUp } else { MouseButton::WheelDown };
            for _ in 0..n.div_ceil(WHEEL_LINES as usize) {
                self.report(b, MouseAction::Press, x, y, state);
            }
            return;
        }
        let mut t = self.session.term();
        if t.alt_screen() {
            if t.modes().alternate_scroll {
                let key = if up { bunker_vt::Key::Up } else { bunker_vt::Key::Down };
                let modes = KeyModes { app_cursor: t.modes().app_cursor, app_keypad: false, kitty: t.kitty_flags() };
                let bytes: Vec<u8> = (0..n).flat_map(|_| encode_key(key, Default::default(), modes)).collect();
                drop(t);
                self.session.write(bytes);
            }
            return;
        }
        t.scroll_display(if up { n as isize } else { -(n as isize) });
        drop(t);
        if self.selecting.get() {
            self.extend_selection(x, y, state);
        }
        self.queue_draw();
    }
}

/// Reports whether a link is safe to hand to the desktop: web and mail
/// links, and local files that are not programs.
fn safe_link(uri: &str) -> bool {
    let Some((scheme, rest)) = uri.split_once(':') else { return false };
    match scheme.to_ascii_lowercase().as_str() {
        "http" | "https" => rest.strip_prefix("//").is_some_and(|r| !r.is_empty() && !r.starts_with('/')),
        "mailto" => !rest.is_empty(),
        "file" => {
            let Some(path) = rest.strip_prefix("//").and_then(|r| r.find('/').map(|i| &r[i..])) else { return false };
            let host = &rest[2..rest.len() - path.len()];
            if !host.is_empty() && host != "localhost" {
                return false;
            }
            use std::os::unix::fs::PermissionsExt;
            // Programs and launchers would run, not open.
            if path.ends_with(".desktop") {
                return false;
            }
            std::fs::metadata(path).is_ok_and(|m| m.is_dir() || (m.is_file() && m.permissions().mode() & 0o111 == 0))
        }
        _ => false,
    }
}

/// Device pixels per logical pixel for the widget's surface: fractional
/// (1.25) where the desktop scales by fractions.
fn device_scale(w: &TermView) -> f32 {
    w.native()
        .and_then(|n| n.surface())
        .map(|s| s.scale() as f32)
        .filter(|s| *s > 0.0)
        .unwrap_or(w.scale_factor() as f32)
}

/// The offset that moves the widget's origin onto a whole device pixel.
/// Under fractional scaling a widget can start inside a pixel; drawing the
/// grid from there would put every cell edge inside one too.
fn pixel_shift(w: &TermView, scale: f32) -> graphene::Point {
    let Some(native) = w.native() else { return graphene::Point::zero() };
    let Some(origin) = w.compute_point(&native, &graphene::Point::zero()) else { return graphene::Point::zero() };
    let (tx, ty) = native.surface_transform();
    let snap = |v: f32| ((v * scale).round() - v * scale) / scale;
    graphene::Point::new(snap(origin.x() + tx as f32), snap(origin.y() + ty as f32))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn safe_links() {
        let cases = [
            ("https://example.com/x", true),
            ("http://", false),
            ("mailto:me@x.y", true),
            ("file:///etc/hostname", true),
            ("file:///usr/bin/ls", false),
            ("file:///usr/share/applications/org.gnome.Settings.desktop", false),
            ("file://otherhost/etc/hostname", false),
            ("javascript:alert(1)", false),
            ("ssh://host", false),
        ];
        for (uri, want) in cases {
            assert_eq!(safe_link(uri), want, "{uri}");
        }
    }
}
