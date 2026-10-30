//! The window: a tab list and the active tab's terminal. Each tab row shows
//! what the tab is doing: a circle with the tab's colour and its state (an
//! agent working or waiting, a command running or finished, a bell), the
//! title, directory, git branch, SSH host, and progress. Tabs can be
//! renamed, and the sidebar resized.

use std::cell::{Cell, RefCell};
use std::collections::HashMap;
use std::path::PathBuf;
use std::rc::{Rc, Weak};
use std::sync::Arc;
use std::sync::atomic::{AtomicBool, Ordering};
use std::time::{Duration, Instant};

use bunker_vt::{Event, Theme};
use gtk::glib::{self, clone};
use gtk::prelude::*;
use gtk::{gdk, gio, graphene, pango};

use super::indicator::{Indicator, Status};
use super::prefs::{self, Prefs};
use super::search::SearchBar;
use super::view::{TermView, View};
use crate::config::{Config, TabPosition};
use crate::procinfo::{self, ProcInfo};
use crate::session::{Notify, Session, Wake};

/// Why a tab wants attention.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
enum Attention {
    Bell,
    Notify,
    Done,
    Failed,
}

struct TabRow {
    root: gtk::Box,
    circle: Indicator,
    text: gtk::Box,
    title: gtk::Label,
    detail: gtk::Label,
    branch: gtk::Label,
    progress: gtk::ProgressBar,
}

struct Tab {
    id: u64,
    view: Rc<View>,
    widget: TermView,
    /// The terminal with the search bar laid over it; the stack's page.
    page: gtk::Overlay,
    search: Rc<SearchBar>,
    /// Shown while keyboard passthrough is on.
    pass_badge: gtk::Label,
    row: TabRow,
    info: RefCell<ProcInfo>,
    attention: Cell<Option<Attention>>,
    /// The tag colour's name, if tagged.
    colour: Cell<Option<&'static str>>,
    /// When the running command started, for "finished" alerts.
    running_since: Cell<Option<Instant>>,
    /// When output last arrived, to tell a working agent from a waiting one.
    last_output: Cell<Option<Instant>>,
    /// A name the user gave the tab; it replaces the title.
    custom_title: RefCell<Option<String>>,
    /// The entry while the tab is being renamed.
    rename: RefCell<Option<gtk::Entry>>,
    /// Whether the program's title says it is busy, for agents that show it.
    title_busy: Cell<Option<bool>>,
    /// When the tab last sent a desktop notification still showing.
    notified: Cell<Option<Instant>>,
}

/// One bunker window.
pub struct Window {
    window: gtk::ApplicationWindow,
    stack: gtk::Stack,
    list: gtk::Box,
    tabs_box: gtk::Box,
    tab_width: Cell<i32>,
    /// The window still follows the terminal's cell size, while it opens.
    fitting: Cell<bool>,
    title: gtk::Label,
    tabs: RefCell<Vec<Rc<Tab>>>,
    active: Cell<Option<u64>>,
    config: RefCell<Config>,
    theme: RefCell<Theme>,
    font: RefCell<pango::FontDescription>,
    prefs: RefCell<Option<Rc<Prefs>>>,
    /// Takes a changed config from Preferences to the whole app.
    on_config: RefCell<Option<prefs::ConfigHandler>>,
    me: RefCell<Weak<Window>>,
}

/// The terminal size a new window opens with, as in the Go version.
const DEFAULT_COLS: f32 = 100.0;
const DEFAULT_ROWS: f32 = 30.0;

/// The height of a tab bar above or below the terminal, when it cannot be
/// measured before the window is shown.
const TAB_BAR_HEIGHT: i32 = 44;

/// The narrowest and widest the sidebar can be dragged.
const MIN_TAB_WIDTH: i32 = prefs::MIN_WIDTH as i32;
const MAX_TAB_WIDTH: i32 = prefs::MAX_WIDTH as i32;

/// How long after its last output an agent counts as working.
const WORKING_FOR: Duration = Duration::from_millis(1500);

/// Output this soon after a key press is the echo of typing, not work.
const ECHO_WITHIN: Duration = Duration::from_millis(400);

/// How often tab states are brought up to date.
const STATUS_TICK: Duration = Duration::from_millis(400);

/// The least time between two desktop notifications from one tab.
const NOTIFY_INTERVAL: Duration = Duration::from_secs(3);

/// The shortest time between two UI updates for one tab's output.
const OUTPUT_COALESCE: std::time::Duration = std::time::Duration::from_millis(8);

/// How long after it is shown a window keeps fitting itself to the cell
/// size: long enough for the display's scale to arrive.
const FIT_PERIOD: std::time::Duration = std::time::Duration::from_secs(1);

/// A tab and the window it is in.
type TabRef = (Weak<Window>, Weak<Tab>);

thread_local! {
    /// Tabs by id, so session callbacks can find theirs on the UI thread.
    static TABS: RefCell<HashMap<u64, TabRef>> = RefCell::new(HashMap::new());
    static NEXT_ID: Cell<u64> = const { Cell::new(1) };
}

fn on_wake(id: u64, wake: Wake) {
    let found = TABS.with(|t| t.borrow().get(&id).cloned());
    let Some((win, tab)) = found else { return };
    let (Some(win), Some(tab)) = (win.upgrade(), tab.upgrade()) else { return };
    match wake {
        Wake::Output => win.output(&tab),
        Wake::Info(info) => {
            *tab.info.borrow_mut() = info;
            win.refresh_row(&tab);
        }
        Wake::Exited => win.close_tab(id),
    }
}

