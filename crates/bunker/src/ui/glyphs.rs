//! Box-drawing, block, and braille characters drawn as shapes. Fonts rarely
//! make line-drawing characters meet exactly at cell edges, and fallback
//! fonts give them foreign widths; drawing them keeps TUI borders (btop,
//! Claude Code, vim splits) seamless at any size.

use gtk::prelude::*;
use gtk::{gdk, graphene, gsk};

/// Arm weights for U+2500-U+257F: 0 none, 1 light, 2 heavy, packed as
/// up | right << 2 | down << 4 | left << 6.
const BOX_ARMS: [u8; 0x80] = {
    const fn arms(u: u8, r: u8, d: u8, l: u8) -> u8 {
        u | r << 2 | d << 4 | l << 6
    }
    let mut t = [0u8; 0x80];
    t[0x00] = arms(0, 1, 0, 1);
    t[0x01] = arms(0, 2, 0, 2);
    t[0x02] = arms(1, 0, 1, 0);
    t[0x03] = arms(2, 0, 2, 0);
    // Corners, tees, and crosses, in Unicode order: (up, right, down, left).
    const SPEC: [(usize, [u8; 4]); 64] = [
        (0x0c, [0, 1, 1, 0]),
        (0x0d, [0, 2, 1, 0]),
        (0x0e, [0, 1, 2, 0]),
        (0x0f, [0, 2, 2, 0]),
        (0x10, [0, 0, 1, 1]),
        (0x11, [0, 0, 1, 2]),
        (0x12, [0, 0, 2, 1]),
        (0x13, [0, 0, 2, 2]),
        (0x14, [1, 1, 0, 0]),
        (0x15, [1, 2, 0, 0]),
        (0x16, [2, 1, 0, 0]),
        (0x17, [2, 2, 0, 0]),
        (0x18, [1, 0, 0, 1]),
        (0x19, [1, 0, 0, 2]),
        (0x1a, [2, 0, 0, 1]),
        (0x1b, [2, 0, 0, 2]),
        (0x1c, [1, 1, 1, 0]),
        (0x1d, [1, 2, 1, 0]),
        (0x1e, [2, 1, 1, 0]),
        (0x1f, [1, 1, 2, 0]),
        (0x20, [2, 1, 2, 0]),
        (0x21, [2, 2, 1, 0]),
        (0x22, [1, 2, 2, 0]),
        (0x23, [2, 2, 2, 0]),
        (0x24, [1, 0, 1, 1]),
        (0x25, [1, 0, 1, 2]),
        (0x26, [2, 0, 1, 1]),
        (0x27, [1, 0, 2, 1]),
        (0x28, [2, 0, 2, 1]),
        (0x29, [2, 0, 1, 2]),
        (0x2a, [1, 0, 2, 2]),
        (0x2b, [2, 0, 2, 2]),
        (0x2c, [0, 1, 1, 1]),
        (0x2d, [0, 1, 1, 2]),
        (0x2e, [0, 2, 1, 1]),
        (0x2f, [0, 2, 1, 2]),
        (0x30, [0, 1, 2, 1]),
        (0x31, [0, 1, 2, 2]),
        (0x32, [0, 2, 2, 1]),
        (0x33, [0, 2, 2, 2]),
        (0x34, [1, 1, 0, 1]),
        (0x35, [1, 1, 0, 2]),
        (0x36, [1, 2, 0, 1]),
        (0x37, [1, 2, 0, 2]),
        (0x38, [2, 1, 0, 1]),
        (0x39, [2, 1, 0, 2]),
        (0x3a, [2, 2, 0, 1]),
        (0x3b, [2, 2, 0, 2]),
        (0x3c, [1, 1, 1, 1]),
        (0x3d, [1, 1, 1, 2]),
        (0x3e, [1, 2, 1, 1]),
        (0x3f, [1, 2, 1, 2]),
        (0x40, [2, 1, 1, 1]),
        (0x41, [1, 1, 2, 1]),
        (0x42, [2, 1, 2, 1]),
        (0x43, [2, 1, 1, 2]),
        (0x44, [2, 2, 1, 1]),
        (0x45, [1, 1, 2, 2]),
        (0x46, [1, 2, 2, 1]),
        (0x47, [2, 2, 1, 2]),
        (0x48, [1, 2, 2, 2]),
        (0x49, [2, 1, 2, 2]),
        (0x4a, [2, 2, 2, 1]),
        (0x4b, [2, 2, 2, 2]),
    ];
    let mut i = 0;
    while i < SPEC.len() {
        let (idx, [u, r, d, l]) = SPEC[i];
        t[idx] = arms(u, r, d, l);
        i += 1;
    }
    const HALVES: [(usize, [u8; 4]); 12] = [
        (0x74, [0, 0, 0, 1]),
        (0x75, [1, 0, 0, 0]),
        (0x76, [0, 1, 0, 0]),
        (0x77, [0, 0, 1, 0]),
        (0x78, [0, 0, 0, 2]),
        (0x79, [2, 0, 0, 0]),
        (0x7a, [0, 2, 0, 0]),
        (0x7b, [0, 0, 2, 0]),
        (0x7c, [0, 2, 0, 1]),
        (0x7d, [1, 0, 2, 0]),
        (0x7e, [0, 1, 0, 2]),
        (0x7f, [2, 0, 1, 0]),
    ];
    let mut i = 0;
    while i < HALVES.len() {
        let (idx, [u, r, d, l]) = HALVES[i];
        t[idx] = arms(u, r, d, l);
        i += 1;
    }
    t
};

