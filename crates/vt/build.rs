//! Generates the table of characters that join whatever printable character
//! comes before them (Grapheme_Cluster_Break Extend, ZWJ, and SpacingMark),
//! from the same segmentation crate the terminal uses at run time, so a
//! character can be joined without running segmentation.

use std::fmt::Write;
use std::path::PathBuf;

use unicode_segmentation::UnicodeSegmentation;
use unicode_width::UnicodeWidthChar;

fn bmp_bit(bmp: &[u64], u: u32) -> bool {
    u < 0x10000 && (bmp[(u / 64) as usize] >> (u % 64)) & 1 != 0
}

fn main() {
    let mut ranges: Vec<(u32, u32)> = Vec::new();
    let mut s = String::new();
    for u in 0x80..=0x10ffffu32 {
        let Some(c) = char::from_u32(u) else { continue };
        s.clear();
        s.push('a');
        s.push(c);
        if s.graphemes(true).count() != 1 {
            continue;
        }
        match ranges.last_mut() {
            Some(r) if r.1 + 1 == u => r.1 = u,
            _ => ranges.push((u, u)),
        }
    }
    // A bitmap for the Basic Multilingual Plane, where nearly all text is,
    // and the ranges for the rest.
    let mut bmp = vec![0u64; 0x10000 / 64];
    for &(a, b) in &ranges {
        for u in a..=b.min(0xffff) {
            bmp[(u / 64) as usize] |= 1 << (u % 64);
        }
    }
    let mut out =
        String::from("/// Bits for U+0000-U+FFFF: characters that join any printable character before them.\n");
    let _ = writeln!(out, "pub(crate) static JOINS_BMP: [u64; {}] = [", bmp.len());
    for w in &bmp {
        let _ = writeln!(out, "    0x{w:x},");
    }
    out.push_str("];\n");
    // One byte per BMP character: the width (bits 0-1), whether it joins
    // anything before it (bit 2), whether what follows it may join it in
    // other ways (bit 3: Prepend, ZWJ, regional indicators, leading Hangul
    // jamo), and whether it joins only some characters (bit 4: regional
    // indicators, Hangul vowel and trailing jamo).
    // Extended_Pictographic, found the same way: "😀 ZWJ c" is one cluster
    // exactly when c is (GB11).
    let mut pict: Vec<(u32, u32)> = Vec::new();
    for u in 0x80..=0x10ffffu32 {
        let Some(c) = char::from_u32(u) else { continue };
        s.clear();
        s.push('\u{1f600}');
        s.push('\u{200d}');
        s.push(c);
        if s.graphemes(true).count() == 1 && !(bmp_bit(&bmp, u)) {
            match pict.last_mut() {
                Some(r) if r.1 + 1 == u => r.1 = u,
                _ => pict.push((u, u)),
            }
        }
    }
    let _ = writeln!(out, "/// Extended_Pictographic ranges (emoji and pictographs).");
    let _ = writeln!(out, "pub(crate) const PICTOGRAPHIC: [(u32, u32); {}] = [", pict.len());
    for (a, b) in &pict {
        let _ = writeln!(out, "    (0x{a:x}, 0x{b:x}),");
    }
    out.push_str("];\n");
    let mut info = vec![0u8; 0x10000];
    let in_any = |u: u32, rs: &[(u32, u32)]| rs.iter().any(|&(a, b)| (a..=b).contains(&u));
    const PREPEND: &[(u32, u32)] =
        &[(0x0600, 0x0605), (0x06dd, 0x06dd), (0x070f, 0x070f), (0x0890, 0x0891), (0x08e2, 0x08e2), (0x0d4e, 0x0d4e)];
    for u in 0..0x10000u32 {
        let Some(c) = char::from_u32(u) else { continue };
        let width = if u < 0x7f { u8::from(u >= 0x20) } else { c.width().unwrap_or(0).min(2) as u8 };
        let joins = (bmp[(u / 64) as usize] >> (u % 64)) & 1;
        let special_prev = u == 0x200d || in_any(u, PREPEND) || in_any(u, &[(0x1100, 0x115f), (0xa960, 0xa97c)]);
        let special_next = in_any(u, &[(0x1160, 0x11ff), (0xd7b0, 0xd7fb)]);
        info[u as usize] = width | (joins as u8) << 2 | u8::from(special_prev) << 3 | u8::from(special_next) << 4;
    }
    let _ = writeln!(out, "/// Width and joining bits for U+0000-U+FFFF (see build.rs).");
    let _ = writeln!(out, "pub(crate) static CHAR_INFO: [u8; 0x10000] = [");
    for chunk in info.chunks(32) {
        let line: Vec<String> = chunk.iter().map(|b| b.to_string()).collect();
        let _ = writeln!(out, "    {},", line.join(", "));
    }
    out.push_str("];\n");
    ranges.retain(|r| r.1 > 0xffff);
    for r in &mut ranges {
        r.0 = r.0.max(0x10000);
    }
    out.push_str("/// Ranges above U+FFFF of characters that join any printable character before them.\n");
    let _ = writeln!(out, "pub(crate) const JOINS_ANY: [(u32, u32); {}] = [", ranges.len());
    for (a, b) in ranges {
        let _ = writeln!(out, "    (0x{a:x}, 0x{b:x}),");
    }
    out.push_str("];\n");
    let path = PathBuf::from(std::env::var("OUT_DIR").unwrap()).join("joins.rs");
    std::fs::write(path, out).unwrap();
    println!("cargo::rerun-if-changed=build.rs");
}
