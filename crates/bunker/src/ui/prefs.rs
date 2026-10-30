//! The Preferences window: pages of settings, each control writing one key
//! to the config file. The file stays the source of truth: the app watches
//! it and applies every change, so edits made in a text editor show up here
//! too (`load`).

use std::cell::{Cell, RefCell};
use std::path::PathBuf;
use std::rc::Rc;

use gtk::prelude::*;
use gtk::{gio, glib, pango};

use super::keys::{self, Combo};
use super::themes::ThemePicker;
use crate::config::{Config, TabPosition};

const POSITIONS: [(&str, &str, TabPosition); 4] = [
    ("left", "Left", TabPosition::Left),
    ("right", "Right", TabPosition::Right),
    ("top", "Top", TabPosition::Top),
    ("bottom", "Bottom", TabPosition::Bottom),
];

/// [notify] desktop values and their labels.
const DESKTOP: [(&str, &str); 3] =
    [("unfocused", "Window in the background"), ("always", "Always"), ("never", "Never")];

/// [cursor] blink values and their labels.
const BLINK: [(&str, &str); 3] = [("system", "Follow system"), ("on", "On"), ("off", "Off")];

/// The width of a page's column of settings.
const PAGE_WIDTH: i32 = 560;

/// The narrowest and widest sidebar the width control allows.
pub const MIN_WIDTH: f64 = 170.0;
/// See [`MIN_WIDTH`].
pub const MAX_WIDTH: f64 = 560.0;

/// Applies a changed config.
pub type ConfigHandler = Rc<dyn Fn(Config)>;