impl Window {
    /// Opens a window with one tab.
    pub fn open(app: &gtk::Application, config: Config, theme: Theme, command: &[String]) -> Rc<Self> {
        let window = gtk::ApplicationWindow::new(app);
        window.add_css_class("bunker-window");
        window.set_icon_name(Some(super::APP_ID));
        super::style::apply(&theme);

        let header = gtk::HeaderBar::new();
        let title = gtk::Label::new(Some("bunker"));
        title.set_ellipsize(pango::EllipsizeMode::End);
        title.add_css_class("title");
        header.set_title_widget(Some(&title));
        let vertical = matches!(config.tab_position, TabPosition::Left | TabPosition::Right);
        let new_tab = gtk::Button::from_icon_name("tab-new-symbolic");
        new_tab.set_tooltip_text(Some("New tab (Ctrl+Shift+T)"));
        header.pack_start(&new_tab);
        let menu = gio::Menu::new();
        let item = |label: &str, action: &str, accel: Option<&str>| {
            let item = gio::MenuItem::new(Some(label), Some(action));
            // Shown beside the label; the keys themselves are handled by
            // the terminal view, so they are not registered as accelerators.
            if let Some(a) = accel {
                item.set_attribute_value("accel", Some(&a.to_variant()));
            }
            item
        };
        let section = gio::Menu::new();
        section.append_item(&item("New Tab", "win.new-tab", Some("<Control><Shift>t")));
        section.append_item(&item("New Window", "win.new-window", None));
        menu.append_section(None, &section);
        let section = gio::Menu::new();
        section.append_item(&item("Preferences", "win.preferences", Some("<Control>comma")));
        menu.append_section(None, &section);
        let section = gio::Menu::new();
        section.append_item(&item("About bunker", "win.about", None));
        menu.append_section(None, &section);
        let menu_button = gtk::MenuButton::new();
        menu_button.set_icon_name("open-menu-symbolic");
        menu_button.set_menu_model(Some(&menu));
        menu_button.set_tooltip_text(Some("Main menu"));
        header.pack_end(&menu_button);
        window.set_titlebar(Some(&header));

        let list = gtk::Box::new(
            if vertical { gtk::Orientation::Vertical } else { gtk::Orientation::Horizontal },
            if vertical { 0 } else { 2 },
        );
        list.add_css_class("bunker-tab-list");
        let strip = super::strip::StripLayout::new(vertical, if vertical { 0 } else { 2 });
        list.set_layout_manager(Some(strip.clone()));
        let scroller = gtk::ScrolledWindow::new();
        scroller.set_child(Some(&list));
        let tabs_box = gtk::Box::new(gtk::Orientation::Vertical, 0);
        tabs_box.add_css_class("bunker-tabs");
        tabs_box.append(&scroller);
        if vertical {
            scroller.set_policy(gtk::PolicyType::Never, gtk::PolicyType::Automatic);
            scroller.set_vexpand(true);
            // The list is exactly as wide as configured. Set explicitly, it
            // does not take expansion from its rows, which would share the
            // window's extra width with the terminal.
            scroller.set_propagate_natural_width(false);
            tabs_box.set_width_request((config.tab_width as i32).clamp(MIN_TAB_WIDTH, MAX_TAB_WIDTH));
            tabs_box.set_hexpand(false);
        } else {
            scroller.set_policy(gtk::PolicyType::Automatic, gtk::PolicyType::Never);
        }
        tabs_box.add_css_class(match config.tab_position {
            TabPosition::Left => "left",
            TabPosition::Right => "right",
            TabPosition::Top => "top",
            TabPosition::Bottom => "bottom",
        });

        let stack = gtk::Stack::new();
        stack.set_hexpand(true);
        stack.set_vexpand(true);
        let body = gtk::Box::new(if vertical { gtk::Orientation::Horizontal } else { gtk::Orientation::Vertical }, 0);
        // A handle on the sidebar's inner edge resizes it.
        let handle = gtk::Box::new(gtk::Orientation::Vertical, 0);
        handle.add_css_class("bunker-tabs-handle");
        handle.set_cursor_from_name(Some("col-resize"));
        handle.set_visible(vertical);
        match config.tab_position {
            TabPosition::Left | TabPosition::Top => {
                body.append(&tabs_box);
                body.append(&handle);
                body.append(&stack);
            }
            TabPosition::Right | TabPosition::Bottom => {
                body.append(&stack);
                body.append(&handle);
                body.append(&tabs_box);
            }
        }
        window.set_child(Some(&body));

        let font = pango::FontDescription::from_string(&config.font);
        let win = Rc::new(Self {
            window,
            stack,
            list,
            tab_width: Cell::new((config.tab_width as i32).clamp(MIN_TAB_WIDTH, MAX_TAB_WIDTH)),
            fitting: Cell::new(true),
            tabs_box,
            title,
            tabs: RefCell::new(Vec::new()),
            active: Cell::new(None),
            config: RefCell::new(config),
            theme: RefCell::new(theme),
            font: RefCell::new(font),
            prefs: RefCell::new(None),
            on_config: RefCell::new(None),
            me: RefCell::new(Weak::new()),
        });
        *win.me.borrow_mut() = Rc::downgrade(&win);
        win.install_actions();
        new_tab.connect_clicked(clone!(
            #[weak]
            win,
            move |_| win.new_tab(None)
        ));
        win.install_resize(&handle);
        let weak = Rc::downgrade(&win);
        super::strip::install_drag(
            &win.list,
            &strip,
            Rc::new(move || {
                if let Some(w) = weak.upgrade() {
                    w.sync_order();
                }
            }),
        );
        win.open_tab(None, command);
        win.fit_to_terminal();
        // The display's fractional scale, and with it the cell size, is
        // only known once the window is shown; each new cell size fits the
        // window again (see open_tab). After that the size is the user's.
        let weak = Rc::downgrade(&win);
        win.window.connect_map(move |_| {
            let weak = weak.clone();
            glib::timeout_add_local_once(FIT_PERIOD, move || {
                if let Some(w) = weak.upgrade() {
                    w.fitting.set(false);
                }
            });
        });
        let weak = Rc::downgrade(&win);
        glib::timeout_add_local(STATUS_TICK, move || match weak.upgrade() {
            Some(w) => {
                w.refresh_circles();
                glib::ControlFlow::Continue
            }
            None => glib::ControlFlow::Break,
        });
        let weak = Rc::downgrade(&win);
        super::debug::install(&win.window, move |i, text| {
            let Some(w) = weak.upgrade() else { return };
            if i >= w.tabs.borrow().len() {
                w.new_tab(None);
            }
            let tab = w.tabs.borrow().get(i).cloned();
            if let Some(tab) = tab {
                tab.view.session.write(text.as_bytes().to_vec());
            }
        });
        win.window.present();
        win
    }

