//! The GTK frontend.

mod debug;
mod glyphs;
mod images;
mod indicator;
mod keys;
mod prefs;
mod render;
mod search;
mod strip;
mod style;
mod themes;
mod view;
mod window;

use std::cell::{Cell, RefCell};
use std::path::PathBuf;
use std::rc::{Rc, Weak};
use std::time::Duration;

use gtk::prelude::*;
use gtk::{gio, glib};

use crate::config::Config;

pub use keys::{ACTIONS, canonical_key, shortcuts};

/// The application id, which the launcher and icons are named after.
/// Windows are not unique, so each opens without asking another process.
pub const APP_ID: &str = crate::cli::APP_ID;

const DESKTOP_SCHEMA: &str = "org.gnome.desktop.interface";

/// How long to wait after the config file changes before reading it, so
/// a burst of writes is read once.
const RELOAD_DELAY: Duration = Duration::from_millis(120);

/// The desktop's style settings, when the schema is installed.
fn desktop_settings() -> Option<gio::Settings> {
    let source = gio::SettingsSchemaSource::default()?;
    let schema = source.lookup(DESKTOP_SCHEMA, true)?;
    schema.has_key("color-scheme").then(|| gio::Settings::new(DESKTOP_SCHEMA))
}

/// Reports whether the desktop prefers a dark style.
fn prefers_dark(settings: Option<&gio::Settings>) -> bool {
    settings.is_none_or(|s| s.string("color-scheme") != "prefer-light")
}

/// Settings shared by every window, kept in step with the config file.
struct App {
    config: RefCell<Config>,
    windows: RefCell<Vec<Weak<window::Window>>>,
    desktop: Option<gio::Settings>,
    monitor: RefCell<Option<gio::FileMonitor>>,
    reload_pending: Cell<bool>,
}

impl App {
    fn theme(&self) -> bunker_vt::Theme {
        crate::theme::resolve(&self.config.borrow().theme, prefers_dark(self.desktop.as_ref()))
    }

    /// Applies the current config to every window.
    fn apply_all(&self) {
        let start = std::time::Instant::now();
        let config = self.config.borrow().clone();
        let theme = self.theme();
        style::apply(&theme);
        self.windows.borrow_mut().retain(|w| w.strong_count() > 0);
        for w in self.windows.borrow().iter().filter_map(Weak::upgrade) {
            w.apply(&config, &theme);
        }
        crate::debug!("config applied in {:.1} ms", start.elapsed().as_secs_f64() * 1e3);
    }

    /// Takes a config changed in Preferences and applies it at once.
    fn set_config(&self, config: Config) {
        if config != *self.config.borrow() {
            *self.config.borrow_mut() = config;
            self.apply_all();
        }
    }

    /// Reads the config file again, off the UI thread, and applies it.
    fn reload(self: &Rc<Self>) {
        let Some(path) = self.config.borrow().path.clone() else { return };
        let app = Rc::downgrade(self);
        glib::spawn_future_local(async move {
            let loaded = gio::spawn_blocking(move || crate::config::load(&path, false)).await;
            let Some(app) = app.upgrade() else { return };
            match loaded {
                Ok(Ok(config)) => {
                    if config != *app.config.borrow() {
                        crate::info!("config changed: theme {} font {}", config.theme, config.font);
                        *app.config.borrow_mut() = config;
                        app.apply_all();
                    }
                }
                Ok(Err(e)) => crate::warn!("config not applied: {e}"),
                Err(_) => crate::warn!("config reader panicked"),
            }
        });
    }

    /// Watches the config file's directory, since saves usually replace
    /// the file, and the desktop's light or dark style for `system`.
    fn watch(self: &Rc<Self>) {
        if self.monitor.borrow().is_some() {
            return;
        }
        let Some(path) = self.config.borrow().path.clone() else { return };
        let (Some(dir), Some(name)) = (path.parent().map(PathBuf::from), path.file_name().map(|n| n.to_owned())) else {
            return;
        };
        let _ = std::fs::create_dir_all(&dir);
        let monitor = match gio::File::for_path(&dir)
            .monitor_directory(gio::FileMonitorFlags::WATCH_MOVES, gio::Cancellable::NONE)
        {
            Ok(m) => m,
            Err(e) => {
                crate::warn!("watch {}: {e}", dir.display());
                return;
            }
        };
        let app = Rc::downgrade(self);
        monitor.connect_changed(move |_, file, other, _| {
            let is_config = |f: Option<&gio::File>| f.and_then(|f| f.basename()).is_some_and(|b| b == name);
            if !is_config(Some(file)) && !is_config(other) {
                return;
            }
            let Some(a) = app.upgrade() else { return };
            if a.reload_pending.replace(true) {
                return;
            }
            let app = app.clone();
            glib::timeout_add_local_once(RELOAD_DELAY, move || {
                if let Some(a) = app.upgrade() {
                    a.reload_pending.set(false);
                    a.reload();
                }
            });
        });
        *self.monitor.borrow_mut() = Some(monitor);

        if let Some(desktop) = &self.desktop {
            let app = Rc::downgrade(self);
            desktop.connect_changed(Some("color-scheme"), move |_, _| {
                if let Some(a) = app.upgrade()
                    && a.config.borrow().theme == "system"
                {
                    a.apply_all();
                }
            });
        }
    }
}

/// Runs the GTK application until its last window closes.
pub fn run(config: Config, command: Vec<String>) -> glib::ExitCode {
    let app = gtk::Application::builder().application_id(APP_ID).flags(gio::ApplicationFlags::NON_UNIQUE).build();
    let state = Rc::new(App {
        config: RefCell::new(config),
        windows: RefCell::new(Vec::new()),
        desktop: desktop_settings(),
        monitor: RefCell::new(None),
        reload_pending: Cell::new(false),
    });
    // A clicked notification brings up its tab.
    let focus = gio::SimpleAction::new("focus-tab", Some(glib::VariantTy::STRING));
    focus.connect_activate(|_, param| {
        if let Some(id) = param.and_then(|p| p.str()).and_then(|s| s.parse().ok()) {
            window::focus_tab(id);
        }
    });
    app.add_action(&focus);
    let windows: Rc<RefCell<Vec<Rc<window::Window>>>> = Rc::default();
    app.connect_startup(|_| {
        // The icon is in the binary: unpack it so windows have it without
        // an install.
        let dir = crate::cli::icon_cache_dir();
        match crate::cli::write_icons(&dir) {
            Ok(()) => {
                if let Some(display) = gtk::gdk::Display::default() {
                    gtk::IconTheme::for_display(&display).add_search_path(&dir);
                }
            }
            Err(e) => crate::warn!("icons: {e}"),
        }
        gtk::Window::set_default_icon_name(APP_ID);
    });
    app.connect_activate(move |app| {
        state.watch();
        let config = state.config.borrow().clone();
        let win = window::Window::open(app, config, state.theme(), &command);
        let weak = Rc::downgrade(&state);
        win.set_config_handler(Rc::new(move |c| {
            if let Some(a) = weak.upgrade() {
                a.set_config(c);
            }
        }));
        state.windows.borrow_mut().push(Rc::downgrade(&win));
        windows.borrow_mut().push(win);
    });
    app.run_with_args(&[std::env::args().next().unwrap_or_else(|| "bunker".into())])
}