/// The Preferences window.
pub struct Prefs {
    /// The window.
    pub window: gtk::Window,
    path: Option<PathBuf>,
    updating: Cell<bool>,
    /// The settings as last loaded or changed here.
    config: RefCell<Config>,
    /// Applies a change at once, before it reaches the file.
    on_change: RefCell<Option<ConfigHandler>>,
    status: gtk::Label,
    themes: Rc<ThemePicker>,
    font: gtk::FontDialogButton,
    padding: gtk::SpinButton,
    blink: gtk::DropDown,
    position: gtk::DropDown,
    width: gtk::SpinButton,
    autohide: gtk::Switch,
    scrollback: gtk::SpinButton,
    command_seconds: gtk::SpinButton,
    bell: gtk::Switch,
    desktop: gtk::DropDown,
    /// Keyboard page: a button per action, the action waiting for a key,
    /// and a key waiting for Replace because another action has it.
    key_buttons: Vec<(&'static str, gtk::Button)>,
    recording: Cell<Option<&'static str>>,
    clash: RefCell<Option<(&'static str, Combo, &'static str)>>,
    clash_bar: gtk::Revealer,
    clash_text: gtk::Label,
}

/// The pages: id, title, icon, and a line under the title.
const PAGES: [(&str, &str, &str, &str); 5] = [
    ("appearance", "Appearance", "preferences-desktop-appearance-symbolic", "Colours, font, and spacing"),
    ("tabs", "Tabs", "tab-new-symbolic", "The tab list and how tabs behave"),
    ("terminal", "Terminal", "utilities-terminal-symbolic", "History and alerts"),
    ("keyboard", "Keyboard", "input-keyboard-symbolic", "Shortcuts for windows, tabs, and text"),
    ("advanced", "Advanced", "preferences-other-symbolic", "Where settings are kept"),
];

/// Adds a page to `stack` and returns the column to fill.
fn page(stack: &gtk::Stack, name: &str) -> gtk::Box {
    let (_, title, _, about) = PAGES.iter().find(|p| p.0 == name).copied().unwrap_or_default();
    let column = gtk::Box::new(gtk::Orientation::Vertical, 26);
    column.add_css_class("bunker-prefs-page");
    column.set_halign(gtk::Align::Center);
    column.set_size_request(PAGE_WIDTH, -1);
    let head = gtk::Box::new(gtk::Orientation::Vertical, 4);
    let heading = gtk::Label::new(Some(title));
    heading.add_css_class("bunker-page-title");
    heading.set_xalign(0.0);
    let sub = gtk::Label::new(Some(about));
    sub.add_css_class("bunker-dim");
    sub.set_xalign(0.0);
    head.append(&heading);
    head.append(&sub);
    column.append(&head);
    let scroll = gtk::ScrolledWindow::new();
    scroll.set_policy(gtk::PolicyType::Never, gtk::PolicyType::Automatic);
    scroll.set_child(Some(&column));
    stack.add_named(&scroll, Some(name));
    column
}

/// The page list on the left: an icon and a name per page.
fn sidebar(stack: &gtk::Stack) -> gtk::ListBox {
    let list = gtk::ListBox::new();
    list.add_css_class("navigation-sidebar");
    list.add_css_class("bunker-prefs-sidebar");
    for (name, title, icon, _) in PAGES {
        let line = gtk::Box::new(gtk::Orientation::Horizontal, 12);
        line.append(&gtk::Image::from_icon_name(icon));
        let l = gtk::Label::new(Some(title));
        l.set_xalign(0.0);
        line.append(&l);
        let row = gtk::ListBoxRow::new();
        row.set_child(Some(&line));
        row.set_widget_name(name);
        list.append(&row);
    }
    let stack = stack.clone();
    list.connect_row_selected(move |_, row| {
        if let Some(r) = row {
            stack.set_visible_child_name(&r.widget_name());
        }
    });
    list.select_row(list.row_at_index(0).as_ref());
    list
}

/// Adds a titled card of rows to a page.
fn group(page: &gtk::Box, title: &str, description: &str) -> gtk::ListBox {
    let g = gtk::Box::new(gtk::Orientation::Vertical, 6);
    if !title.is_empty() {
        let l = gtk::Label::new(Some(title));
        l.add_css_class("bunker-group-title");
        l.set_xalign(0.0);
        g.append(&l);
    }
    if !description.is_empty() {
        let d = gtk::Label::new(Some(description));
        d.add_css_class("bunker-dim");
        d.set_xalign(0.0);
        d.set_wrap(true);
        g.append(&d);
    }
    let list = gtk::ListBox::new();
    list.set_selection_mode(gtk::SelectionMode::None);
    list.add_css_class("bunker-card");
    g.append(&list);
    page.append(&g);
    list
}

/// Adds "title / subtitle … control" to a card.
fn row(list: &gtk::ListBox, title: &str, subtitle: &str, control: &impl IsA<gtk::Widget>) {
    let text = gtk::Box::new(gtk::Orientation::Vertical, 2);
    text.set_hexpand(true);
    text.set_valign(gtk::Align::Center);
    let t = gtk::Label::new(Some(title));
    t.set_xalign(0.0);
    text.append(&t);
    if !subtitle.is_empty() {
        let s = gtk::Label::new(Some(subtitle));
        s.add_css_class("bunker-dim");
        s.set_xalign(0.0);
        s.set_wrap(true);
        text.append(&s);
    }
    let line = gtk::Box::new(gtk::Orientation::Horizontal, 24);
    line.add_css_class("bunker-row");
    line.append(&text);
    control.set_valign(gtk::Align::Center);
    // Spin buttons and drop-downs get a common width; a switch keeps its own.
    if !control.is::<gtk::Switch>() {
        control.add_css_class("bunker-control");
    }
    line.append(control);
    let r = gtk::ListBoxRow::new();
    r.set_activatable(false);
    r.set_child(Some(&line));
    list.append(&r);
}

fn spin(lo: f64, hi: f64, step: f64) -> gtk::SpinButton {
    let s = gtk::SpinButton::with_range(lo, hi, step);
    s.set_digits(0);
    s.set_numeric(true);
    s
}

/// Keeps the font chooser to fixed-width families: a proportional font
/// would break the cell grid.
fn monospace_only(item: &glib::Object) -> bool {
    if let Some(family) = item.downcast_ref::<pango::FontFamily>() {
        return family.is_monospace();
    }
    if let Some(face) = item.downcast_ref::<pango::FontFace>() {
        return face.family().is_monospace();
    }
    true
}

impl Prefs {
    /// Builds the window for `parent`.
    pub fn new(parent: &gtk::Window, config: &Config) -> Rc<Self> {
        let window = gtk::Window::new();
        window.set_title(Some("Preferences"));
        window.add_css_class("bunker-settings");
        window.set_transient_for(Some(parent));
        window.set_destroy_with_parent(true);
        window.set_hide_on_close(true);
        window.set_default_size(860, 620);

        let stack = gtk::Stack::new();
        stack.set_hexpand(true);
        stack.set_vexpand(true);
        stack.set_transition_type(gtk::StackTransitionType::Crossfade);

        // Appearance.
        let p = page(&stack, "appearance");
        let g = gtk::Box::new(gtk::Orientation::Vertical, 8);
        let title = gtk::Label::new(Some("Theme"));
        title.add_css_class("bunker-group-title");
        title.set_xalign(0.0);
        let about = gtk::Label::new(Some(
            "Terminal colours; the header bar and tabs follow them. System follows the desktop's light or dark style.",
        ));
        about.add_css_class("bunker-dim");
        about.set_xalign(0.0);
        about.set_wrap(true);
        let themes = ThemePicker::new();
        g.append(&title);
        g.append(&about);
        g.append(&themes.root);
        p.append(&g);
        let g = group(&p, "Text", "");
        let dialog = gtk::FontDialog::new();
        dialog.set_title("Terminal Font");
        dialog.set_modal(true);
        dialog.set_filter(Some(&gtk::CustomFilter::new(monospace_only)));
        let font = gtk::FontDialogButton::new(Some(dialog));
        font.set_level(gtk::FontLevel::Font);
        row(&g, "Font", "Monospace fonts only; Ctrl+Shift+Plus and Minus resize text in a window", &font);
        let padding = spin(0.0, 200.0, 1.0);
        row(&g, "Padding", "Space between the window edge and the text, in pixels", &padding);
        let labels: Vec<&str> = BLINK.iter().map(|(_, l)| *l).collect();
        let blink = gtk::DropDown::from_strings(&labels);
        row(
            &g,
            "Cursor blinking",
            "Follow system blinks when the program asks for it, otherwise as GNOME's Typing setting says",
            &blink,
        );

        // Tabs.
        let p = page(&stack, "tabs");
        let g = group(&p, "Tab List", "");
        let labels: Vec<&str> = POSITIONS.iter().map(|(_, l, _)| *l).collect();
        let position = gtk::DropDown::from_strings(&labels);
        row(&g, "Position", "Left and right show a sidebar, top and bottom a bar. Applies to new windows.", &position);
        let width = spin(MIN_WIDTH, MAX_WIDTH, 10.0);
        row(&g, "Sidebar width", "In pixels; dragging the sidebar's edge sets it too", &width);
        let autohide = gtk::Switch::new();
        row(&g, "Hide with a single tab", "", &autohide);
        let g = group(&p, "Mouse", "");
        for (what, how) in [
            ("Rename tab", "Double-click"),
            ("Close tab", "Middle-click"),
            ("Tab menu (colour, rename, close)", "Right-click"),
            ("Resize the sidebar", "Drag its edge"),
        ] {
            let l = gtk::Label::new(Some(how));
            l.add_css_class("bunker-dim");
            row(&g, what, "", &l);
        }

        // Terminal.
        let p = page(&stack, "terminal");
        let g = group(&p, "Scrollback", "Applies to tabs opened afterwards.");
        let scrollback = spin(100.0, 1_000_000.0, 1000.0);
        row(&g, "Lines", "History kept per terminal", &scrollback);
        let g = group(
            &p,
            "Alerts",
            "A tab you are not looking at lights up when a long command finishes (red if it failed), when a program rings the bell, or when it asks for a notification, as Claude Code can.",
        );
        let command_seconds = spin(0.0, 3600.0, 5.0);
        row(
            &g,
            "Long commands (seconds)",
            "A command counts once it ran this long; 0 turns these alerts off",
            &command_seconds,
        );
        let bell = gtk::Switch::new();
        row(&g, "Bell", "The bell (Ctrl+G) lights the tab", &bell);
        let labels: Vec<&str> = DESKTOP.iter().map(|(_, l)| *l).collect();
        let desktop = gtk::DropDown::from_strings(&labels);
        row(&g, "Desktop notifications", "Clicking one brings the window up on that tab", &desktop);

        // Keyboard.
        let p = page(&stack, "keyboard");
        let clash_text = gtk::Label::new(None);
        clash_text.set_wrap(true);
        clash_text.set_xalign(0.0);
        clash_text.set_hexpand(true);
        let replace = gtk::Button::with_label("Replace");
        replace.add_css_class("suggested-action");
        let cancel = gtk::Button::with_label("Cancel");
        let clash_box = gtk::Box::new(gtk::Orientation::Horizontal, 10);
        clash_box.add_css_class("bunker-clash");
        clash_box.append(&clash_text);
        clash_box.append(&cancel);
        clash_box.append(&replace);
        let clash_bar = gtk::Revealer::new();
        clash_bar.set_child(Some(&clash_box));
        p.append(&clash_bar);
        let g =
            group(&p, "Shortcuts", "Click a shortcut and press the new keys. Backspace turns it off; Escape cancels.");
        let mut key_buttons = Vec::new();
        for (action, _, about) in keys::ACTIONS {
            let b = gtk::Button::new();
            b.add_css_class("bunker-keycap");
            row(&g, about, "", &b);
            key_buttons.push((action, b));
        }

        // Advanced.
        let p = page(&stack, "advanced");
        let g = group(
            &p,
            "Config File",
            "Everything here is stored in this file, shared with the Go version of bunker. Edits saved in any editor apply immediately, and settings changed here keep your comments.",
        );
        let open = gtk::Button::with_label("Open in Editor");
        let path = config.path.clone();
        let shown = path.as_deref().map(crate::procinfo::tilde).unwrap_or_default();
        row(&g, "config.toml", &shown, &open);

        let side = sidebar(&stack);
        let side_scroll = gtk::ScrolledWindow::new();
        side_scroll.set_policy(gtk::PolicyType::Never, gtk::PolicyType::Automatic);
        side_scroll.set_child(Some(&side));
        side_scroll.set_size_request(210, -1);
        side_scroll.add_css_class("bunker-prefs-side");
        let status = gtk::Label::new(None);
        status.add_css_class("bunker-settings-error");
        status.set_wrap(true);
        status.set_xalign(0.0);
        status.set_visible(false);
        let right = gtk::Box::new(gtk::Orientation::Vertical, 0);
        right.append(&status);
        right.append(&stack);
        let body = gtk::Box::new(gtk::Orientation::Horizontal, 0);
        body.append(&side_scroll);
        body.append(&gtk::Separator::new(gtk::Orientation::Vertical));
        body.append(&right);
        window.set_child(Some(&body));

        let prefs = Rc::new(Self {
            window,
            path,
            updating: Cell::new(false),
            config: RefCell::new(config.clone()),
            on_change: RefCell::new(None),
            status,
            themes,
            font,
            padding,
            blink,
            position,
            width,
            autohide,
            scrollback,
            command_seconds,
            bell,
            desktop,
            key_buttons,
            recording: Cell::new(None),
            clash: RefCell::new(None),
            clash_bar,
            clash_text,
        });
        prefs.connect_keys(&replace, &cancel);
        prefs.connect(&open);
        prefs.load(config);
        prefs
    }

