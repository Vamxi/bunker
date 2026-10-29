//! Writes the benchmark workloads to DIR as NAME.bin. Each models a kind of
//! output people produce, so a regression shows against real traffic:
//!
//!   seq      short lines that scroll constantly     (seq, find, logs)
//!   prose    long wrapped lines with a few colours  (compilers, grep)
//!   colors   a colour change on nearly every cell   (ls --color, bat, lolcat)
//!   unicode  CJK, emoji, combining marks            (i18n text, Claude Code)
//!   tui      full-screen cursor-addressed redraws   (btop, vim, htop)
//!
//!   cargo run --release -p bunker-vt --example workloads -- DIR [COPIES]
//!
//! Output is deterministic, so runs compare; COPIES repeats each workload.

use std::fmt::Write;

fn seq(n: usize) -> String {
    (1..=n).map(|i| format!("{i}\r\n")).collect()
}

fn prose(n: usize) -> String {
    "\x1b[32mINFO\x1b[0m the quick brown fox jumps over the lazy dog; pack my box with five dozen liquor jugs 0123456789\r\n"
        .repeat(n)
}

fn colors(n: usize) -> String {
    const TEXT: &str = "rainbow-coloured output, one SGR per character";
    let mut out = String::new();
    for i in 0..n {
        for (j, c) in TEXT.chars().enumerate() {
            let (r, g) = ((i * 7 + j * 11) % 256, (i * 13 + j * 5) % 256);
            let _ = write!(out, "\x1b[38;2;{r};{g};200m{c}");
        }
        out.push_str("\x1b[0m\r\n");
    }
    out
}

fn unicode(n: usize) -> String {
    // Combining marks spelled out: an editor could compose "é" otherwise.
    "日本語のテキスト ✳ Claude Code 👍🏽 👩\u{200d}💻 🇳🇱 e\u{301} a\u{308}\u{301} ⣿⡇ ╭──╮\r\n".repeat(n)
}

fn tui(frames: usize, cols: usize, rows: usize) -> String {
    let mut out = String::new();
    for f in 0..frames {
        out.push_str("\x1b[?2026h\x1b[H");
        for y in 1..=rows {
            let _ = write!(out, "\x1b[{y};1H\x1b[38;5;{}m│", 16 + (y + f) % 200);
            for x in 1..cols - 1 {
                if y == 1 || y == rows {
                    out.push('─');
                } else if x % 10 == 0 {
                    let _ = write!(out, "\x1b[1;33m{}\x1b[22;39m", (x + y + f) % 10);
                } else {
                    out.push(char::from_u32(0x2800 + ((x * y + f) % 256) as u32).unwrap());
                }
            }
            out.push('│');
        }
        out.push_str("\x1b[0m\x1b[?2026l");
    }
    out
}

fn main() {
    let mut args = std::env::args().skip(1);
    let dir = args.next().expect("usage: workloads DIR [COPIES]");
    let copies: usize = args.next().and_then(|c| c.parse().ok()).unwrap_or(1);
    std::fs::create_dir_all(&dir).expect("create the output directory");
    let workloads = [
        ("seq", seq(200_000)),
        ("prose", prose(20_000)),
        ("colors", colors(5_000)),
        ("unicode", unicode(10_000)),
        ("tui", tui(60, 120, 40)),
    ];
    for (name, data) in workloads {
        std::fs::write(format!("{dir}/{name}.bin"), data.repeat(copies)).expect("write a workload");
    }
}
