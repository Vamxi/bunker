//! Character widths and grapheme joining.

use unicode_segmentation::UnicodeSegmentation;
use unicode_width::{UnicodeWidthChar, UnicodeWidthStr};

/// Display width of a single character: 0, 1, or 2 cells.
#[inline]
pub fn char_width(c: char) -> usize {
    let u = c as u32;
    if u < 0x10000 {
        return usize::from(CHAR_INFO[u as usize] & 3);
    }
    c.width().unwrap_or(0).min(2)
}

/// The width of `c`, when it certainly starts a new cluster after `prev`
/// (which it does for nearly all text); `None` when it might join.
#[inline]
pub fn simple_width(prev: char, c: char) -> Option<usize> {
    let (u, p) = (c as u32, prev as u32);
    if u < 0x10000 && p < 0x10000 {
        let (ic, ip) = (CHAR_INFO[u as usize], CHAR_INFO[p as usize]);
        let w = ic & 3;
        return (w != 0 && ic & 0b10100 == 0 && ip & 0b1000 == 0).then_some(usize::from(w));
    }
    let w = char_width(c);
    (w != 0 && !may_join(prev, c)).then_some(w)
}

/// Display width of a grapheme cluster: 1 or 2 cells.
pub fn cluster_width(cluster: &str) -> usize {
    cluster.width().clamp(1, 2)
}

fn in_ranges(u: u32, ranges: &[(u32, u32)]) -> bool {
    ranges.iter().any(|&(lo, hi)| (lo..=hi).contains(&u))
}

/// Grapheme_Cluster_Break=Prepend: these join the character after them.
const PREPEND: &[(u32, u32)] = &[
    (0x0600, 0x0605),
    (0x06dd, 0x06dd),
    (0x070f, 0x070f),
    (0x0890, 0x0891),
    (0x08e2, 0x08e2),
    (0x0d4e, 0x0d4e),
    (0x110bd, 0x110bd),
    (0x110cd, 0x110cd),
    (0x111c2, 0x111c3),
    (0x1193f, 0x1193f),
    (0x11941, 0x11941),
    (0x11a3a, 0x11a3a),
    (0x11a84, 0x11a89),
    (0x11d46, 0x11d46),
    (0x11f02, 0x11f02),
];

const HANGUL_L: &[(u32, u32)] = &[(0x1100, 0x115f), (0xa960, 0xa97c)];

/// Hangul vowel and trailing jamo, which join only some Hangul.
const HANGUL_VT: &[(u32, u32)] = &[(0x1160, 0x11ff), (0xd7b0, 0xd7fb)];

include!(concat!(env!("OUT_DIR"), "/joins.rs"));

/// Reports whether `c` joins any printable character before it: combining
/// marks, variation selectors, emoji modifiers, ZWJ, and spacing marks.
#[inline]
pub fn joins_any(c: char) -> bool {
    let u = c as u32;
    if u < 0x10000 {
        return JOINS_BMP[(u / 64) as usize] >> (u % 64) & 1 != 0;
    }
    JOINS_ANY
        .binary_search_by(|&(lo, hi)| {
            if hi < u {
                std::cmp::Ordering::Less
            } else if lo > u {
                std::cmp::Ordering::Greater
            } else {
                std::cmp::Ordering::Equal
            }
        })
        .is_ok()
}

fn is_pictographic(c: char) -> bool {
    let u = c as u32;
    PICTOGRAPHIC
        .binary_search_by(|&(lo, hi)| {
            if hi < u {
                std::cmp::Ordering::Less
            } else if lo > u {
                std::cmp::Ordering::Greater
            } else {
                std::cmp::Ordering::Equal
            }
        })
        .is_ok()
}

fn is_regional(u: u32) -> bool {
    (0x1f1e6..=0x1f1ff).contains(&u)
}

/// Reports whether `c` might join a cluster ending in `prev`. False means
/// it certainly starts a new cluster (UAX #29); true leaves the answer to
/// [`joins_cluster`].
#[inline]
pub fn may_join(prev: char, c: char) -> bool {
    let (u, p) = (c as u32, prev as u32);
    if ((0x600..=0xd4e).contains(&p) || (0x110bd..=0x11f02).contains(&p)) && in_ranges(p, PREPEND) {
        return true;
    }
    if u < 0x300 {
        return false;
    }
    if p == 0x200d || joins_any(c) {
        return true;
    }
    if is_regional(u) {
        return is_regional(p);
    }
    in_ranges(p, HANGUL_L) || in_ranges(u, HANGUL_VT)
}

