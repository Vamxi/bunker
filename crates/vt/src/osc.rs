//! Operating system commands (OSC) and device control strings (DCS).

use std::io::Write;

use crate::colors::Rgb;
use crate::grid::RowFlags;
use crate::parser::Params;
use crate::term::{Event, Terminal};

/// The terminal name reported to XTGETTCAP.
const TERM_NAME: &str = "xterm-256color";

pub(crate) fn dispatch(t: &mut Terminal, data: &[u8], bell: bool) {
    if let Some(body) = data.strip_prefix(b"1337;") {
        t.iterm_image(body);
        return;
    }
    let text = String::from_utf8_lossy(data);
    let (ps, rest) = text.split_once(';').unwrap_or((&text, ""));
    let st: &[u8] = if bell { b"\x07" } else { b"\x1b\\" };
    match ps {
        "0" | "2" => t.set_title(rest),
        "4" => set_palette(t, rest, st),
        "7" => {
            if let Some(path) = file_url_path(rest) {
                t.set_cwd(path);
            }
        }
        "8" => {
            let uri = rest.split_once(';').map_or("", |(_, u)| u);
            t.set_link(uri);
        }
        "9" => notify_conemu(t, rest),
        "10" | "11" | "12" => {
            let first: u16 = ps.parse().unwrap_or(10);
            for (i, spec) in rest.split(';').enumerate() {
                dynamic_color(t, first + i as u16, spec, st);
            }
        }
        "52" => clipboard(t, rest),
        "104" => {
            if rest.is_empty() {
                t.palette.reset_all_indexed();
            } else {
                for i in rest.split(';').filter_map(|s| s.parse::<u8>().ok()) {
                    t.palette.set_indexed(i, None);
                }
            }
        }
        "110" => t.palette.set_dynamic(10, None),
        "111" => t.palette.set_dynamic(11, None),
        "112" => t.palette.set_dynamic(12, None),
        "133" => semantic_prompt(t, rest),
        "777" => {
            let mut parts = rest.splitn(3, ';');
            if parts.next() == Some("notify") {
                let title = parts.next().unwrap_or("").to_string();
                let body = parts.next().unwrap_or("").to_string();
                t.event(Event::Notify { title, body });
            }
        }
        "99" => kitty_notify(t, rest),
        _ => {}
    }
}

fn set_palette(t: &mut Terminal, rest: &str, st: &[u8]) {
    let mut parts = rest.split(';');
    while let (Some(idx), Some(spec)) = (parts.next(), parts.next()) {
        let Ok(i) = idx.parse::<u8>() else { continue };
        if spec == "?" {
            let c = t.palette.indexed(i);
            let mut out = Vec::new();
            let _ = write!(out, "\x1b]4;{i};{}", c.x11());
            out.extend_from_slice(st);
            t.reply(&out);
        } else if let Some(c) = Rgb::parse(spec) {
            t.palette.set_indexed(i, Some(c));
        }
    }
}

fn dynamic_color(t: &mut Terminal, which: u16, spec: &str, st: &[u8]) {
    if spec == "?" {
        let c = match which {
            10 => Some(t.palette.fg()),
            11 => Some(t.palette.bg()),
            12 => Some(t.palette.cursor().unwrap_or(t.palette.fg())),
            _ => None,
        };
        if let Some(c) = c {
            let mut out = Vec::new();
            let _ = write!(out, "\x1b]{which};{}", c.x11());
            out.extend_from_slice(st);
            t.reply(&out);
        }
    } else if let Some(c) = Rgb::parse(spec) {
        t.palette.set_dynamic(which, Some(c));
    }
}

/// The path of a `file://host/path` URL, percent-decoded.
fn file_url_path(url: &str) -> Option<String> {
    let rest = url.strip_prefix("file://")?;
    let path = &rest[rest.find('/')?..];
    let bytes = path.as_bytes();
    let mut out = Vec::with_capacity(bytes.len());
    let mut i = 0;
    while i < bytes.len() {
        if bytes[i] == b'%'
            && i + 2 < bytes.len()
            && let Ok(v) = u8::from_str_radix(&path[i + 1..i + 3], 16)
        {
            out.push(v);
            i += 3;
            continue;
        }
        out.push(bytes[i]);
        i += 1;
    }
    String::from_utf8(out).ok()
}

fn notify_conemu(t: &mut Terminal, rest: &str) {
    // OSC 9;4;state;percent is progress; other numbered OSC 9 forms are
    // ConEmu commands, not notifications.
    if let Some(progress) = rest.strip_prefix("4;").or((rest == "4").then_some("")) {
        let mut it = progress.split(';');
        let state = it.next().and_then(|s| s.parse().ok()).unwrap_or(0);
        let percent = it.next().and_then(|s| s.parse::<u8>().ok()).unwrap_or(0).min(100);
        t.event(Event::Progress { state, percent });
        return;
    }
    if rest.split(';').next().is_some_and(|s| !s.is_empty() && s.bytes().all(|b| b.is_ascii_digit())) {
        return;
    }
    t.event(Event::Notify { title: String::new(), body: rest.to_string() });
}

fn kitty_notify(t: &mut Terminal, rest: &str) {
    let (meta, payload) = rest.split_once(';').unwrap_or((rest, ""));
    let mut done = true;
    let mut body = true;
    let mut base64 = false;
    for kv in meta.split(':') {
        match kv.split_once('=') {
            Some(("d", v)) => done = v != "0",
            Some(("p", v)) => body = v != "title",
            Some(("e", v)) => base64 = v == "1",
            _ => {}
        }
    }
    let text = if base64 {
        base64_decode(payload.as_bytes()).map(|b| String::from_utf8_lossy(&b).into_owned()).unwrap_or_default()
    } else {
        payload.to_string()
    };
    let pending = t.notification_mut();
    if body {
        pending.1.push_str(&text);
    } else {
        pending.0.push_str(&text);
    }
    if done {
        let (title, body) = std::mem::take(t.notification_mut());
        if !title.is_empty() || !body.is_empty() {
            t.event(Event::Notify { title, body });
        }
    }
}

