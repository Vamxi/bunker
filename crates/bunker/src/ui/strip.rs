//! The tab list's layout and dragging: rows sit one after another, a
//! dragged row follows the pointer, and the others slide out of its way
//! (as in Ptyxis) rather than jumping. Rows keep their order in the widget
//! tree; the layout only places them.

use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::rc::Rc;

use gtk::glib;
use gtk::prelude::*;
use gtk::subclass::prelude::*;
use gtk::{graphene, gsk};

/// How far the pointer must move before a press becomes a drag.
const DRAG_THRESHOLD: f64 = 8.0;
/// How quickly sliding rows settle: the fraction of the distance left
/// after a second is e^(-1/τ), with τ this many seconds.
const SLIDE_TIME: f64 = 0.06;

#[derive(Default)]
pub struct State {
    vertical: bool,
    spacing: i32,
    /// Where each row is drawn now and where it is going, by widget.
    shown: HashMap<usize, f64>,
    target: HashMap<usize, f64>,
    /// The dragged row and its position along the strip.
    dragged: Option<(usize, f64)>,
    last_tick: Option<i64>,
    ticking: bool,
}

fn key(w: &gtk::Widget) -> usize {
    w.as_ptr() as usize
}

mod imp {
    use super::*;

    #[derive(Default)]
    pub struct StripLayout {
        pub state: RefCell<State>,
    }

    #[glib::object_subclass]
    impl ObjectSubclass for StripLayout {
        const NAME: &'static str = "BunkerStripLayout";
        type Type = super::StripLayout;
        type ParentType = gtk::LayoutManager;
    }

    impl ObjectImpl for StripLayout {}

    impl LayoutManagerImpl for StripLayout {
        fn measure(&self, widget: &gtk::Widget, orientation: gtk::Orientation, for_size: i32) -> (i32, i32, i32, i32) {
            let st = self.state.borrow();
            let along = (orientation == gtk::Orientation::Vertical) == st.vertical;
            let (mut min, mut nat, mut n) = (0, 0, 0);
            let mut child = widget.first_child();
            while let Some(c) = child {
                if c.is_visible() {
                    let (cmin, cnat, _, _) = c.measure(orientation, if along { -1 } else { for_size });
                    if along {
                        min += cmin;
                        nat += cnat;
                        n += 1;
                    } else {
                        min = min.max(cmin);
                        nat = nat.max(cnat);
                    }
                }
                child = c.next_sibling();
            }
            if along && n > 1 {
                min += st.spacing * (n - 1);
                nat += st.spacing * (n - 1);
            }
            (min, nat, -1, -1)
        }

        fn allocate(&self, widget: &gtk::Widget, width: i32, height: i32, _baseline: i32) {
            let mut st = self.state.borrow_mut();
            let vertical = st.vertical;
            let mut pos = 0;
            let mut moving = false;
            let mut child = widget.first_child();
            while let Some(c) = child {
                child = c.next_sibling();
                if !c.is_visible() {
                    continue;
                }
                let size = if vertical {
                    c.measure(gtk::Orientation::Vertical, width).1
                } else {
                    c.measure(gtk::Orientation::Horizontal, height).1
                };
                let k = key(&c);
                let slot = f64::from(pos);
                pos += size + st.spacing;
                let target = match st.dragged {
                    Some((d, at)) if d == k => at,
                    _ => slot,
                };
                st.target.insert(k, target);
                let shown = *st.shown.entry(k).or_insert(target);
                let shown = if st.dragged.is_some_and(|(d, _)| d == k) { target } else { shown };
                st.shown.insert(k, shown);
                if (shown - target).abs() > 0.5 {
                    moving = true;
                }
                let at = shown.round() as f32;
                let t = gsk::Transform::new().translate(&if vertical {
                    graphene::Point::new(0.0, at)
                } else {
                    graphene::Point::new(at, 0.0)
                });
                if vertical {
                    c.allocate(width, size, -1, Some(t));
                } else {
                    c.allocate(size, height, -1, Some(t));
                }
            }
            if moving && !st.ticking {
                st.ticking = true;
                st.last_tick = None;
                drop(st);
                let layout = self.obj().clone();
                widget.add_tick_callback(move |w, clock| layout.slide(w, clock.frame_time()));
            }
        }
    }
}

glib::wrapper! {
    /// The tab list's layout manager.
    pub struct StripLayout(ObjectSubclass<imp::StripLayout>) @extends gtk::LayoutManager;
}

impl StripLayout {
    /// A layout along `vertical` or horizontal, with `spacing` between rows.
    pub fn new(vertical: bool, spacing: i32) -> Self {
        let l: Self = glib::Object::new();
        {
            let mut st = l.imp().state.borrow_mut();
            st.vertical = vertical;
            st.spacing = spacing;
        }
        l
    }