/// Reports whether `c` joins the cluster `prev` (UAX #29). `scratch` is a
/// reusable buffer.
pub fn joins_cluster(prev: &str, c: char, scratch: &mut String) -> bool {
    let Some(last) = prev.chars().last() else { return false };
    if !may_join(last, c) {
        return false;
    }
    if joins_any(c) {
        return true;
    }
    // Flags are pairs of regional indicators.
    if is_regional(c as u32) && prev.chars().all(|p| is_regional(p as u32)) {
        return prev.chars().count() % 2 == 1;
    }
    // Emoji ZWJ sequences (GB11): a pictograph, extenders, ZWJ, pictograph.
    if prev.ends_with('\u{200d}') && is_pictographic(c) && prev.chars().next().is_some_and(is_pictographic) {
        let middle = &prev[prev.chars().next().map_or(0, char::len_utf8)..prev.len() - 3];
        if middle.chars().all(joins_any) {
            return true;
        }
    }
    scratch.clear();
    scratch.push_str(prev);
    scratch.push(c);
    let mut clusters = scratch.graphemes(true);
    clusters.next();
    clusters.next().is_none()
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn widths() {
        let cases = [('a', 1), ('\u{7}', 0), ('漢', 2), ('\u{301}', 0), ('😀', 2), ('é', 1)];
        for (c, w) in cases {
            assert_eq!(char_width(c), w, "{c:?}");
        }
    }

    #[test]
    fn joining() {
        let mut s = String::new();
        let cases = [
            ("e", '\u{301}', true),
            ("e", 'f', false),
            ("👨", '\u{200d}', true),
            ("👨\u{200d}", '👩', true),
            ("🇳", '🇱', true),
            ("🇳🇱", '🇩', false),
            ("❤", '\u{fe0f}', true),
            ("漢", '字', false),
            ("👍", '\u{1f3fd}', true),
            ("क", '\u{93f}', true),
            ("⣿", '⡇', false),
            ("─", '─', false),
            ("\u{600}", '1', true),
            ("\u{1100}", '\u{ac00}', true),
        ];
        for (prev, c, want) in cases {
            assert_eq!(joins_cluster(prev, c, &mut s), want, "{prev:?} + {c:?}");
        }
    }

    #[test]
    fn cluster_widths() {
        assert_eq!(cluster_width("❤\u{fe0f}"), 2);
        assert_eq!(cluster_width("👨\u{200d}👩\u{200d}👧"), 2);
        assert_eq!(cluster_width("e\u{301}"), 1);
        assert_eq!(cluster_width("🇳🇱"), 2);
    }
}

#[cfg(test)]
mod exhaustive {
    use super::*;

    /// The fast check never says "starts a cluster" when segmentation
    /// would join: every character after a sample of cluster ends.
    #[test]
    fn may_join_agrees_with_segmentation() {
        let prevs = ['a', 'e', '漢', '👍', '🇳', '\u{200d}', 'क', '\u{600}', '\u{1100}', '⣿', '─', 'ก', '\u{1f468}'];
        let mut s = String::new();
        for p in prevs {
            for u in 0x20..0x3_0000u32 {
                let Some(c) = char::from_u32(u) else { continue };
                if c.is_control() || may_join(p, c) {
                    continue;
                }
                s.clear();
                s.push(p);
                s.push(c);
                assert_eq!(s.graphemes(true).count(), 2, "{p:?} + {c:?} (U+{u:04X}) joins");
            }
        }
    }
}

#[cfg(test)]
mod table {
    use super::*;

    /// The generated table matches segmentation for every character.
    #[test]
    fn joins_any_matches_segmentation() {
        let mut s = String::new();
        for u in 0x80..=0x10ffffu32 {
            let Some(c) = char::from_u32(u) else { continue };
            s.clear();
            s.push('a');
            s.push(c);
            assert_eq!(joins_any(c), s.graphemes(true).count() == 1, "U+{u:04X}");
        }
    }

    #[test]
    fn zwj_sequences() {
        let mut s = String::new();
        for (prev, c, want) in [
            ("👩\u{200d}", '💻', true),
            ("👍\u{1f3fd}\u{200d}", '💻', true),
            ("a\u{200d}", '💻', false),
            ("👩\u{200d}", 'x', false),
        ] {
            assert_eq!(joins_cluster(prev, c, &mut s), want, "{prev:?} + {c:?}");
        }
    }
}
