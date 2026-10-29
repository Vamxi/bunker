//! Control sequences (CSI): cursor movement, editing, modes, SGR, and
//! queries.

use std::io::Write;

use crate::cell::{Attrs, Color, Style, Underline};
use crate::parser::Params;
use crate::term::{Event, MouseMode, Terminal};

/// Primary device attributes: a VT220 with SIXEL graphics (4) and colour (22).
const DA1: &[u8] = b"\x1b[?62;4;22c";
/// Secondary device attributes, as xterm 279 answers.
const DA2: &[u8] = b"\x1b[>0;279;0c";
/// Tertiary device attributes: no unit id.
const DA3: &[u8] = b"\x1bP!|00000000\x1b\\";
/// XTVERSION. Programs such as Claude Code and Neovim enable features by
/// terminal name, and bunker supports what VTE does.
const XTVERSION: &[u8] = b"\x1bP>|VTE(8203)\x1b\\";

pub(crate) fn dispatch(t: &mut Terminal, p: &Params, inter: &[u8], action: u8) {
    let n = |i: usize| usize::from(p.get(i, 1));
    match (inter, action) {
        ([], b'@') => t.insert_blanks(n(0)),
        ([], b'A') => t.move_rows(-(n(0) as isize)),
        ([], b'B' | b'e') => t.move_rows(n(0) as isize),
        ([], b'C' | b'a') => t.move_cols(n(0) as isize),
        ([], b'D') => t.move_cols(-(n(0) as isize)),
        ([], b'E') => {
            t.move_rows(n(0) as isize);
            t.goto_col(0);
        }
        ([], b'F') => {
            t.move_rows(-(n(0) as isize));
            t.goto_col(0);
        }
        ([], b'G' | b'`') => t.goto_col(n(0) - 1),
        ([], b'H' | b'f') => t.goto(n(1) - 1, n(0) - 1),
        ([], b'I') => t.tab(n(0)),
        ([], b'J') => t.erase_display(p.raw(0)),
        ([b'?'], b'J') => t.erase_display(p.raw(0)),
        ([], b'K') => t.erase_line(p.raw(0)),
        ([b'?'], b'K') => t.erase_line(p.raw(0)),
        ([], b'L') => t.insert_lines(n(0)),
        ([], b'M') => t.delete_lines(n(0)),
        ([], b'P') => t.delete_chars(n(0)),
        ([], b'S') => t.scroll_up(n(0), false),
        ([], b'T') => t.scroll_down(n(0)),
        ([], b'X') => t.erase_chars(n(0)),
        ([], b'Z') => t.back_tab(n(0)),
        ([], b'b') => t.repeat_last(n(0)),
        ([], b'c') if p.raw(0) == 0 => t.reply(DA1),
        ([b'>'], b'c') if p.raw(0) == 0 => t.reply(DA2),
        ([b'='], b'c') if p.raw(0) == 0 => t.reply(DA3),
        ([], b'd') => {
            let x = t.cursor().0;
            t.goto(x, n(0) - 1);
        }
        ([], b'g') => match p.raw(0) {
            0 => t.clear_tabs(false),
            3 => t.clear_tabs(true),
            _ => {}
        },
        ([], b'h') => set_ansi_modes(t, p, true),
        ([], b'l') => set_ansi_modes(t, p, false),
        ([b'?'], b'h') => set_private_modes(t, p, true),
        ([b'?'], b'l') => set_private_modes(t, p, false),
        ([], b'm') => sgr(t, p),
        ([], b'n') => device_status(t, p.raw(0)),
        ([b'?'], b'n') => private_status(t, p.raw(0)),
        ([], b'r') => t.set_margins(usize::from(p.raw(0)), usize::from(p.raw(1))),
        ([], b's') => t.save_cursor(),
        ([], b't') => window_ops(t, p),
        ([], b'u') => t.restore_cursor(),
        ([b' '], b'q') => t.set_cursor_style(p.raw(0)),
        ([b'!'], b'p') => t.soft_reset(),
        ([b'?', b'$'], b'p') => report_private_mode(t, p.raw(0)),
        ([b'$'], b'p') => report_ansi_mode(t, p.raw(0)),
        ([b'>'], b'q') if p.raw(0) == 0 => t.reply(XTVERSION),
        ([b'?'], b'u') => {
            let flags = t.kitty_flags();
            let mut out = Vec::new();
            let _ = write!(out, "\x1b[?{flags}u");
            t.reply(&out);
        }
        ([b'>'], b'u') => t.push_kitty(p.raw(0) as u8),
        ([b'<'], b'u') => {
            let count = usize::from(p.get(0, 1));
            let stack = t.kitty_stack();
            stack.truncate(stack.len().saturating_sub(count));
            t.event(Event::KeyboardModeChanged);
        }
        ([b'='], b'u') => {
            let flags = p.raw(0) as u8;
            let mode = p.get(1, 1);
            let stack = t.kitty_stack();
            if stack.is_empty() {
                stack.push(0);
            }
            let top = stack.last_mut().unwrap();
            match mode {
                1 => *top = flags,
                2 => *top |= flags,
                3 => *top &= !flags,
                _ => {}
            }
            t.event(Event::KeyboardModeChanged);
        }
        _ => {}
    }
}