    fn connect(self: &Rc<Self>, open: &gtk::Button) {
        let weak = Rc::downgrade(self);
        let on = move |f: &dyn Fn(&Self)| {
            if let Some(p) = weak.upgrade()
                && !p.updating.get()
            {
                f(&p);
            }
        };
        let o = on.clone();
        self.themes.connect_pick(Box::new(move |name| {
            let name = name.to_string();
            o(&|p| p.change(None, "theme", name.as_str().into(), |c| c.theme = name.clone()));
        }));
        let o = on.clone();
        self.font.connect_font_desc_notify(move |b| {
            if let Some(desc) = b.font_desc() {
                o(&|p| p.change(None, "font", desc.to_string().into(), |c| c.font = desc.to_string()));
            }
        });
        let o = on.clone();
        self.padding.connect_value_changed(move |s| {
            let v = i64::from(s.value_as_int());
            o(&|p| p.change(Some("window"), "padding", v.into(), |c| c.padding = v as u32));
        });
        let o = on.clone();
        self.blink.connect_selected_notify(move |d| {
            let i = d.selected() as usize;
            o(&|p| {
                if let Some((name, _)) = BLINK.get(i) {
                    p.change(Some("cursor"), "blink", (*name).into(), |c| c.cursor_blink = (*name).into());
                }
            })
        });
        let o = on.clone();
        self.position.connect_selected_notify(move |d| {
            let i = d.selected() as usize;
            o(&|p| {
                if let Some((name, _, _)) = POSITIONS.get(i) {
                    p.change(Some("tabs"), "position", (*name).into(), |_| {});
                }
            })
        });
        let o = on.clone();
        self.width.connect_value_changed(move |s| {
            let v = i64::from(s.value_as_int());
            o(&|p| p.change(Some("tabs"), "width", v.into(), |c| c.tab_width = v as u32));
        });
        let o = on.clone();
        self.autohide.connect_active_notify(move |s| {
            let v = s.is_active();
            o(&|p| p.change(Some("tabs"), "autohide", v.into(), |c| c.tab_autohide = v));
        });
        let o = on.clone();
        self.scrollback.connect_value_changed(move |s| {
            let v = i64::from(s.value_as_int());
            o(&|p| p.change(None, "scrollback", v.into(), |c| c.scrollback = v as usize));
        });
        let o = on.clone();
        self.command_seconds.connect_value_changed(move |s| {
            let v = i64::from(s.value_as_int());
            o(&|p| p.change(Some("notify"), "command_seconds", v.into(), |c| c.command_seconds = v as u64));
        });
        let o = on.clone();
        self.desktop.connect_selected_notify(move |d| {
            let i = d.selected() as usize;
            o(&|p| {
                if let Some((name, _)) = DESKTOP.get(i) {
                    p.change(Some("notify"), "desktop", (*name).into(), |c| c.notify_desktop = (*name).into());
                }
            })
        });
        let o = on;
        self.bell.connect_active_notify(move |s| {
            let v = s.is_active();
            o(&|p| p.change(Some("notify"), "bell", v.into(), |c| c.notify_bell = v));
        });
        let path = self.path.clone();
        let window = self.window.clone();
        open.connect_clicked(move |_| {
            let Some(path) = &path else { return };
            gtk::FileLauncher::new(Some(&gio::File::for_path(path))).launch(
                Some(&window),
                gio::Cancellable::NONE,
                |r| {
                    if let Err(e) = r {
                        crate::warn!("open config file: {e}");
                    }
                },
            );
        });
    }