    /// Sizes the window for a terminal of the default size, as the Go
    /// version does: the tab list and header bar are measured, not guessed.
    /// Only while the window opens: then the size is the user's.
    fn fit_to_terminal(&self) {
        if !self.fitting.get() || self.window.is_maximized() {
            return;
        }
        let Some(tab) = self.active_tab() else { return };
        let (cw, ch) = tab.view.cell_size();
        let config = self.config.borrow().clone();
        let pad = 2.0 * config.padding as f32;
        let header = self.window.titlebar().map_or(0, |t| t.measure(gtk::Orientation::Vertical, -1).1) as f32;
        let mut w = pad + DEFAULT_COLS * cw + 0.5;
        let mut h = pad + DEFAULT_ROWS * ch + 0.5 + header;
        let shown = !(config.tab_autohide && self.tabs.borrow().len() < 2);
        if shown {
            if matches!(config.tab_position, TabPosition::Left | TabPosition::Right) {
                // The sidebar and its drag handle: everything beside the
                // terminal.
                let beside = self.window.child().map_or(0, |body| {
                    body.measure(gtk::Orientation::Horizontal, -1).0
                        - self.stack.measure(gtk::Orientation::Horizontal, -1).0
                });
                w += beside.max(self.tab_width.get()) as f32;
            } else {
                h += self.tabs_box.measure(gtk::Orientation::Vertical, -1).1.max(TAB_BAR_HEIGHT) as f32;
            }
        }
        self.window.set_default_size(w.ceil() as i32, h.ceil() as i32);
    }

    /// Lets the sidebar be dragged wider or narrower, between limits, and
    /// saves the width to the config file when the drag ends.
    fn install_resize(&self, handle: &gtk::Box) {
        let drag = gtk::GestureDrag::new();
        // The width and the pointer's window position when the drag began.
        let start = Rc::new(Cell::new((0, 0.0f32)));
        let me = self.me.borrow().clone();
        let right = self.config.borrow().tab_position == TabPosition::Right;
        // The handle moves with the sidebar's edge, so positions are taken
        // in the window's coordinates: in the handle's own, every step
        // would be measured from a moved origin and the width would jump
        // back and forth.
        let window_x = |handle: &gtk::Box, w: &Window, x: f64, y: f64| {
            handle.compute_point(&w.window, &graphene::Point::new(x as f32, y as f32)).map(|p| p.x())
        };
        drag.connect_drag_begin(clone!(
            #[strong]
            me,
            #[strong]
            start,
            #[strong]
            handle,
            move |_, x, y| {
                if let Some(w) = me.upgrade()
                    && let Some(px) = window_x(&handle, &w, x, y)
                {
                    start.set((w.tab_width.get(), px));
                }
            }
        ));
        drag.connect_drag_update(clone!(
            #[strong]
            me,
            #[strong]
            start,
            #[strong]
            handle,
            move |g, dx, dy| {
                let Some(w) = me.upgrade() else { return };
                let Some((sx, sy)) = g.start_point() else { return };
                let Some(px) = window_x(&handle, &w, sx + dx, sy + dy) else { return };
                let (width0, px0) = start.get();
                let moved = if right { px0 - px } else { px - px0 };
                let width = (width0 + moved.round() as i32).clamp(MIN_TAB_WIDTH, MAX_TAB_WIDTH);
                if width != w.tab_width.get() {
                    w.tab_width.set(width);
                    w.tabs_box.set_width_request(width);
                }
            }
        ));
        drag.connect_drag_end(clone!(
            #[strong]
            me,
            move |_, _, _| {
                let Some(w) = me.upgrade() else { return };
                let Some(path) = w.config.borrow().path.clone() else { return };
                let width = i64::from(w.tab_width.get());
                // Off the UI thread: the config may be on a slow disk.
                std::thread::spawn(move || {
                    if let Err(e) = crate::config::save(&path, Some("tabs"), "width", width.into()) {
                        crate::warn!("save sidebar width: {e}");
                    }
                });
            }
        ));
        handle.add_controller(drag);
    }

    /// Window actions, the header-bar buttons, and finishing a rename on
    /// a click anywhere else.
    fn install_actions(&self) {
        let me = self.me.borrow().clone();
        for name in ["preferences", "new-tab", "new-window", "about"] {
            let action = gio::SimpleAction::new(name, None);
            action.connect_activate(clone!(
                #[strong]
                me,
                move |_, _| {
                    if let Some(w) = me.upgrade() {
                        w.run_action(&name.replace('-', "_"));
                    }
                }
            ));
            self.window.add_action(&action);
        }
        // Capture phase: this sees every click in the window before the
        // widget under it, without taking the click from it.
        let click = gtk::GestureClick::new();
        click.set_button(0);
        click.set_propagation_phase(gtk::PropagationPhase::Capture);
        click.connect_pressed(clone!(
            #[strong]
            me,
            move |_, _, x, y| {
                let Some(w) = me.upgrade() else { return };
                let target = w.window.pick(x, y, gtk::PickFlags::DEFAULT);
                let renaming: Vec<(u64, gtk::Entry)> =
                    w.tabs.borrow().iter().filter_map(|t| t.rename.borrow().clone().map(|e| (t.id, e))).collect();
                for (id, entry) in renaming {
                    let inside = target
                        .as_ref()
                        .is_some_and(|t| t == entry.upcast_ref::<gtk::Widget>() || t.is_ancestor(&entry));
                    if !inside {
                        w.finish_rename(id, true);
                    }
                }
            }
        ));
        self.window.add_controller(click);
        self.window.connect_is_active_notify(clone!(
            #[strong]
            me,
            move |win| {
                if win.is_active()
                    && let Some(w) = me.upgrade()
                    && let Some(t) = w.active_tab()
                {
                    w.seen(&t);
                    w.refresh_row(&t);
                }
            }
        ));
    }

    fn show_about(&self) {
        let about = gtk::AboutDialog::builder()
            .transient_for(&self.window)
            .modal(true)
            .program_name("bunker")
            .logo_icon_name(super::APP_ID)
            .version(crate::VERSION)
            .comments("A GTK 4 terminal for Linux")
            .license_type(gtk::License::MitX11)
            .build();
        about.present();
    }