fn set_ansi_modes(t: &mut Terminal, p: &Params, on: bool) {
    for g in p.iter() {
        match g[0] {
            4 => t.modes_mut().insert = on,
            20 => t.modes_mut().newline = on,
            _ => {}
        }
    }
}

fn set_private_modes(t: &mut Terminal, p: &Params, on: bool) {
    for g in p.iter() {
        set_private_mode(t, g[0], on);
    }
}

fn set_mouse(t: &mut Terminal, mode: MouseMode, on: bool) {
    let m = t.modes_mut();
    if on {
        m.mouse = mode;
    } else if m.mouse == mode {
        m.mouse = MouseMode::Off;
    }
    t.event(Event::ReportModesChanged);
}

fn set_private_mode(t: &mut Terminal, mode: u16, on: bool) {
    match mode {
        1 => t.modes_mut().app_cursor = on,
        5 => t.modes_mut().reverse_video = on,
        6 => {
            t.modes_mut().origin = on;
            t.goto(0, 0);
        }
        7 => t.modes_mut().autowrap = on,
        9 => set_mouse(t, MouseMode::X10, on),
        12 => t.modes_mut().cursor_blink = on,
        25 => t.modes_mut().cursor_visible = on,
        47 | 1047 => {
            if on {
                t.enter_alt(false, false);
            } else {
                if mode == 1047 && t.alt_screen() {
                    t.erase_display(2);
                }
                t.leave_alt(false);
            }
        }
        66 => t.modes_mut().app_keypad = on,
        1000 => set_mouse(t, MouseMode::Click, on),
        1002 => set_mouse(t, MouseMode::Drag, on),
        1003 => set_mouse(t, MouseMode::Motion, on),
        1004 => {
            t.modes_mut().focus_events = on;
            t.event(Event::ReportModesChanged);
        }
        1006 => t.modes_mut().mouse_sgr = on,
        1007 => t.modes_mut().alternate_scroll = on,
        1048 => {
            if on {
                t.save_cursor()
            } else {
                t.restore_cursor()
            }
        }
        1049 => {
            if on {
                t.enter_alt(true, true);
            } else {
                t.leave_alt(true);
            }
        }
        2004 => t.modes_mut().bracketed_paste = on,
        2026 => t.modes_mut().synchronized = on,
        2027 => t.modes_mut().grapheme_clusters = on,
        2031 => t.modes_mut().color_scheme_reports = on,
        _ => {}
    }
}