    /// The Keyboard page: recording a key, and resolving clashes.
    fn connect_keys(self: &Rc<Self>, replace: &gtk::Button, cancel: &gtk::Button) {
        for (action, button) in &self.key_buttons {
            let weak = Rc::downgrade(self);
            let action = *action;
            button.connect_clicked(move |_| {
                if let Some(p) = weak.upgrade() {
                    p.record(Some(action));
                }
            });
        }
        // Capture phase: a recording sees the key before the focused button
        // takes Space or Enter as a click.
        let keys = gtk::EventControllerKey::new();
        keys.set_propagation_phase(gtk::PropagationPhase::Capture);
        let weak = Rc::downgrade(self);
        keys.connect_key_pressed(move |_, key, _, state| {
            let Some(p) = weak.upgrade() else { return glib::Propagation::Proceed };
            let Some(action) = p.recording.get() else {
                if key == gtk::gdk::Key::Escape {
                    p.window.close();
                    return glib::Propagation::Stop;
                }
                return glib::Propagation::Proceed;
            };
            match key {
                gtk::gdk::Key::Escape => p.record(None),
                gtk::gdk::Key::BackSpace => {
                    p.record(None);
                    p.set_key(action, None);
                }
                _ => {
                    let Some(combo) = Combo::from_press(key, state) else {
                        // A modifier on its own: wait for the key.
                        return glib::Propagation::Stop;
                    };
                    p.record(None);
                    p.offer_key(action, combo);
                }
            }
            glib::Propagation::Stop
        });
        self.window.add_controller(keys);
        let weak = Rc::downgrade(self);
        replace.connect_clicked(move |_| {
            let Some(p) = weak.upgrade() else { return };
            if let Some((action, combo, other)) = p.clash.borrow_mut().take() {
                p.set_key(other, None);
                p.set_key(action, Some(combo));
            }
            p.clash_bar.set_reveal_child(false);
        });
        let weak = Rc::downgrade(self);
        cancel.connect_clicked(move |_| {
            if let Some(p) = weak.upgrade() {
                p.clash.replace(None);
                p.clash_bar.set_reveal_child(false);
            }
        });
    }

