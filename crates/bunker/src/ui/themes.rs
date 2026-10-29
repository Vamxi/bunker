//! The theme picker: cards that preview each theme as a small terminal,
//! nine to a page. Only the shown page's cards exist.

use std::cell::{Cell, RefCell};
use std::f64::consts::PI;
use std::rc::Rc;

use bunker_vt::{Rgb, Theme};
use gtk::prelude::*;

/// Cards per page: three rows of three.
const PER_PAGE: usize = 9;
const COLUMNS: i32 = 3;
/// Preview size in pixels.
const PREVIEW_W: i32 = 164;
const PREVIEW_H: i32 = 90;

/// What a card previews.
#[derive(Clone)]
enum Look {
    One(Theme),
    /// The system theme: dark and light side by side.
    Both(Theme, Theme),
}

struct Entry {
    name: String,
    label: String,
    look: Look,
}

/// Called with a theme's config name when it is picked.
pub type OnPick = Box<dyn Fn(&str)>;

/// The picker widget and its state.
pub struct ThemePicker {
    /// The widget to place.
    pub root: gtk::Box,
    grid: gtk::Grid,
    pager: gtk::Label,
    prev: gtk::Button,
    next: gtk::Button,
    entries: Vec<Entry>,
    page: Cell<usize>,
    selected: RefCell<String>,
    on_pick: RefCell<Option<OnPick>>,
    /// The shown page's cards, by theme name.
    cards: RefCell<Vec<(String, gtk::Button)>>,
}

fn source(cr: &gtk::cairo::Context, c: Rgb, alpha: f64) {
    cr.set_source_rgba(f64::from(c.r) / 255.0, f64::from(c.g) / 255.0, f64::from(c.b) / 255.0, alpha);
}

fn rounded(cr: &gtk::cairo::Context, x: f64, y: f64, w: f64, h: f64, r: f64) {
    cr.new_sub_path();
    cr.arc(x + w - r, y + r, r, -PI / 2.0, 0.0);
    cr.arc(x + w - r, y + h - r, r, 0.0, PI / 2.0);
    cr.arc(x + r, y + h - r, r, PI / 2.0, PI);
    cr.arc(x + r, y + r, r, PI, 1.5 * PI);
    cr.close_path();
}

/// Draws `t` simply: its background, a prompt and two lines of text, and
/// its colours.
fn draw_terminal(cr: &gtk::cairo::Context, t: &Theme, w: f64, h: f64) {
    source(cr, t.bg, 1.0);
    cr.paint().ok();
    let a = &t.ansi;
    let lines: [&[(Rgb, f64)]; 3] = [&[(a[2], 28.0), (a[4], 40.0)], &[(t.fg, 64.0)], &[(t.fg.mix(t.bg, 0.45), 46.0)]];
    for (i, words) in lines.iter().enumerate() {
        let y = 16.0 + i as f64 * 15.0;
        let mut x = 14.0;
        for (c, wd) in words.iter() {
            source(cr, *c, 1.0);
            rounded(cr, x, y, *wd, 7.0, 3.5);
            let _ = cr.fill();
            x += wd + 6.0;
        }
    }
    for (i, c) in [a[1], a[3], a[2], a[6], a[4], a[5]].iter().enumerate() {
        source(cr, *c, 1.0);
        cr.arc(18.0 + i as f64 * 14.0, h - 16.0, 4.5, 0.0, 2.0 * PI);
        let _ = cr.fill();
    }
    let _ = w;
}

fn draw(cr: &gtk::cairo::Context, look: &Look, w: f64, h: f64) {
    rounded(cr, 0.0, 0.0, w, h, 10.0);
    cr.clip();
    match look {
        Look::One(t) => draw_terminal(cr, t, w, h),
        Look::Both(dark, light) => {
            // Each half is a terminal of its own, so both show text.
            draw_terminal(cr, dark, w, h);
            cr.rectangle(w / 2.0, 0.0, w / 2.0, h);
            cr.clip();
            cr.translate(w / 2.0, 0.0);
            draw_terminal(cr, light, w / 2.0, h);
        }
    }
}

/// A theme's name as people read it: "dark-pastel" is "Dark Pastel".
pub fn label(slug: &str) -> String {
    if slug == "system" {
        return "System".into();
    }
    slug.split('-')
        .map(|w| {
            let mut c = w.chars();
            c.next().map(|f| f.to_uppercase().chain(c).collect::<String>()).unwrap_or_default()
        })
        .collect::<Vec<_>>()
        .join(" ")
}