    /// Moves every sliding row a step towards its place; stops when all
    /// have arrived.
    fn slide(&self, widget: &gtk::Widget, now: i64) -> glib::ControlFlow {
        let mut st = self.imp().state.borrow_mut();
        let dt = st.last_tick.map_or(1.0 / 60.0, |t| (now - t) as f64 / 1e6);
        st.last_tick = Some(now);
        let k = 1.0 - (-dt / SLIDE_TIME).exp();
        let mut moving = false;
        let targets: Vec<(usize, f64)> = st.target.iter().map(|(a, b)| (*a, *b)).collect();
        for (w, target) in targets {
            let shown = st.shown.entry(w).or_insert(target);
            *shown += (target - *shown) * k;
            if (target - *shown).abs() > 0.5 {
                moving = true;
            } else {
                *shown = target;
            }
        }
        if !moving && st.dragged.is_none() {
            st.ticking = false;
        }
        let ticking = st.ticking;
        drop(st);
        widget.queue_allocate();
        if ticking { glib::ControlFlow::Continue } else { glib::ControlFlow::Break }
    }
}

/// The child of `list` at `along` (pixels along the strip), by where rows
/// are drawn now.
fn row_at(list: &gtk::Box, layout: &StripLayout, along: f64) -> Option<(gtk::Widget, f64)> {
    let st = layout.imp().state.borrow();
    let mut child = list.first_child();
    while let Some(c) = child {
        let start = st.shown.get(&key(&c)).copied().unwrap_or(0.0);
        let size = f64::from(if st.vertical { c.height() } else { c.width() });
        if c.is_visible() && along >= start && along < start + size {
            return Some((c, start));
        }
        child = c.next_sibling();
    }
    None
}

/// Lets rows of `list` be dragged to a new place. `on_moved` runs after a
/// row moves in the widget tree, so the owner can follow the new order.
pub fn install_drag(list: &gtk::Box, layout: &StripLayout, on_moved: Rc<dyn Fn()>) {
    let drag = gtk::GestureDrag::new();
    drag.set_button(gtk::gdk::BUTTON_PRIMARY);
    // The pressed row, where it was drawn, and whether the drag started.
    let pressed: Rc<RefCell<Option<(gtk::Widget, f64)>>> = Rc::default();
    let started = Rc::new(Cell::new(false));
    let along = |layout: &StripLayout, x: f64, y: f64| if layout.imp().state.borrow().vertical { y } else { x };

    drag.connect_drag_begin(glib::clone!(
        #[weak]
        list,
        #[weak]
        layout,
        #[strong]
        pressed,
        #[strong]
        started,
        move |_, x, y| {
            started.set(false);
            *pressed.borrow_mut() = row_at(&list, &layout, along(&layout, x, y));
        }
    ));
    drag.connect_drag_update(glib::clone!(
        #[weak]
        list,
        #[weak]
        layout,
        #[strong]
        pressed,
        #[strong]
        started,
        #[strong]
        on_moved,
        move |g, dx, dy| {
            let Some((row, start)) = pressed.borrow().clone() else { return };
            let vertical = layout.imp().state.borrow().vertical;
            let delta = if vertical { dy } else { dx };
            if !started.get() {
                if dx.hypot(dy) < DRAG_THRESHOLD {
                    return;
                }
                started.set(true);
                g.set_state(gtk::EventSequenceState::Claimed);
                list.set_cursor_from_name(Some("grabbing"));
                row.add_css_class("dragging");
            }
            let size = f64::from(if vertical { row.height() } else { row.width() });
            let total = f64::from(if vertical { list.height() } else { list.width() });
            let at = (start + delta).clamp(0.0, (total - size).max(0.0));
            layout.imp().state.borrow_mut().dragged = Some((key(&row), at));
            // Move the row past a neighbour once its middle crosses the
            // neighbour's middle; the neighbour slides into the gap.
            let middle = at + size / 2.0;
            let mid_of = |w: &gtk::Widget| {
                let st = layout.imp().state.borrow();
                let s = st.target.get(&key(w)).copied().unwrap_or(0.0);
                s + f64::from(if vertical { w.height() } else { w.width() }) / 2.0
            };
            let mut moved = false;
            while let Some(prev) = row.prev_sibling() {
                if middle >= mid_of(&prev) {
                    break;
                }
                list.reorder_child_after(&row, prev.prev_sibling().as_ref());
                moved = true;
            }
            while let Some(next) = row.next_sibling() {
                if middle <= mid_of(&next) {
                    break;
                }
                list.reorder_child_after(&row, Some(&next));
                moved = true;
            }
            list.queue_allocate();
            if moved {
                on_moved();
            }
        }
    ));
    drag.connect_drag_end(glib::clone!(
        #[weak]
        list,
        #[weak]
        layout,
        #[strong]
        pressed,
        move |_, _, _| {
            if let Some((row, _)) = pressed.borrow_mut().take() {
                row.remove_css_class("dragging");
            }
            list.set_cursor(None);
            // The row settles into its place from where it was dropped.
            layout.imp().state.borrow_mut().dragged = None;
            list.queue_allocate();
        }
    ));
    list.add_controller(drag);
}
