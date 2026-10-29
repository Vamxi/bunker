//! The circle at the start of a tab row. Its ring is the tab's colour tag;
//! inside it shows what the tab is doing, an agent's state above all.
//! Neighbouring tabs with the same colour are joined by a line from circle
//! to circle.

use std::cell::{Cell, RefCell};
use std::f64::consts::PI;
use std::rc::Rc;

use bunker_vt::{Rgb, Theme};
use gtk::glib;
use gtk::prelude::*;

/// What a tab is doing, as the circle shows it.
#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]
pub enum Status {
    /// A shell waiting at its prompt.
    #[default]
    Idle,
    /// A command other than an agent is running.
    Running,
    /// An agent is producing output.
    Working,
    /// An agent is waiting for input.
    Ready,
    /// Something rang or sent a notification while out of view.
    NeedsYou,
    /// A long command finished while out of view.
    Done,
    /// A long command failed while out of view.
    Failed,
}

impl Status {
    fn animated(self) -> bool {
        matches!(self, Self::Working | Self::NeedsYou)
    }
}

/// The colours the circle uses.
#[derive(Clone, Copy, Debug, Default)]
struct Colors {
    muted: Rgb,
    accent: Rgb,
    ok: Rgb,
    bad: Rgb,
    warn: Rgb,
}

#[derive(Default)]
struct State {
    status: Status,
    tag: Option<Rgb>,
    up: bool,
    down: bool,
    colors: Colors,
}

/// Ring radius in pixels.
const RADIUS: f64 = 7.0;
/// Ring and line width.
const LINE: f64 = 2.0;
/// One spinner turn, in seconds.
const TURN: f64 = 1.1;

/// The circle widget.
pub struct Indicator {
    /// The widget to put in the row.
    pub area: gtk::DrawingArea,
    state: Rc<RefCell<State>>,
    tick: RefCell<Option<gtk::TickCallbackId>>,
    animating: Cell<bool>,
}

fn source(cr: &gtk::cairo::Context, c: Rgb, alpha: f64) {
    cr.set_source_rgba(f64::from(c.r) / 255.0, f64::from(c.g) / 255.0, f64::from(c.b) / 255.0, alpha);
}

impl Indicator {
    /// A circle coloured from `theme`.
    pub fn new(theme: &Theme) -> Self {
        let area = gtk::DrawingArea::new();
        area.set_content_width(28);
        area.set_vexpand(true);
        area.set_valign(gtk::Align::Fill);
        area.add_css_class("bunker-tab-circle");
        let state = Rc::new(RefCell::new(State::default()));
        let s = state.clone();
        area.set_draw_func(move |_, cr, w, h| draw(&s.borrow(), cr, f64::from(w), f64::from(h)));
        let ind = Self { area, state, tick: RefCell::new(None), animating: Cell::new(false) };
        ind.set_theme(theme);
        ind
    }

    /// Recolours the circle for `theme`.
    pub fn set_theme(&self, theme: &Theme) {
        self.state.borrow_mut().colors = Colors {
            muted: theme.fg.mix(theme.bg, 0.45),
            accent: theme.ansi[4],
            ok: theme.ansi[2],
            bad: theme.ansi[1],
            warn: theme.ansi[3],
        };
        self.area.queue_draw();
    }

    /// Shows `status` in a ring of colour `tag`, joined to the circles
    /// above and below when `up` and `down` are set.
    pub fn set(&self, status: Status, tag: Option<Rgb>, up: bool, down: bool) {
        {
            let mut s = self.state.borrow_mut();
            if (s.status, s.tag, s.up, s.down) == (status, tag, up, down) {
                return;
            }
            s.status = status;
            s.tag = tag;
            s.up = up;
            s.down = down;
        }
        let animate = status.animated();
        if animate != self.animating.get() {
            self.animating.set(animate);
            if animate {
                let id = self.area.add_tick_callback(|w, _| {
                    w.queue_draw();
                    glib::ControlFlow::Continue
                });
                *self.tick.borrow_mut() = Some(id);
            } else if let Some(id) = self.tick.borrow_mut().take() {
                id.remove();
            }
        }
        self.area.queue_draw();
    }
}

fn draw(s: &State, cr: &gtk::cairo::Context, w: f64, h: f64) {
    let (cx, cy) = ((w / 2.0).floor() + 0.5, (h / 2.0).floor());
    let c = s.colors;
    let ring = s.tag.unwrap_or(c.muted);
    cr.set_line_width(LINE);
    cr.set_line_cap(gtk::cairo::LineCap::Round);

    // Lines to the neighbours' circles.
    if s.tag.is_some() && (s.up || s.down) {
        source(cr, ring, 0.9);
        if s.up {
            cr.move_to(cx, 0.0);
            cr.line_to(cx, cy - RADIUS - LINE);
        }
        if s.down {
            cr.move_to(cx, cy + RADIUS + LINE);
            cr.line_to(cx, h);
        }
        let _ = cr.stroke();
    }

    // The ring: solid for a tag, faint otherwise.
    source(cr, ring, if s.tag.is_some() { 1.0 } else { 0.55 });
    cr.arc(cx, cy, RADIUS, 0.0, 2.0 * PI);
    let _ = cr.stroke();

    let now = glib::monotonic_time() as f64 / 1e6;
    let dot = |color: Rgb, r: f64, alpha: f64| {
        source(cr, color, alpha);
        cr.arc(cx, cy, r, 0.0, 2.0 * PI);
        let _ = cr.fill();
    };
    match s.status {
        Status::Idle => {}
        Status::Running => dot(c.muted, 2.0, 0.9),
        Status::Working => {
            // A spinning arc inside the ring.
            let start = (now / TURN).fract() * 2.0 * PI;
            source(cr, s.tag.unwrap_or(c.accent), 1.0);
            cr.arc(cx, cy, RADIUS - 3.5, start, start + 1.4 * PI);
            let _ = cr.stroke();
        }
        Status::Ready => dot(s.tag.unwrap_or(c.accent), 3.5, 1.0),
        Status::NeedsYou => {
            let pulse = 0.5 + 0.5 * (now * 2.0 * PI / 1.4).sin();
            dot(c.warn, 3.0 + 1.5 * pulse, 0.7 + 0.3 * pulse);
        }
        Status::Done => dot(c.ok, 3.5, 1.0),
        Status::Failed => dot(c.bad, 3.5, 1.0),
    }
}