/// DECRQM state of a private mode: 1 set, 2 reset, 0 not recognised.
fn private_mode_state(t: &Terminal, mode: u16) -> u8 {
    let m = t.modes();
    let on = match mode {
        1 => m.app_cursor,
        5 => m.reverse_video,
        6 => m.origin,
        7 => m.autowrap,
        9 => m.mouse == MouseMode::X10,
        12 => m.cursor_blink,
        25 => m.cursor_visible,
        47 | 1047 | 1049 => t.alt_screen(),
        66 => m.app_keypad,
        1000 => m.mouse == MouseMode::Click,
        1002 => m.mouse == MouseMode::Drag,
        1003 => m.mouse == MouseMode::Motion,
        1004 => m.focus_events,
        1006 => m.mouse_sgr,
        1007 => m.alternate_scroll,
        2004 => m.bracketed_paste,
        2026 => m.synchronized,
        2027 => m.grapheme_clusters,
        2031 => m.color_scheme_reports,
        _ => return 0,
    };
    if on { 1 } else { 2 }
}

fn report_private_mode(t: &mut Terminal, mode: u16) {
    let state = private_mode_state(t, mode);
    let mut out = Vec::new();
    let _ = write!(out, "\x1b[?{mode};{state}$y");
    t.reply(&out);
}

fn report_ansi_mode(t: &mut Terminal, mode: u16) {
    let state = match mode {
        4 => 2 - u8::from(t.modes().insert),
        20 => 2 - u8::from(t.modes().newline),
        _ => 0,
    };
    let mut out = Vec::new();
    let _ = write!(out, "\x1b[{mode};{state}$y");
    t.reply(&out);
}

fn cursor_report(t: &Terminal) -> (usize, usize) {
    let (x, y) = t.cursor();
    let y = if t.modes().origin { y - t.top } else { y };
    (x + 1, y + 1)
}

fn device_status(t: &mut Terminal, ps: u16) {
    match ps {
        5 => t.reply(b"\x1b[0n"),
        6 => {
            let (x, y) = cursor_report(t);
            let mut out = Vec::new();
            let _ = write!(out, "\x1b[{y};{x}R");
            t.reply(&out);
        }
        _ => {}
    }
}

fn private_status(t: &mut Terminal, ps: u16) {
    match ps {
        6 => {
            let (x, y) = cursor_report(t);
            let mut out = Vec::new();
            let _ = write!(out, "\x1b[?{y};{x}R");
            t.reply(&out);
        }
        996 => t.report_color_scheme(),
        _ => {}
    }
}

fn window_ops(t: &mut Terminal, p: &Params) {
    let (cw, ch) = t.cell_pixels();
    let (cols, lines) = (t.cols(), t.lines());
    let mut out = Vec::new();
    match p.raw(0) {
        14 => {
            let _ = write!(out, "\x1b[4;{};{}t", lines * usize::from(ch), cols * usize::from(cw));
        }
        16 => {
            let _ = write!(out, "\x1b[6;{ch};{cw}t");
        }
        18 => {
            let _ = write!(out, "\x1b[8;{lines};{cols}t");
        }
        22 => t.push_title(),
        23 => t.pop_title(),
        _ => {}
    }
    t.reply(&out);
}

/// Reads an extended colour (38/48/58) from a colon group (`38:2::r:g:b`)
/// or from the parameters after it (`38;2;r;g;b`).
fn extended_color<'a>(group: &[u16], rest: &mut impl Iterator<Item = &'a [u16]>) -> Option<Color> {
    let byte = |v: u16| v.min(255) as u8;
    if group.len() > 1 {
        return match group[1] {
            5 => group.get(2).map(|&i| Color::Indexed(byte(i))),
            2 => {
                let c = if group.len() >= 6 { &group[3..6] } else { group.get(2..5)? };
                Some(Color::Rgb(byte(c[0]), byte(c[1]), byte(c[2])))
            }
            _ => None,
        };
    }
    match rest.next()?[0] {
        5 => rest.next().map(|g| Color::Indexed(byte(g[0]))),
        2 => {
            let r = rest.next()?[0];
            let g = rest.next()?[0];
            let b = rest.next()?[0];
            Some(Color::Rgb(byte(r), byte(g), byte(b)))
        }
        _ => None,
    }
}