fn clipboard(t: &mut Terminal, rest: &str) {
    let Some((_selection, data)) = rest.split_once(';') else { return };
    // Reading the clipboard ("?") is never answered: any program, including
    // one on a remote host, could read it.
    if data == "?" {
        return;
    }
    if let Some(bytes) = base64_decode(data.as_bytes()) {
        t.event(Event::Clipboard(String::from_utf8_lossy(&bytes).into_owned()));
    }
}

fn semantic_prompt(t: &mut Terminal, rest: &str) {
    let mut parts = rest.split(';');
    match parts.next() {
        Some("A") => {
            t.mark_row(RowFlags::PROMPT);
            t.event(Event::PromptStart);
        }
        Some("C") => {
            t.mark_row(RowFlags::OUTPUT);
            t.event(Event::CommandStart);
        }
        Some("D") => {
            let exit = parts.next().and_then(|s| s.parse().ok());
            t.finish_command(exit);
        }
        _ => {}
    }
}

/// Decodes standard base64, ignoring whitespace and padding.
pub fn base64_decode(input: &[u8]) -> Option<Vec<u8>> {
    let mut out = Vec::with_capacity(input.len() * 3 / 4);
    let mut acc = 0u32;
    let mut bits = 0;
    for &c in input {
        let v = match c {
            b'A'..=b'Z' => c - b'A',
            b'a'..=b'z' => c - b'a' + 26,
            b'0'..=b'9' => c - b'0' + 52,
            b'+' | b'-' => 62,
            b'/' | b'_' => 63,
            b'=' | b' ' | b'\n' | b'\r' | b'\t' => continue,
            _ => return None,
        };
        acc = (acc << 6) | u32::from(v);
        bits += 6;
        if bits >= 8 {
            bits -= 8;
            out.push((acc >> bits) as u8);
        }
    }
    Some(out)
}

pub(crate) fn dcs(t: &mut Terminal, params: &Params, inter: &[u8], action: u8, data: &[u8]) {
    match (inter, action) {
        ([], b'q') => t.sixel(data, params.raw(1) == 1),
        ([b'$'], b'q') => request_setting(t, data),
        ([b'+'], b'q') => get_termcap(t, data),
        _ => {}
    }
}

/// DECRQSS: reports SGR, scroll margins, and the cursor style.
fn request_setting(t: &mut Terminal, data: &[u8]) {
    let mut out = Vec::new();
    match data {
        b"m" => {
            let _ = write!(out, "\x1bP1$r{}m\x1b\\", crate::csi::sgr_params(t.pen()));
        }
        b"r" => {
            let _ = write!(out, "\x1bP1$r{};{}r\x1b\\", t.top + 1, t.bottom + 1);
        }
        b" q" => {
            let _ = write!(out, "\x1bP1$r{} q\x1b\\", t.cursor_style_param());
        }
        _ => out.extend_from_slice(b"\x1bP0$r\x1b\\"),
    }
    t.reply(&out);
}

fn hex_decode(s: &[u8]) -> Option<String> {
    if !s.len().is_multiple_of(2) {
        return None;
    }
    let bytes: Option<Vec<u8>> =
        s.chunks(2).map(|p| std::str::from_utf8(p).ok().and_then(|h| u8::from_str_radix(h, 16).ok())).collect();
    String::from_utf8(bytes?).ok()
}

fn hex_encode(s: &str) -> String {
    s.bytes().map(|b| format!("{b:02X}")).collect()
}

/// XTGETTCAP: answers the capabilities programs ask for to detect colour
/// and underline support.
fn get_termcap(t: &mut Terminal, data: &[u8]) {
    for name in data.split(|&b| b == b';') {
        let Some(cap) = hex_decode(name) else { continue };
        let value: Option<&str> = match cap.as_str() {
            "TN" | "name" => Some(TERM_NAME),
            "Co" | "colors" => Some("256"),
            "RGB" => Some("8/8/8"),
            "Smulx" => Some("\x1b[4:%p1%dm"),
            "Setulc" => Some("\x1b[58:2::%p1%{65536}%/%d:%p1%{256}%/%{255}%&%d:%p1%{255}%&%d%;m"),
            "Su" => Some(""),
            _ => None,
        };
        let mut out = Vec::new();
        match value {
            Some("") => {
                let _ = write!(out, "\x1bP1+r{}\x1b\\", hex_encode(&cap));
            }
            Some(v) => {
                let _ = write!(out, "\x1bP1+r{}={}\x1b\\", hex_encode(&cap), hex_encode(v));
            }
            None => {
                let _ = write!(out, "\x1bP0+r{}\x1b\\", hex_encode(&cap));
            }
        }
        t.reply(&out);
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn base64() {
        assert_eq!(base64_decode(b"aGVsbG8=").unwrap(), b"hello");
        assert_eq!(base64_decode(b"aGVsbG8gd29ybGQ").unwrap(), b"hello world");
        assert!(base64_decode(b"a!").is_none());
    }

    #[test]
    fn file_urls() {
        assert_eq!(file_url_path("file://host/home/me/a%20b").as_deref(), Some("/home/me/a b"));
        assert_eq!(file_url_path("file:///tmp").as_deref(), Some("/tmp"));
        assert_eq!(file_url_path("http://x/y"), None);
    }
}