/// Quadrant bits for U+2596-U+259F: upper-left 1, upper-right 2,
/// lower-left 4, lower-right 8.
const QUADRANTS: [u8; 10] = [4, 8, 1, 1 | 4 | 8, 1 | 8, 1 | 2 | 4, 1 | 2 | 8, 2, 2 | 4, 2 | 4 | 8];

struct Painter<'a> {
    snap: &'a gtk::Snapshot,
    color: gdk::RGBA,
    x0: f32,
    w: f32,
    h: f32,
    /// Device pixels per logical pixel: lines snap to device pixels, so
    /// neighbouring cells' lines meet without a seam.
    scale: f32,
}

impl Painter<'_> {
    fn floor(&self, v: f32) -> f32 {
        (v * self.scale).floor() / self.scale
    }

    fn ceil(&self, v: f32) -> f32 {
        (v * self.scale).ceil() / self.scale
    }

    fn round(&self, v: f32) -> f32 {
        (v * self.scale).round() / self.scale
    }

    fn rect(&self, x: f32, y: f32, w: f32, h: f32) {
        if w > 0.0 && h > 0.0 {
            self.snap.append_color(&self.color, &graphene::Rect::new(x, y, w, h));
        }
    }

    fn thickness(&self, weight: u8) -> f32 {
        let light = (self.h * self.scale / 18.0).round().max(1.0) / self.scale;
        if weight == 2 { 2.0 * light } else { light }
    }

    fn box_arms(&self, arms: u8) {
        let (up, right, down, left) = (arms & 3, arms >> 2 & 3, arms >> 4 & 3, arms >> 6 & 3);
        let (x0, w, h) = (self.x0, self.w, self.h);
        let cx = x0 + self.floor(w / 2.0);
        let cy = self.floor(h / 2.0);
        let vt = self.thickness(up.max(down));
        let ht = self.thickness(left.max(right));
        if up != 0 {
            let t = self.thickness(up);
            self.rect(cx - self.floor(t / 2.0), 0.0, t, cy + self.ceil(ht / 2.0));
        }
        if down != 0 {
            let t = self.thickness(down);
            let top = if up == 0 && left == 0 && right == 0 { cy } else { cy - self.floor(ht / 2.0) };
            self.rect(cx - self.floor(t / 2.0), top, t, h - top);
        }
        if left != 0 {
            let t = self.thickness(left);
            self.rect(x0, cy - self.floor(t / 2.0), cx - x0 + self.ceil(vt / 2.0), t);
        }
        if right != 0 {
            let t = self.thickness(right);
            let start = if up == 0 && down == 0 && left == 0 { cx } else { cx - self.floor(vt / 2.0) };
            self.rect(start, cy - self.floor(t / 2.0), x0 + w - start, t);
        }
    }

    /// A rounded corner: 0 ╭, 1 ╮, 2 ╯, 3 ╰.
    fn arc(&self, which: usize) {
        let (x0, w, h) = (self.x0, self.w, self.h);
        let t = self.thickness(1);
        // The centre line of a stroke whose left/top edge sits where the
        // straight lines' do.
        let cx = x0 + self.floor(w / 2.0) - self.floor(t / 2.0) + t / 2.0;
        let cy = self.floor(h / 2.0) - self.floor(t / 2.0) + t / 2.0;
        let (sx, sy): (f32, f32) = match which {
            0 => (1.0, 1.0),
            1 => (-1.0, 1.0),
            2 => (-1.0, -1.0),
            _ => (1.0, -1.0),
        };
        let r = self.floor(w.min(h) / 2.0);
        let x_edge = if sx > 0.0 { x0 + w } else { x0 };
        let y_edge = if sy > 0.0 { h } else { 0.0 };
        let b = gsk::PathBuilder::new();
        b.move_to(x_edge, cy);
        b.line_to(cx + sx * r, cy);
        b.quad_to(cx, cy, cx, cy + sy * r);
        b.line_to(cx, y_edge);
        self.snap.append_stroke(&b.to_path(), &gsk::Stroke::new(t), &self.color);
    }

    fn block(&self, ch: u32) -> bool {
        let (x0, w, h) = (self.x0, self.w, self.h);
        let eighth_h = |n: f32| self.round(h * n / 8.0);
        let eighth_w = |n: f32| self.round(w * n / 8.0);
        match ch {
            0x2580 => self.rect(x0, 0.0, w, eighth_h(4.0)),
            0x2581..=0x2588 => {
                let top = h - eighth_h((ch - 0x2580) as f32);
                self.rect(x0, top, w, h - top);
            }
            0x2589..=0x258f => self.rect(x0, 0.0, eighth_w((0x2590 - ch) as f32), h),
            0x2590 => {
                let half = eighth_w(4.0);
                self.rect(x0 + half, 0.0, w - half, h);
            }
            0x2591..=0x2593 => {
                let mut c = self.color;
                c.set_alpha(c.alpha() * (ch - 0x2590) as f32 * 0.25);
                self.snap.append_color(&c, &graphene::Rect::new(x0, 0.0, w, h));
            }
            0x2594 => self.rect(x0, 0.0, w, eighth_h(1.0)),
            0x2595 => {
                let e = eighth_w(1.0);
                self.rect(x0 + w - e, 0.0, e, h);
            }
            0x2596..=0x259f => {
                let q = QUADRANTS[(ch - 0x2596) as usize];
                let (hw, hh) = (eighth_w(4.0), eighth_h(4.0));
                if q & 1 != 0 {
                    self.rect(x0, 0.0, hw, hh);
                }
                if q & 2 != 0 {
                    self.rect(x0 + hw, 0.0, w - hw, hh);
                }
                if q & 4 != 0 {
                    self.rect(x0, hh, hw, h - hh);
                }
                if q & 8 != 0 {
                    self.rect(x0 + hw, hh, w - hw, h - hh);
                }
            }
            _ => return false,
        }
        true
    }

    fn braille(&self, bits: u32) {
        // Dot order: 1 2 3 7 down the left column, 4 5 6 8 down the right.
        const ORDER: [(f32, f32); 8] = [(0., 0.), (0., 1.), (0., 2.), (1., 0.), (1., 1.), (1., 2.), (0., 3.), (1., 3.)];
        let dot = self.round((self.w / 4.0).min(self.h / 8.0)).max(1.0 / self.scale);
        for (i, (col, row)) in ORDER.iter().enumerate() {
            if bits & (1 << i) != 0 {
                let cx = self.x0 + self.w * (0.25 + 0.5 * col);
                let cy = self.h * (0.125 + 0.25 * row);
                self.rect(self.round(cx - dot / 2.0), self.round(cy - dot / 2.0), dot, dot);
            }
        }
    }
}