    /// Sets what Preferences calls to apply a change everywhere at once.
    pub fn set_config_handler(&self, f: prefs::ConfigHandler) {
        *self.on_config.borrow_mut() = Some(f);
    }

    /// Shows the Preferences window.
    fn open_prefs(&self) {
        let config = self.config.borrow().clone();
        let prefs = self.prefs.borrow().clone();
        let prefs = match prefs {
            Some(p) => {
                p.load(&config);
                p
            }
            None => {
                let p = Prefs::new(self.window.upcast_ref(), &config);
                if let Some(f) = self.on_config.borrow().clone() {
                    p.set_on_change(f);
                }
                *self.prefs.borrow_mut() = Some(p.clone());
                p
            }
        };
        prefs.window.present();
    }

    /// Applies a changed config: colours, font, padding, the sidebar.
    /// The tab position applies to new windows.
    pub fn apply(&self, config: &Config, theme: &Theme) {
        let old = self.config.borrow().clone();
        let font = pango::FontDescription::from_string(&config.font);
        let recolour = *self.theme.borrow() != *theme;
        for t in self.tabs.borrow().iter() {
            if recolour {
                t.view.session.term().set_theme(theme.clone());
                t.row.circle.set_theme(theme);
                t.view.queue_draw();
            }
            t.view.set_font(&font);
            t.view.set_padding(config.padding);
            t.view.set_cursor_blink(&config.cursor_blink);
            if config.keys != old.keys {
                t.view.set_shortcuts(super::keys::shortcuts(&config.keys));
            }
        }
        *self.font.borrow_mut() = font;
        *self.theme.borrow_mut() = theme.clone();
        *self.config.borrow_mut() = Config { tab_position: old.tab_position, ..config.clone() };
        if config.tab_width != old.tab_width {
            let width = (config.tab_width as i32).clamp(MIN_TAB_WIDTH, MAX_TAB_WIDTH);
            self.tab_width.set(width);
            if matches!(old.tab_position, TabPosition::Left | TabPosition::Right) {
                self.tabs_box.set_width_request(width);
            }
        }
        self.update_list_visibility();
        self.refresh_circles();
        if let Some(p) = self.prefs.borrow().as_ref() {
            p.load(config);
        }
    }

    fn active_tab(&self) -> Option<Rc<Tab>> {
        let id = self.active.get()?;
        self.tabs.borrow().iter().find(|t| t.id == id).cloned()
    }

    /// Opens a tab with the shell in `cwd`, or in the active tab's directory.
    pub fn new_tab(&self, cwd: Option<PathBuf>) {
        self.open_tab(cwd, &[]);
    }

    /// Opens a tab running `command` (the shell when empty).
    fn open_tab(&self, cwd: Option<PathBuf>, command: &[String]) {
        let cwd = cwd.or_else(|| self.active_tab().and_then(|t| t.info.borrow().cwd.clone()));
        let id = NEXT_ID.with(|n| {
            let id = n.get();
            n.set(id + 1);
            id
        });
        let pending = Arc::new(AtomicBool::new(false));
        let notify: Notify = Arc::new(move |wake| match wake {
            // At most one wake per frame: under heavy output the reader would
            // otherwise wake the UI on every read and fight it for the lock.
            Wake::Output => {
                if pending.swap(true, Ordering::AcqRel) {
                    return;
                }
                let p = pending.clone();
                glib::MainContext::default().invoke(move || {
                    on_wake(id, Wake::Output);
                    glib::timeout_add_local_once(OUTPUT_COALESCE, move || {
                        if p.swap(false, Ordering::AcqRel) {
                            on_wake(id, Wake::Output);
                        }
                    });
                });
            }
            other => glib::MainContext::default().invoke(move || on_wake(id, other)),
        });
        let session = match Session::spawn(
            command,
            cwd.as_deref(),
            80,
            24,
            self.config.borrow().scrollback,
            self.theme.borrow().clone(),
            Box::new(super::images::GtkCodec),
            notify,
        ) {
            Ok(s) => s,
            Err(e) => {
                crate::error!("open tab: {e}");
                return;
            }
        };
        let (widget, view) = TermView::new(session, &self.font.borrow(), self.config.borrow().padding);
        view.set_shortcuts(super::keys::shortcuts(&self.config.borrow().keys));
        view.set_cursor_blink(&self.config.borrow().cursor_blink);
        let me = self.me.borrow().clone();
        view.set_metrics_handler(Box::new(move || {
            if let Some(w) = me.upgrade() {
                w.fit_to_terminal();
            }
        }));
        let me = self.me.borrow().clone();
        view.set_action_handler(Box::new(clone!(
            #[strong]
            me,
            move |action| {
                if let Some(w) = me.upgrade() {
                    w.run_action(action);
                }
            }
        )));
        let row = self.make_row(id);
        self.list.append(&row.root);
        let page = gtk::Overlay::new();
        page.set_child(Some(&widget));
        let search = SearchBar::new(view.clone(), widget.clone());
        page.add_overlay(&search.root);
        let pass_badge = gtk::Label::new(Some("PASS"));
        pass_badge.add_css_class("bunker-pass");
        pass_badge.set_halign(gtk::Align::End);
        pass_badge.set_valign(gtk::Align::End);
        pass_badge.set_tooltip_text(Some("Every key goes to the program; Ctrl+F12 turns this off"));
        pass_badge.set_visible(false);
        page.add_overlay(&pass_badge);
        self.stack.add_named(&page, Some(&id.to_string()));
        let tab = Rc::new(Tab {
            id,
            view,
            widget,
            page,
            search,
            pass_badge,
            row,
            info: RefCell::new(ProcInfo { cwd: cwd.clone(), ..ProcInfo::default() }),
            attention: Cell::new(None),
            colour: Cell::new(None),
            running_since: Cell::new(None),
            last_output: Cell::new(None),
            custom_title: RefCell::new(None),
            rename: RefCell::new(None),
            title_busy: Cell::new(None),
            notified: Cell::new(None),
        });
        TABS.with(|t| t.borrow_mut().insert(id, (me, Rc::downgrade(&tab))));
        self.tabs.borrow_mut().push(tab.clone());
        self.update_list_visibility();
        self.refresh_row(&tab);
        self.activate(id);
    }

