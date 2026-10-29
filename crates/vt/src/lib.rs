//! bunker's terminal emulator core: the escape-sequence parser, the screen
//! and scrollback, resizing with reflow, and key and mouse encoding.
//!
//! It does no I/O. A frontend feeds program output to [`Terminal::advance`],
//! writes [`Terminal::take_replies`] back to the program, handles
//! [`Terminal::take_events`], and draws [`Terminal::view_row`].

mod cell;
mod colors;
mod csi;
mod graphics;
mod grid;
mod input;
mod osc;
mod parser;
mod reflow;
mod search;
mod select;
mod term;
mod unicode;

pub use cell::{Attrs, Cell, CellFlags, Color, Style, StyleId, Underline};
pub use colors::{Palette, Rgb, Theme};
pub use graphics::{Codec, Image, Placement};
pub use grid::{Row, RowFlags};
pub use input::{
    Key, KeyModes, KeypadKey, Mods, MouseAction, MouseButton, encode_focus, encode_key, encode_mouse, encode_paste,
};
pub use osc::base64_decode;
pub use parser::{Params, Parser, Perform};
pub use select::Point;
pub use term::{CursorShape, Event, Modes, MouseMode, Terminal};
pub use unicode::{char_width, cluster_width};

#[cfg(test)]
mod tests;
