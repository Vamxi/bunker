//! Hooks for screenshots and scripted checks, driven by the environment:
//!
//!   BUNKER_INPUT       text typed into tabs; tabs are separated by `|||`,
//!                      and each extra part opens a new tab first
//!   BUNKER_SCREENSHOT  after BUNKER_SCREENSHOT_DELAY ms (default 1500),
//!                      save the window as a PNG at this path and quit

use std::time::Duration;

use gtk::glib;
use gtk::prelude::*;

/// Installs the hooks for `window`. `type_into` types text into tab `i`,
/// opening it first when it does not exist.
pub fn install(window: &gtk::ApplicationWindow, type_into: impl Fn(usize, &str) + 'static) {
    if let Ok(input) = std::env::var("BUNKER_INPUT") {
        glib::timeout_add_local_once(Duration::from_millis(300), move || {
            for (i, part) in input.split("|||").enumerate() {
                type_into(i, &part.replace("\\n", "\n").replace("\\e", "\x1b"));
            }
        });
    }
    let Ok(path) = std::env::var("BUNKER_SCREENSHOT") else { return };
    let delay = std::env::var("BUNKER_SCREENSHOT_DELAY").ok().and_then(|d| d.parse().ok()).unwrap_or(1500);
    let window = window.clone();
    glib::timeout_add_local_once(Duration::from_millis(delay), move || {
        if let Err(e) = screenshot(&window, &path) {
            crate::error!("screenshot: {e}");
        }
        window.close();
    });
}

fn screenshot(window: &gtk::ApplicationWindow, path: &str) -> Result<(), String> {
    let paintable = gtk::WidgetPaintable::new(Some(window));
    let (w, h) = (window.width(), window.height());
    let snap = gtk::Snapshot::new();
    paintable.snapshot(&snap, f64::from(w), f64::from(h));
    let node = snap.to_node().ok_or("nothing drawn")?;
    let renderer = window.native().and_then(|n| n.renderer()).ok_or("no renderer")?;
    let texture = renderer.render_texture(&node, None);
    texture.save_to_png(path).map_err(|e| format!("save {path}: {e}"))
}