    fn make_row(&self, id: u64) -> TabRow {
        let vertical = matches!(self.config.borrow().tab_position, TabPosition::Left | TabPosition::Right);
        let root = gtk::Box::new(gtk::Orientation::Horizontal, 0);
        root.add_css_class("bunker-tab");
        // The circle fills the row's height; the row itself does not grow.
        root.set_vexpand(false);
        let circle = Indicator::new(&self.theme.borrow());
        let text = gtk::Box::new(gtk::Orientation::Vertical, 0);
        text.add_css_class("bunker-tab-text");
        text.set_hexpand(true);
        text.set_valign(gtk::Align::Center);
        let title = gtk::Label::new(None);
        title.add_css_class("bunker-tab-title");
        title.set_xalign(0.0);
        title.set_ellipsize(pango::EllipsizeMode::End);
        let detail = gtk::Label::new(None);
        detail.add_css_class("bunker-tab-detail");
        detail.set_xalign(0.0);
        detail.set_ellipsize(pango::EllipsizeMode::Start);
        let branch = gtk::Label::new(None);
        branch.add_css_class("bunker-tab-branch");
        branch.set_xalign(0.0);
        branch.set_ellipsize(pango::EllipsizeMode::End);
        let progress = gtk::ProgressBar::new();
        progress.add_css_class("bunker-tab-progress");
        progress.set_visible(false);
        text.append(&title);
        if vertical {
            // Labels ask for one character's width, so long titles
            // ellipsize instead of widening the sidebar.
            for l in [&title, &detail, &branch] {
                l.set_max_width_chars(1);
            }
            text.append(&detail);
            text.append(&branch);
        } else {
            title.set_max_width_chars(24);
        }
        text.append(&progress);
        let close = gtk::Button::from_icon_name("window-close-symbolic");
        close.add_css_class("flat");
        close.add_css_class("bunker-tab-close");
        close.set_valign(gtk::Align::Center);
        close.set_tooltip_text(Some("Close tab"));
        root.append(&circle.area);
        root.append(&text);
        root.append(&close);

        let me = self.me.borrow().clone();
        close.connect_clicked(clone!(
            #[strong]
            me,
            move |_| {
                if let Some(w) = me.upgrade() {
                    w.close_tab(id);
                }
            }
        ));
        let click = gtk::GestureClick::new();
        click.set_button(0);
        click.connect_pressed(clone!(
            #[strong]
            me,
            move |g, presses, _, _| {
                let Some(w) = me.upgrade() else { return };
                match g.current_button() {
                    gdk::BUTTON_MIDDLE => w.close_tab(id),
                    gdk::BUTTON_SECONDARY => w.show_menu(id),
                    _ if presses == 2 => w.start_rename(id),
                    _ => w.activate(id),
                }
            }
        ));
        root.add_controller(click);
        TabRow { root, circle, text, title, detail, branch, progress }
    }

    /// Replaces the tab's title with an entry to rename it. Enter or
    /// leaving the entry keeps the name, Escape cancels, and an empty name
    /// goes back to the automatic title.
    fn start_rename(&self, id: u64) {
        let Some(tab) = self.find(id) else { return };
        if tab.rename.borrow().is_some() {
            return;
        }
        let entry = gtk::Entry::new();
        entry.add_css_class("bunker-tab-rename");
        entry.set_text(&tab.row.title.label());
        entry.set_placeholder_text(Some("Tab name"));
        tab.row.text.insert_child_after(&entry, Some(&tab.row.title));
        tab.row.title.set_visible(false);
        *tab.rename.borrow_mut() = Some(entry.clone());
        let me = self.me.borrow().clone();
        let finish = move |keep: bool| {
            if let Some(w) = me.upgrade() {
                w.finish_rename(id, keep);
            }
        };
        let f = finish.clone();
        entry.connect_activate(move |_| f(true));
        let keys = gtk::EventControllerKey::new();
        let f = finish.clone();
        keys.connect_key_pressed(move |_, key, _, _| {
            if key == gdk::Key::Escape {
                f(false);
                return glib::Propagation::Stop;
            }
            glib::Propagation::Proceed
        });
        entry.add_controller(keys);
        let focus = gtk::EventControllerFocus::new();
        focus.connect_leave(move |_| finish(true));
        entry.add_controller(focus);
        entry.grab_focus();
    }

    fn finish_rename(&self, id: u64, keep: bool) {
        let Some(tab) = self.find(id) else { return };
        let Some(entry) = tab.rename.borrow_mut().take() else { return };
        if keep {
            let name = entry.text().trim().to_string();
            *tab.custom_title.borrow_mut() = (!name.is_empty()).then_some(name);
        }
        tab.row.title.set_visible(true);
        self.refresh_row(&tab);
        if self.active.get() == Some(id) {
            tab.widget.grab_focus();
        }
        // This can run while GTK is moving focus out of the entry; removing
        // it then leaves GTK walking a destroyed widget, which hangs. It is
        // removed once the focus change is over.
        let text = tab.row.text.clone();
        glib::idle_add_local_once(move || text.remove(&entry));
    }

    /// Puts the tabs in the order their rows now have, after a drag.
    fn sync_order(&self) {
        let mut rows = Vec::new();
        let mut child = self.list.first_child();
        while let Some(c) = child {
            child = c.next_sibling();
            rows.push(c);
        }
        let place = |t: &Rc<Tab>| rows.iter().position(|r| r == t.row.root.upcast_ref::<gtk::Widget>());
        self.tabs.borrow_mut().sort_by_key(|t| place(t).unwrap_or(usize::MAX));
        self.group_tags();
    }

    fn find(&self, id: u64) -> Option<Rc<Tab>> {
        self.tabs.borrow().iter().find(|t| t.id == id).cloned()
    }