    /// Starts waiting for a key for `action`, or stops.
    fn record(&self, action: Option<&'static str>) {
        self.recording.set(action);
        for (a, b) in &self.key_buttons {
            if Some(*a) == action {
                b.set_label("Press keys…");
                b.add_css_class("bunker-recording");
            } else {
                b.remove_css_class("bunker-recording");
            }
        }
        if action.is_none() {
            self.show_keys();
        }
    }

    /// Binds `combo` to `action`, or asks first when another action has it.
    fn offer_key(&self, action: &'static str, combo: Combo) {
        let current = keys::shortcuts(&self.config.borrow().keys);
        let other = current.iter().find(|s| s.action != action && s.combo == Some(combo)).map(|s| s.action);
        match other {
            None => self.set_key(action, Some(combo)),
            Some(other) => {
                let what = |a: &str| keys::ACTIONS.iter().find(|x| x.0 == a).map_or(a.to_string(), |x| x.2.to_string());
                self.clash_text.set_label(&format!(
                    "{} is used by “{}”. Replace it, leaving that without a shortcut?",
                    combo.label(),
                    what(other)
                ));
                *self.clash.borrow_mut() = Some((action, combo, other));
                self.clash_bar.set_reveal_child(true);
            }
        }
    }

    fn set_key(&self, action: &'static str, combo: Option<Combo>) {
        let value = combo.map_or(keys::NONE.to_string(), |c| c.to_config());
        let v = value.clone();
        self.change(Some("keys"), action, value.into(), move |c| {
            c.keys.insert(action.to_string(), v);
        });
        self.show_keys();
    }