impl ThemePicker {
    /// A picker over the bundled themes: System first, then dark themes,
    /// then light ones.
    pub fn new() -> Rc<Self> {
        let bundled = crate::theme::bundled();
        let mut entries = vec![Entry {
            name: "system".into(),
            label: label("system"),
            look: Look::Both(crate::theme::resolve("system", true), crate::theme::resolve("system", false)),
        }];
        let (dark, light): (Vec<_>, Vec<_>) = bundled.into_iter().partition(|(_, t)| t.bg.is_dark());
        for (name, t) in dark.into_iter().chain(light) {
            entries.push(Entry { label: label(&name), name, look: Look::One(t) });
        }

        let root = gtk::Box::new(gtk::Orientation::Vertical, 12);
        let grid = gtk::Grid::new();
        grid.set_column_spacing(12);
        grid.set_row_spacing(10);
        grid.set_column_homogeneous(true);
        grid.set_halign(gtk::Align::Center);
        root.append(&grid);
        let pager_box = gtk::Box::new(gtk::Orientation::Horizontal, 12);
        pager_box.set_halign(gtk::Align::Center);
        let prev = gtk::Button::from_icon_name("go-previous-symbolic");
        prev.add_css_class("flat");
        prev.add_css_class("circular");
        prev.set_tooltip_text(Some("Previous themes"));
        let next = gtk::Button::from_icon_name("go-next-symbolic");
        next.add_css_class("flat");
        next.add_css_class("circular");
        next.set_tooltip_text(Some("More themes"));
        let pager = gtk::Label::new(None);
        pager.add_css_class("bunker-dim");
        pager_box.append(&prev);
        pager_box.append(&pager);
        pager_box.append(&next);
        root.append(&pager_box);

        let picker = Rc::new(Self {
            root,
            grid,
            pager,
            prev,
            next,
            entries,
            page: Cell::new(0),
            selected: RefCell::new(String::new()),
            on_pick: RefCell::new(None),
            cards: RefCell::new(Vec::new()),
        });
        let weak = Rc::downgrade(&picker);
        picker.prev.connect_clicked(move |_| {
            if let Some(p) = weak.upgrade() {
                p.show_page(p.page.get().saturating_sub(1));
            }
        });
        let weak = Rc::downgrade(&picker);
        picker.next.connect_clicked(move |_| {
            if let Some(p) = weak.upgrade() {
                p.show_page(p.page.get() + 1);
            }
        });
        picker.show_page(0);
        picker
    }

    /// Sets what runs when a card is clicked.
    pub fn connect_pick(&self, f: OnPick) {
        *self.on_pick.borrow_mut() = Some(f);
    }

    fn pages(&self) -> usize {
        self.entries.len().div_ceil(PER_PAGE)
    }

    /// Marks `name` as the current theme, turning to its page if needed.
    pub fn set_selected(self: &Rc<Self>, name: &str) {
        if *self.selected.borrow() == name {
            return;
        }
        *self.selected.borrow_mut() = name.to_string();
        let page = self.entries.iter().position(|e| e.name == name).map_or(self.page.get(), |i| i / PER_PAGE);
        if page == self.page.get() && !self.cards.borrow().is_empty() {
            self.mark();
        } else {
            self.show_page(page);
        }
    }

    /// Rings the selected card on the shown page.
    fn mark(&self) {
        let selected = self.selected.borrow();
        for (name, card) in self.cards.borrow().iter() {
            if *name == *selected {
                card.add_css_class("picked");
            } else {
                card.remove_css_class("picked");
            }
        }
    }

    /// Builds the cards of page `n`, dropping the previous page's.
    fn show_page(self: &Rc<Self>, n: usize) {
        let n = n.min(self.pages().saturating_sub(1));
        self.page.set(n);
        while let Some(child) = self.grid.first_child() {
            self.grid.remove(&child);
        }
        self.cards.borrow_mut().clear();
        let selected = self.selected.borrow().clone();
        for (i, e) in self.entries.iter().skip(n * PER_PAGE).take(PER_PAGE).enumerate() {
            let card = gtk::Button::new();
            card.add_css_class("flat");
            card.add_css_class("bunker-theme-card");
            if e.name == selected {
                card.add_css_class("picked");
            }
            card.set_tooltip_text(Some(&e.label));
            let column = gtk::Box::new(gtk::Orientation::Vertical, 7);
            let preview = gtk::DrawingArea::new();
            preview.add_css_class("bunker-theme-preview");
            preview.set_content_width(PREVIEW_W);
            preview.set_content_height(PREVIEW_H);
            let look = e.look.clone();
            preview.set_draw_func(move |_, cr, w, h| draw(cr, &look, f64::from(w), f64::from(h)));
            let name = gtk::Label::new(Some(&e.label));
            name.set_ellipsize(gtk::pango::EllipsizeMode::End);
            column.append(&preview);
            column.append(&name);
            card.set_child(Some(&column));
            let weak = Rc::downgrade(self);
            let theme = e.name.clone();
            card.connect_clicked(move |_| {
                let Some(p) = weak.upgrade() else { return };
                *p.selected.borrow_mut() = theme.clone();
                p.mark();
                if let Some(f) = p.on_pick.borrow().as_ref() {
                    f(&theme);
                }
            });
            self.grid.attach(&card, i as i32 % COLUMNS, i as i32 / COLUMNS, 1, 1);
            self.cards.borrow_mut().push((e.name.clone(), card));
        }
        self.pager.set_label(&format!("{} / {}", n + 1, self.pages()));
        self.prev.set_sensitive(n > 0);
        self.next.set_sensitive(n + 1 < self.pages());
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn labels() {
        assert_eq!(label("dark-pastel"), "Dark Pastel");
        assert_eq!(label("tango-light"), "Tango Light");
        assert_eq!(label("system"), "System");
    }
}