    /// The tab's menu: tag colours and Close.
    fn show_menu(&self, id: u64) {
        let Some(tab) = self.find(id) else { return };
        let popover = gtk::Popover::new();
        popover.add_css_class("bunker-tab-menu");
        popover.set_has_arrow(false);
        popover.set_position(gtk::PositionType::Right);
        let items = gtk::Box::new(gtk::Orientation::Vertical, 0);
        let heading = gtk::Label::new(Some("Colour"));
        heading.add_css_class("bunker-menu-heading");
        heading.set_xalign(0.0);
        items.append(&heading);
        let me = self.me.borrow().clone();
        let choices = super::style::TAG_COLOURS.iter().map(|c| (Some(c.name), c.label)).chain([(None, "None")]);
        for (name, label) in choices {
            let swatch = gtk::Box::new(gtk::Orientation::Horizontal, 0);
            swatch.add_css_class("bunker-swatch");
            swatch.add_css_class(&format!("swatch-{}", name.unwrap_or("none")));
            swatch.set_valign(gtk::Align::Center);
            let text = gtk::Label::new(Some(label));
            text.set_xalign(0.0);
            text.set_hexpand(true);
            let check = gtk::Image::from_icon_name("object-select-symbolic");
            if tab.colour.get() != name {
                check.set_opacity(0.0);
            }
            let content = gtk::Box::new(gtk::Orientation::Horizontal, 10);
            content.append(&swatch);
            content.append(&text);
            content.append(&check);
            let button = gtk::Button::new();
            button.set_child(Some(&content));
            button.add_css_class("flat");
            button.connect_clicked(clone!(
                #[strong]
                me,
                #[weak]
                popover,
                move |_| {
                    popover.popdown();
                    if let Some(w) = me.upgrade() {
                        w.set_colour(id, name);
                    }
                }
            ));
            items.append(&button);
        }
        items.append(&gtk::Separator::new(gtk::Orientation::Horizontal));
        let rename = gtk::Button::with_label("Rename…");
        rename.add_css_class("flat");
        if let Some(l) = rename.child().and_downcast::<gtk::Label>() {
            l.set_xalign(0.0);
        }
        rename.connect_clicked(clone!(
            #[strong]
            me,
            #[weak]
            popover,
            move |_| {
                popover.popdown();
                if let Some(w) = me.upgrade() {
                    w.start_rename(id);
                }
            }
        ));
        items.append(&rename);
        let close = gtk::Button::with_label("Close Tab");
        close.add_css_class("flat");
        if let Some(l) = close.child().and_downcast::<gtk::Label>() {
            l.set_xalign(0.0);
        }
        close.connect_clicked(clone!(
            #[strong]
            me,
            #[weak]
            popover,
            move |_| {
                popover.popdown();
                if let Some(w) = me.upgrade() {
                    w.close_tab(id);
                }
            }
        ));
        items.append(&close);
        popover.set_child(Some(&items));
        popover.set_parent(&tab.row.root);
        popover.connect_closed(|p| {
            let p = p.clone();
            glib::idle_add_local_once(move || p.unparent());
        });
        popover.popup();
    }

    /// Tags tab `id` with a colour, or clears its tag.
    fn set_colour(&self, id: u64, name: Option<&'static str>) {
        let Some(tab) = self.find(id) else { return };
        tab.colour.set(name);
        self.group_tags();
    }

    /// Marks where each tagged row sits in its run of same-coloured
    /// neighbours: rows in a run sit together and their circles are joined.
    fn group_tags(&self) {
        let tabs = self.tabs.borrow();
        for (i, t) in tabs.iter().enumerate() {
            let c = t.colour.get();
            let same =
                |j: Option<usize>| c.is_some() && j.and_then(|j| tabs.get(j)).is_some_and(|o| o.colour.get() == c);
            let (prev, next) = (same(i.checked_sub(1)), same(Some(i + 1)));
            for class in ["group-first", "group-mid", "group-last"] {
                t.row.root.remove_css_class(class);
            }
            match (prev, next) {
                (false, true) => t.row.root.add_css_class("group-first"),
                (true, true) => t.row.root.add_css_class("group-mid"),
                (true, false) => t.row.root.add_css_class("group-last"),
                (false, false) => {}
            }
        }
        drop(tabs);
        self.refresh_circles();
    }

    /// What tab `t` is doing, for its circle.
    fn status(&self, t: &Tab) -> Status {
        match t.attention.get() {
            Some(Attention::Bell | Attention::Notify) => return Status::NeedsYou,
            Some(Attention::Done) => return Status::Done,
            Some(Attention::Failed) => return Status::Failed,
            None => {}
        }
        let info = t.info.borrow();
        if info.agent.is_some() {
            if let Some(busy) = t.title_busy.get() {
                return if busy { Status::Working } else { Status::Ready };
            }
            let now = Instant::now();
            let recent = t.last_output.get().is_some_and(|o| now.duration_since(o) < WORKING_FOR);
            let echo = match (t.last_output.get(), t.view.last_input()) {
                (Some(o), Some(i)) => o >= i && o.duration_since(i) < ECHO_WITHIN,
                _ => false,
            };
            return if recent && !echo { Status::Working } else { Status::Ready };
        }
        if info.command.is_some() { Status::Running } else { Status::Idle }
    }

    /// Brings every tab's circle up to date.
    fn refresh_circles(&self) {
        let vertical = matches!(self.config.borrow().tab_position, TabPosition::Left | TabPosition::Right);
        let theme = self.theme.borrow();
        let tabs = self.tabs.borrow();
        for (i, t) in tabs.iter().enumerate() {
            let c = t.colour.get();
            let same =
                |j: Option<usize>| c.is_some() && j.and_then(|j| tabs.get(j)).is_some_and(|o| o.colour.get() == c);
            let (up, down) = (vertical && same(i.checked_sub(1)), vertical && same(Some(i + 1)));
            let tag = c.and_then(|n| super::style::tag_rgb(&theme, n));
            t.row.circle.set(self.status(t), tag, up, down);
        }
    }

    fn update_list_visibility(&self) {
        let hide = self.config.borrow().tab_autohide && self.tabs.borrow().len() < 2;
        self.tabs_box.set_visible(!hide);
    }