    /// Shows each action's key on its button.
    fn show_keys(&self) {
        let current = keys::shortcuts(&self.config.borrow().keys);
        for (action, button) in &self.key_buttons {
            if self.recording.get() == Some(*action) {
                continue;
            }
            match current.iter().find(|s| s.action == *action).and_then(|s| s.combo) {
                Some(c) => {
                    button.set_label(&c.label());
                    button.remove_css_class("bunker-keycap-off");
                }
                None => {
                    button.set_label("Off");
                    button.add_css_class("bunker-keycap-off");
                }
            }
        }
    }

    /// Sets what applies a change at once.
    pub fn set_on_change(&self, f: ConfigHandler) {
        *self.on_change.borrow_mut() = Some(f);
    }

    /// Applies a change at once and saves it to the file. The file watch
    /// then finds nothing new, so there is no wait for the round trip.
    fn change(
        &self,
        table: Option<&'static str>,
        key: &'static str,
        value: toml_edit::Value,
        edit: impl FnOnce(&mut Config),
    ) {
        edit(&mut self.config.borrow_mut());
        let config = self.config.borrow().clone();
        if let Some(f) = self.on_change.borrow().clone() {
            f(config);
        }
        self.write(table, key, value);
    }

    /// Saves one key off the UI thread and shows a failure in the window.
    fn write(&self, table: Option<&'static str>, key: &'static str, value: toml_edit::Value) {
        let Some(path) = self.path.clone() else { return };
        let status = self.status.clone();
        glib::spawn_future_local(async move {
            let result = gio::spawn_blocking(move || crate::config::save(&path, table, key, value)).await;
            match result {
                Ok(Ok(())) => status.set_visible(false),
                Ok(Err(e)) => {
                    crate::warn!("save {key}: {e}");
                    status.set_text(&format!("Could not save: {e}"));
                    status.set_visible(true);
                }
                Err(_) => crate::warn!("save {key}: the writer panicked"),
            }
        });
    }

    /// Shows `config` in the controls without writing anything back.
    pub fn load(&self, config: &Config) {
        *self.config.borrow_mut() = config.clone();
        self.updating.set(true);
        self.themes.set_selected(&config.theme);
        self.font.set_font_desc(&pango::FontDescription::from_string(&config.font));
        self.padding.set_value(f64::from(config.padding));
        if let Some(i) = BLINK.iter().position(|(n, _)| *n == config.cursor_blink) {
            self.blink.set_selected(i as u32);
        }
        if let Some(i) = POSITIONS.iter().position(|(_, _, p)| *p == config.tab_position) {
            self.position.set_selected(i as u32);
        }
        self.width.set_value(f64::from(config.tab_width).clamp(MIN_WIDTH, MAX_WIDTH));
        self.autohide.set_active(config.tab_autohide);
        self.scrollback.set_value(config.scrollback as f64);
        self.command_seconds.set_value(config.command_seconds as f64);
        self.bell.set_active(config.notify_bell);
        if let Some(i) = DESKTOP.iter().position(|(n, _)| *n == config.notify_desktop) {
            self.desktop.set_selected(i as u32);
        }
        self.show_keys();
        self.updating.set(false);
    }
}