/// Draws `ch` at `x0` in a cell `w` by `h` and returns true, or returns
/// false for characters the font draws. `scale` is device pixels per
/// logical pixel.
pub fn draw(snap: &gtk::Snapshot, ch: char, x0: f32, w: f32, h: f32, scale: f32, color: gdk::RGBA) -> bool {
    let c = ch as u32;
    let p = Painter { snap, color, x0, w, h, scale };
    match c {
        0x2500..=0x257f => {
            let idx = (c - 0x2500) as usize;
            if BOX_ARMS[idx] != 0 {
                p.box_arms(BOX_ARMS[idx]);
                true
            } else if (0x6d..=0x70).contains(&idx) {
                p.arc(idx - 0x6d);
                true
            } else {
                false
            }
        }
        0x2580..=0x259f => p.block(c),
        0x2800..=0x28ff => {
            p.braille(c - 0x2800);
            true
        }
        _ => false,
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn arms_table() {
        assert_eq!(BOX_ARMS[0x00], 1 << 2 | 1 << 6, "─");
        assert_eq!(BOX_ARMS[0x0c], 1 << 2 | 1 << 4, "┌");
        assert_eq!(BOX_ARMS[0x3c], 1 | 1 << 2 | 1 << 4 | 1 << 6, "┼");
        assert_eq!(BOX_ARMS[0x4b], 2 | 2 << 2 | 2 << 4 | 2 << 6, "╋");
        assert_eq!(BOX_ARMS[0x6d], 0, "╭ is drawn as an arc");
    }
}