    /// Shows tab `id`.
    fn activate(&self, id: u64) {
        let Some(tab) = self.find(id) else { return };
        self.active.set(Some(id));
        self.stack.set_visible_child(&tab.page);
        for t in self.tabs.borrow().iter() {
            if t.id == id {
                t.row.root.add_css_class("active");
            } else {
                t.row.root.remove_css_class("active");
            }
        }
        self.seen(&tab);
        self.refresh_row(&tab);
        tab.widget.grab_focus();
        tab.view.queue_draw();
    }

    fn close_tab(&self, id: u64) {
        let idx = self.tabs.borrow().iter().position(|t| t.id == id);
        let Some(idx) = idx else { return };
        let tab = self.tabs.borrow_mut().remove(idx);
        TABS.with(|t| t.borrow_mut().remove(&id));
        self.list.remove(&tab.row.root);
        self.stack.remove(&tab.page);
        self.update_list_visibility();
        self.group_tags();
        let remaining = self.tabs.borrow().len();
        if remaining == 0 {
            self.window.close();
            return;
        }
        if self.active.get() == Some(id) {
            let next = self.tabs.borrow()[idx.min(remaining - 1)].id;
            self.activate(next);
        }
    }

    fn cycle(&self, delta: isize) {
        let tabs = self.tabs.borrow();
        let Some(i) = tabs.iter().position(|t| Some(t.id) == self.active.get()) else { return };
        let n = tabs.len() as isize;
        let next = tabs[((i as isize + delta).rem_euclid(n)) as usize].id;
        drop(tabs);
        self.activate(next);
    }

    fn run_action(&self, action: &str) {
        match action {
            "new_tab" => self.new_tab(None),
            "close_tab" => {
                if let Some(id) = self.active.get() {
                    self.close_tab(id);
                }
            }
            "next_tab" => self.cycle(1),
            "prev_tab" => self.cycle(-1),
            "close_window" => self.window.close(),
            "preferences" => self.open_prefs(),
            "passthrough" => {
                if let Some(t) = self.active_tab() {
                    t.pass_badge.set_visible(t.view.passthrough());
                }
            }
            "search" => {
                if let Some(t) = self.active_tab() {
                    t.search.open();
                }
            }
            // A new window comes from the application, which keeps every
            // window in step with the config file.
            "new_window" => {
                if let Some(app) = self.window.application() {
                    app.activate();
                }
            }
            "about" => self.show_about(),
            other => crate::debug!("unhandled action {other}"),
        }
    }

    /// New output in `tab`: redraw it and act on its events.
    fn output(&self, tab: &Rc<Tab>) {
        tab.last_output.set(Some(Instant::now()));
        let events = tab.view.session.term().take_events();
        let visible = self.active.get() == Some(tab.id) && self.window.is_active();
        let mut changed = false;
        for e in events {
            match e {
                Event::TitleChanged | Event::CwdChanged => changed = true,
                Event::Bell if self.config.borrow().notify_bell => {
                    self.alert(tab, Attention::Bell, visible, "Wants your attention".into())
                }
                Event::Notify { title, body } => {
                    let text = match (title.is_empty(), body.is_empty()) {
                        (false, false) => format!("{title}: {body}"),
                        (false, true) => title,
                        _ => body,
                    };
                    self.alert(tab, Attention::Notify, visible, text);
                }
                Event::CommandStart => tab.running_since.set(Some(Instant::now())),
                Event::CommandFinished(code) => {
                    // Only a command that ran a while counts: shells report
                    // every prompt.
                    let ran = tab.running_since.take().map(|t| t.elapsed());
                    let min = self.config.borrow().command_seconds;
                    if let Some(ran) = ran.filter(|r| min > 0 && r.as_secs() >= min) {
                        let failed = code.filter(|c| *c != 0);
                        let verb = failed.map_or("finished".to_string(), |c| format!("failed (exit {c})"));
                        let text = format!("The command {verb} after {}", human_duration(ran));
                        let kind = if failed.is_some() { Attention::Failed } else { Attention::Done };
                        self.alert(tab, kind, visible, text);
                    }
                }
                Event::Progress { state, percent } => {
                    let bar = &tab.row.progress;
                    bar.set_visible(state != 0);
                    if state == 3 {
                        bar.pulse();
                    } else {
                        bar.set_fraction(f64::from(percent) / 100.0);
                    }
                    if state == 2 {
                        bar.add_css_class("error");
                    } else {
                        bar.remove_css_class("error");
                    }
                }
                Event::Clipboard(text) => tab.widget.clipboard().set_text(&text),
                _ => {}
            }
        }
        if changed {
            self.refresh_row(tab);
        }
        if self.active.get() == Some(tab.id) {
            tab.view.queue_draw();
        }
    }

    /// Marks a tab that is out of view, and sends a desktop notification
    /// as [notify] desktop says.
    fn alert(&self, tab: &Rc<Tab>, kind: Attention, visible: bool, text: String) {
        if visible {
            return;
        }
        // A failure outranks a success, which outranks a bell.
        let keep = matches!((tab.attention.get(), kind), (Some(Attention::Failed), _));
        if !keep {
            tab.attention.set(Some(kind));
        }
        self.refresh_row(tab);
        let send = match self.config.borrow().notify_desktop.as_str() {
            "always" => true,
            "never" => false,
            _ => !self.window.is_active(),
        };
        if send {
            self.notify(tab, &text);
        }
    }

    fn notify(&self, tab: &Rc<Tab>, text: &str) {
        let now = Instant::now();
        if tab.notified.get().is_some_and(|t| now.duration_since(t) < NOTIFY_INTERVAL) {
            return;
        }
        let Some(app) = self.window.application() else { return };
        tab.notified.set(Some(now));
        let n = gio::Notification::new(&tab.row.title.label());
        n.set_body(Some(text));
        n.set_default_action_and_target_value("app.focus-tab", Some(&tab.id.to_string().to_variant()));
        app.send_notification(Some(&notification_id(tab.id)), &n);
    }

    /// Takes back a tab's notification once the tab is looked at.
    fn seen(&self, tab: &Tab) {
        tab.attention.set(None);
        if tab.notified.take().is_some()
            && let Some(app) = self.window.application()
        {
            app.withdraw_notification(&notification_id(tab.id));
        }
    }