fn sgr(t: &mut Terminal, p: &Params) {
    let mut s: Style = *t.pen();
    if p.is_empty() {
        s = Style { link: s.link, ..Style::default() };
    }
    let mut it = p.iter();
    while let Some(g) = it.next() {
        let a = &mut s.attrs;
        match g[0] {
            0 => s = Style { link: s.link, ..Style::default() },
            1 => a.set(Attrs::BOLD, true),
            2 => a.set(Attrs::DIM, true),
            3 => a.set(Attrs::ITALIC, true),
            4 => {
                s.underline = match g.get(1) {
                    None | Some(1) => Underline::Single,
                    Some(0) => Underline::None,
                    Some(2) => Underline::Double,
                    Some(3) => Underline::Curly,
                    Some(4) => Underline::Dotted,
                    Some(5) => Underline::Dashed,
                    Some(_) => s.underline,
                }
            }
            5 | 6 => a.set(Attrs::BLINK, true),
            7 => a.set(Attrs::REVERSE, true),
            8 => a.set(Attrs::HIDDEN, true),
            9 => a.set(Attrs::STRIKE, true),
            21 => s.underline = Underline::Double,
            22 => {
                a.set(Attrs::BOLD, false);
                a.set(Attrs::DIM, false);
            }
            23 => a.set(Attrs::ITALIC, false),
            24 => s.underline = Underline::None,
            25 => a.set(Attrs::BLINK, false),
            27 => a.set(Attrs::REVERSE, false),
            28 => a.set(Attrs::HIDDEN, false),
            29 => a.set(Attrs::STRIKE, false),
            53 => a.set(Attrs::OVERLINE, true),
            55 => a.set(Attrs::OVERLINE, false),
            v @ 30..=37 => s.fg = Color::Indexed((v - 30) as u8),
            38 => {
                if let Some(c) = extended_color(g, &mut it) {
                    s.fg = c;
                }
            }
            39 => s.fg = Color::Default,
            v @ 40..=47 => s.bg = Color::Indexed((v - 40) as u8),
            48 => {
                if let Some(c) = extended_color(g, &mut it) {
                    s.bg = c;
                }
            }
            49 => s.bg = Color::Default,
            58 => {
                if let Some(c) = extended_color(g, &mut it) {
                    s.underline_color = c;
                }
            }
            59 => s.underline_color = Color::Default,
            v @ 90..=97 => s.fg = Color::Indexed((v - 90 + 8) as u8),
            v @ 100..=107 => s.bg = Color::Indexed((v - 100 + 8) as u8),
            _ => {}
        }
    }
    t.set_pen(s);
}

/// The SGR parameters that recreate `s`, for DECRQSS.
pub(crate) fn sgr_params(s: &Style) -> String {
    let mut out = vec!["0".to_string()];
    let a = s.attrs;
    for (flag, code) in [
        (Attrs::BOLD, "1"),
        (Attrs::DIM, "2"),
        (Attrs::ITALIC, "3"),
        (Attrs::BLINK, "5"),
        (Attrs::REVERSE, "7"),
        (Attrs::HIDDEN, "8"),
        (Attrs::STRIKE, "9"),
        (Attrs::OVERLINE, "53"),
    ] {
        if a.contains(flag) {
            out.push(code.into());
        }
    }
    match s.underline {
        Underline::None => {}
        Underline::Single => out.push("4".into()),
        Underline::Double => out.push("4:2".into()),
        Underline::Curly => out.push("4:3".into()),
        Underline::Dotted => out.push("4:4".into()),
        Underline::Dashed => out.push("4:5".into()),
    }
    let color = |c: Color, base: u16, bright: u16, ext: u16| match c {
        Color::Default => None,
        Color::Indexed(i) if i < 8 && base > 0 => Some(format!("{}", base + u16::from(i))),
        Color::Indexed(i) if i < 16 && bright > 0 => Some(format!("{}", bright + u16::from(i) - 8)),
        Color::Indexed(i) => Some(format!("{ext}:5:{i}")),
        Color::Rgb(r, g, b) => Some(format!("{ext}:2::{r}:{g}:{b}")),
    };
    out.extend(color(s.fg, 30, 90, 38));
    out.extend(color(s.bg, 40, 100, 48));
    out.extend(color(s.underline_color, 0, 0, 58));
    out.join(";")
}
