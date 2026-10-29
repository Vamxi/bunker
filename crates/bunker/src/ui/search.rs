//! The search bar over a terminal (Ctrl+F). Enter and the up arrow find
//! older matches, Shift+Enter and the down arrow newer ones; Escape closes.

use std::rc::Rc;

use gtk::prelude::*;
use gtk::{gdk, glib};

use super::view::{TermView, View};

/// A search bar for one terminal.
pub struct SearchBar {
    /// The bar, laid over the terminal.
    pub root: gtk::Box,
    entry: gtk::SearchEntry,
    count: gtk::Label,
    view: Rc<View>,
    widget: TermView,
}

impl SearchBar {
    /// A hidden bar searching `view`.
    pub fn new(view: Rc<View>, widget: TermView) -> Rc<Self> {
        let root = gtk::Box::new(gtk::Orientation::Horizontal, 4);
        root.add_css_class("bunker-search");
        root.set_halign(gtk::Align::End);
        root.set_valign(gtk::Align::Start);
        root.set_visible(false);
        let entry = gtk::SearchEntry::new();
        entry.set_placeholder_text(Some("Find"));
        entry.set_width_chars(22);
        let count = gtk::Label::new(None);
        count.add_css_class("bunker-dim");
        count.set_width_chars(9);
        count.set_xalign(1.0);
        let button = |icon: &str, tip: &str| {
            let b = gtk::Button::from_icon_name(icon);
            b.add_css_class("flat");
            b.set_tooltip_text(Some(tip));
            b
        };
        let up = button("go-up-symbolic", "Older match (Enter)");
        let down = button("go-down-symbolic", "Newer match (Shift+Enter)");
        let close = button("window-close-symbolic", "Close (Escape)");
        root.append(&entry);
        root.append(&count);
        root.append(&up);
        root.append(&down);
        root.append(&close);
        let bar = Rc::new(Self { root, entry, count, view, widget });

        let weak = Rc::downgrade(&bar);
        bar.entry.connect_search_changed(move |e| {
            if let Some(b) = weak.upgrade() {
                let text = e.text();
                let found = b.view.search((!text.is_empty()).then_some(text.as_str()));
                b.show_count(found, !text.is_empty());
            }
        });
        let step = |bar: &Rc<Self>, upwards: bool| {
            let weak = Rc::downgrade(bar);
            move || {
                if let Some(b) = weak.upgrade() {
                    let found = b.view.search_step(upwards);
                    b.show_count(found, true);
                }
            }
        };
        let older = step(&bar, true);
        let newer = step(&bar, false);
        let (o, n) = (older.clone(), newer.clone());
        bar.entry.connect_activate(move |_| o());
        let o = older.clone();
        up.connect_clicked(move |_| o());
        let n2 = newer.clone();
        down.connect_clicked(move |_| n2());
        let weak = Rc::downgrade(&bar);
        close.connect_clicked(move |_| {
            if let Some(b) = weak.upgrade() {
                b.close();
            }
        });
        let weak = Rc::downgrade(&bar);
        bar.entry.connect_stop_search(move |_| {
            if let Some(b) = weak.upgrade() {
                b.close();
            }
        });
        // Capture phase: before the entry handles Enter or the arrows.
        let keys = gtk::EventControllerKey::new();
        keys.set_propagation_phase(gtk::PropagationPhase::Capture);
        let o = older;
        keys.connect_key_pressed(move |_, key, _, state| {
            let shift = state.contains(gdk::ModifierType::SHIFT_MASK);
            let ctrl = state.contains(gdk::ModifierType::CONTROL_MASK);
            match key {
                gdk::Key::Return | gdk::Key::KP_Enter if shift => n(),
                gdk::Key::Up => o(),
                gdk::Key::Down => n(),
                gdk::Key::n if ctrl => o(),
                gdk::Key::p if ctrl => n(),
                _ => return glib::Propagation::Proceed,
            }
            glib::Propagation::Stop
        });
        bar.entry.add_controller(keys);
        bar
    }

    fn show_count(&self, (index, total): (Option<usize>, usize), searching: bool) {
        let text = match (index, total) {
            _ if !searching => String::new(),
            (_, 0) => "No matches".into(),
            (Some(i), n) => format!("{} of {n}", i + 1),
            (None, n) => format!("{n} found"),
        };
        self.count.set_label(&text);
    }

    /// Shows the bar with the query selected, ready to type over.
    pub fn open(&self) {
        self.root.set_visible(true);
        self.entry.grab_focus();
        self.entry.select_region(0, -1);
        if !self.entry.text().is_empty() {
            let found = self.view.search(Some(&self.entry.text()));
            self.show_count(found, true);
        }
    }

    /// Hides the bar, clears the highlights, and returns to the terminal.
    pub fn close(&self) {
        self.root.set_visible(false);
        self.view.search(None);
        self.widget.grab_focus();
    }
}