    /// Updates a tab's row (and the window title for the active tab).
    fn refresh_row(&self, tab: &Rc<Tab>) {
        let mut term_title = tab.view.session.term().title().to_string();
        let info = tab.info.borrow();
        // Claude Code starts its title with ◐ or ◑ while it works and ✳
        // while it waits; the circle shows that, so the title drops it.
        let (busy, rest) = agent_title(&term_title);
        tab.title_busy.set(info.agent.and(busy));
        term_title = rest.to_string();
        // Shells title themselves "user@host:dir", which the row already
        // shows; over ssh the remote directory is worth keeping.
        let mut remote_dir = None;
        if let Some((_, dir)) = shell_title(&term_title) {
            remote_dir = info.ssh_host.is_some().then(|| dir.to_string());
            term_title.clear();
        }
        let dir = info.cwd.as_deref().map(procinfo::tilde);
        let dir_name = info
            .cwd
            .as_deref()
            .and_then(|p| p.file_name())
            .map(|n| n.to_string_lossy().into_owned())
            .or_else(|| dir.clone());
        let title = if let Some(name) = tab.custom_title.borrow().clone() {
            name
        } else if !term_title.is_empty() {
            term_title.clone()
        } else if let Some(dir) = &remote_dir {
            dir.rsplit('/').find(|p| !p.is_empty()).unwrap_or(dir).to_string()
        } else if let Some(cmd) = &info.command {
            cmd.clone()
        } else {
            dir_name.clone().unwrap_or_else(|| "bunker".into())
        };
        // Second line: where the tab is. Third: the branch and what runs.
        let detail = match (&info.ssh_host, &remote_dir) {
            (Some(host), Some(d)) => format!("{host}:{d}"),
            (Some(host), None) => format!("ssh {host}"),
            _ => dir.clone().unwrap_or_default(),
        };
        let mut meta = Vec::new();
        if let (Some(b), None) = (&info.git_branch, &info.ssh_host) {
            meta.push(format!("⎇ {b}"));
        }
        // What runs: the agent, another command, or else the shell, so the
        // line is always there and rows keep one height.
        let running = info.agent.map(str::to_string).or_else(|| info.command.clone()).or_else(|| info.shell.clone());
        if let Some(cmd) = running.filter(|c| *c != title || meta.is_empty()) {
            meta.push(cmd);
        }
        let row = &tab.row;
        row.title.set_label(&title);
        row.detail.set_label(&detail);
        row.detail.set_visible(!detail.is_empty());
        row.branch.set_label(&meta.join(" · "));
        row.branch.set_visible(!meta.is_empty());
        let tooltip = [Some(title.clone()), dir, info.git_branch.as_ref().map(|b| format!("branch {b}"))]
            .into_iter()
            .flatten()
            .collect::<Vec<_>>()
            .join("\n");
        row.root.set_tooltip_text(Some(&tooltip));
        if self.active.get() == Some(tab.id) {
            self.title.set_label(&title);
            self.window.set_title(Some(&title));
        }
        drop(info);
        self.refresh_circles();
    }
}

fn notification_id(tab: u64) -> String {
    format!("tab-{}-{tab}", std::process::id())
}

/// A duration for people: "45 s", "2 min 5 s", "1 h 3 min".
fn human_duration(d: Duration) -> String {
    let secs = d.as_secs();
    let (h, m, s) = (secs / 3600, secs / 60 % 60, secs % 60);
    let mut parts = Vec::new();
    if h > 0 {
        parts.push(format!("{h} h"));
    }
    if m > 0 {
        parts.push(format!("{m} min"));
    }
    if s > 0 && h == 0 {
        parts.push(format!("{s} s"));
    }
    if parts.is_empty() { "0 s".into() } else { parts.join(" ") }
}

/// Brings up the window holding tab `id` and shows the tab, for a clicked
/// notification.
pub fn focus_tab(id: u64) {
    let found = TABS.with(|t| t.borrow().get(&id).cloned());
    if let Some((win, _)) = found
        && let Some(w) = win.upgrade()
    {
        w.window.present();
        w.activate(id);
    }
}

/// Reads the state an agent puts at the start of its title: `Some(true)`
/// for busy, `Some(false)` for waiting. Returns the title without it.
fn agent_title(title: &str) -> (Option<bool>, &str) {
    let mut chars = title.chars();
    let busy = match chars.next() {
        Some('◐' | '◑') => true,
        Some('✳') => false,
        _ => return (None, title),
    };
    (Some(busy), chars.as_str().trim_start())
}

/// Splits a shell's default title, `user@host:dir`, into host and directory.
fn shell_title(title: &str) -> Option<(&str, &str)> {
    let (user_host, dir) = title.split_once(':')?;
    let (user, host) = user_host.split_once('@')?;
    let word = |s: &str| !s.is_empty() && !s.contains(char::is_whitespace);
    (word(user) && word(host) && (dir.starts_with('~') || dir.starts_with('/'))).then_some((host, dir.trim()))
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn durations() {
        assert_eq!(human_duration(Duration::from_secs(45)), "45 s");
        assert_eq!(human_duration(Duration::from_secs(125)), "2 min 5 s");
        assert_eq!(human_duration(Duration::from_secs(3780)), "1 h 3 min");
    }

    #[test]
    fn agent_titles() {
        assert_eq!(agent_title("◐ Fix the tests"), (Some(true), "Fix the tests"));
        assert_eq!(agent_title("◑ Fix the tests"), (Some(true), "Fix the tests"));
        assert_eq!(agent_title("✳ Claude Code"), (Some(false), "Claude Code"));
        assert_eq!(agent_title("vim notes.md"), (None, "vim notes.md"));
    }

    #[test]
    fn shell_titles() {
        assert_eq!(shell_title("me@box:~/code"), Some(("box", "~/code")));
        assert_eq!(shell_title("me@box: /etc"), None);
        assert_eq!(shell_title("✳ Fix the tests"), None);
        assert_eq!(shell_title("vim: notes.md"), None);
    }
}
