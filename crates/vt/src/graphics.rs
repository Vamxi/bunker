//! Inline images: SIXEL (DCS q), the kitty graphics protocol (APC G), and
//! iTerm2 inline files (OSC 1337). Images are decoded to RGBA and shown as
//! pixels over the cells they cover; each lives on the row where it starts,
//! so it scrolls, reflows, and is erased with that row.
//!
//! PNG, JPEG, GIF, and zlib need codecs this crate does not carry; the
//! frontend supplies them through [`Codec`].

use std::collections::HashMap;
use std::sync::Arc;
use std::sync::atomic::{AtomicU64, Ordering};

use crate::osc::base64_decode;

/// The longest graphics control or multipart transfer kept.
pub const MAX_SEQUENCE: usize = 8 << 20;
/// The most pixels a decoded image may have.
pub const MAX_PIXELS: usize = 4 << 20;
/// The largest image width or height, and SIXEL repeat count.
pub const MAX_DIMENSION: usize = 4096;
/// The most bytes of kitty images kept for reuse.
pub const MAX_CACHE_BYTES: usize = 32 << 20;
const MAX_IMAGES: usize = 32;
const MAX_CELLS: usize = 256 << 10;

static NEXT_IMAGE: AtomicU64 = AtomicU64::new(1);

/// Image decoders the frontend provides.
pub trait Codec: Send {
    /// Decodes a PNG, JPEG, or GIF (first frame) to RGBA rows.
    fn decode(&self, data: &[u8]) -> Option<(u32, u32, Vec<u8>)>;
    /// Inflates zlib data, giving up past `limit` bytes.
    fn inflate(&self, data: &[u8], limit: usize) -> Option<Vec<u8>>;
}

/// Decoded pixels: RGBA rows, not premultiplied.
#[derive(Debug)]
pub struct Image {
    /// Width in pixels.
    pub width: u32,
    /// Height in pixels.
    pub height: u32,
    /// `width * height * 4` bytes.
    pub rgba: Vec<u8>,
    /// Unique for the process, so a frontend can cache what it made of it.
    pub id: u64,
}

impl Image {
    fn new(width: u32, height: u32, rgba: Vec<u8>) -> Arc<Self> {
        Arc::new(Self { width, height, rgba, id: NEXT_IMAGE.fetch_add(1, Ordering::Relaxed) })
    }
}

/// An image shown on the grid, from the row it is stored on.
#[derive(Clone, Debug)]
pub struct Placement {
    /// The pixels.
    pub image: Arc<Image>,
    /// The part of the image shown: x, y, width, height in image pixels.
    pub src: [u32; 4],
    /// The first column covered.
    pub col: usize,
    /// Columns and rows covered.
    pub cols: usize,
    /// See `cols`.
    pub rows: usize,
    /// The shown size in cell-pixel units, and its offset into the first cell.
    pub size: [u32; 2],
    /// See `size`.
    pub offset: [u32; 2],
    /// The kitty image id and placement id, for deletion.
    pub kitty: Option<(u32, u32)>,
}

/// The cell size images are measured in.
#[derive(Clone, Copy, Debug)]
pub struct CellSize {
    /// Pixels per cell across.
    pub width: u32,
    /// Pixels per cell down.
    pub height: u32,
    /// The terminal's size in cells.
    pub cols: usize,
    /// See `cols`.
    pub rows: usize,
}

/// What a graphics command asks the terminal to do.
#[derive(Debug, Default)]
pub struct Outcome {
    /// An image to place at the cursor.
    pub place: Option<Placement>,
    /// Leave the cursor below the image at its left (SIXEL, iTerm2), rather
    /// than after it (kitty, unless C=1 keeps it in place).
    pub inline: bool,
    /// Move the cursor at all.
    pub moves: bool,
    /// Placements to remove, as a kitty selector.
    pub delete: Option<Delete>,
    /// A reply for the program.
    pub reply: Option<String>,
}

/// Which placements a kitty delete removes.
#[derive(Clone, Copy, Debug, PartialEq, Eq)]
pub enum Delete {
    /// Every placement.
    All,
    /// Placements of image `id` (one placement when `pid` is set).
    Image(u32, u32),
    /// Placements covering a cell (x, y are 1-based screen coordinates).
    Cell(usize, usize),
    /// Placements covering a column.
    Column(usize),
    /// Placements covering a row.
    Row(usize),
    /// Images with ids in a range.
    Range(u32, u32),
}

struct Transfer {
    params: HashMap<String, String>,
    data: Vec<u8>,
}

struct Cached {
    image: Arc<Image>,
    number: u32,
    used: u64,
}

/// Per-terminal graphics state: kitty's image cache and transfers in
/// progress, and the SIXEL palette.
#[derive(Default)]
pub struct Graphics {
    images: HashMap<u32, Cached>,
    bytes: usize,
    serial: u64,
    next_id: u32,
    kitty: Option<Transfer>,
    iterm: Option<Transfer>,
    palette: Option<Box<[[u8; 4]; 256]>>,
}

type Res<T> = Result<T, String>;

fn dimensions(w: usize, h: usize) -> Res<()> {
    if w == 0 || h == 0 || w > MAX_DIMENSION || h > MAX_DIMENSION || w > MAX_PIXELS / h {
        return Err("E2BIG: image dimensions exceed limit".into());
    }
    Ok(())
}

fn params(data: &str, separator: char) -> Res<HashMap<String, String>> {
    if data.len() > 4096 {
        return Err("E2BIG: graphics header exceeds limit".into());
    }
    let mut out = HashMap::new();
    for part in data.split(separator).filter(|p| !p.is_empty()) {
        let (k, v) = part.split_once('=').ok_or("EINVAL: invalid graphics parameter")?;
        out.insert(k.to_string(), v.to_string());
    }
    Ok(out)
}

fn number(p: &HashMap<String, String>, key: &str, default: i64) -> Res<i64> {
    match p.get(key) {
        None => Ok(default),
        Some(v) => v.parse::<i64>().ok().filter(|n| *n >= 0).ok_or_else(|| format!("EINVAL: invalid {key}")),
    }
}

fn decode_file(codec: Option<&dyn Codec>, data: &[u8]) -> Res<Arc<Image>> {
    let codec = codec.ok_or("ENOTSUP: no image decoder")?;
    let (w, h, rgba) = codec.decode(data).ok_or("EINVAL: unreadable image")?;
    dimensions(w as usize, h as usize)?;
    if rgba.len() != w as usize * h as usize * 4 {
        return Err("EINVAL: image data does not match its size".into());
    }
    Ok(Image::new(w, h, rgba))
}

/// Sizes a placement in cells. `cols`/`rows` of 0 follow the image.
fn size_placement(mut p: Placement, cols: usize, rows: usize, cell: CellSize) -> Res<Placement> {
    let (cw, ch) = (cell.width.max(1), cell.height.max(1));
    let (w, h) = (p.src[2].max(1), p.src[3].max(1));
    let mut pw = if cols > 0 { (cols as u32 * cw).saturating_sub(p.offset[0]) } else { w };
    let mut ph = if rows > 0 { (rows as u32 * ch).saturating_sub(p.offset[1]) } else { h };
    if cols > 0 && rows == 0 {
        ph = (h * pw / w).max(1);
    }
    if rows > 0 && cols == 0 {
        pw = (w * ph / h).max(1);
    }
    p.cols = (pw + p.offset[0]).div_ceil(cw) as usize;
    p.rows = (ph + p.offset[1]).div_ceil(ch) as usize;
    if pw == 0 || ph == 0 || p.rows > MAX_DIMENSION || p.cols > MAX_DIMENSION || p.cols * p.rows > MAX_CELLS {
        return Err("E2BIG: placement exceeds cell limit".into());
    }
    p.size = [pw, ph];
    Ok(p)
}

fn whole(image: Arc<Image>) -> Placement {
    let src = [0, 0, image.width, image.height];
    Placement { image, src, col: 0, cols: 0, rows: 0, size: [0, 0], offset: [0, 0], kitty: None }
}

impl Graphics {
    /// Handles a DCS q (SIXEL) string. `transparent` is P2 = 1.
    pub fn sixel(&mut self, data: &[u8], transparent: bool, cell: CellSize) -> Outcome {
        match self.decode_sixel(data, transparent).and_then(|img| size_placement(whole(img), 0, 0, cell)) {
            Ok(p) => Outcome { place: Some(p), inline: true, moves: true, ..Outcome::default() },
            Err(_) => Outcome::default(),
        }
    }

    fn decode_sixel(&mut self, data: &[u8], transparent: bool) -> Res<Arc<Image>> {
        let palette = self.palette.get_or_insert_with(|| Box::new(default_palette()));
        // Measure first, then paint into an image of that size.
        let mut s = Sixel::new(palette.clone());
        s.parse(data, None)?;
        dimensions(s.width, s.height)?;
        let mut rgba = vec![0u8; s.width * s.height * 4];
        if !transparent {
            let bg = s.palette[0];
            rgba.as_chunks_mut::<4>().0.fill([bg[0], bg[1], bg[2], 255]);
        }
        let (w, h) = (s.width, s.height);
        let mut s = Sixel::new(palette.clone());
        s.parse(data, Some((&mut rgba, w, h)))?;
        **palette = *s.palette;
        Ok(Image::new(w as u32, h as u32, rgba))
    }

    /// Handles an APC G (kitty) string, without the G.
    pub fn kitty(&mut self, body: &[u8], cell: CellSize, codec: Option<&dyn Codec>) -> Outcome {
        let text = String::from_utf8_lossy(body);
        let (header, payload) = text.split_once(';').unwrap_or((&text, ""));
        let mut p = match params(header, ',') {
            Ok(p) => p,
            Err(e) => {
                self.kitty = None;
                return Outcome { reply: Some(format!("\x1b_Gi=0;{e}\x1b\\")), ..Outcome::default() };
            }
        };
        if p.get("a").map(String::as_str) == Some("d") {
            self.kitty = None;
        }
        // A chunk of a transfer in progress carries only m (and q).
        if let Some(t) = self.kitty.as_mut() {
            if p.keys().all(|k| k == "m" || k == "q") {
                let more = p.get("m").cloned().unwrap_or_else(|| "0".into());
                t.params.insert("m".into(), more);
                if let Some(q) = p.get("q") {
                    t.params.insert("q".into(), q.clone());
                }
                p = t.params.clone();
            } else {
                self.kitty = None;
            }
        }
        let id = number(&p, "i", 0).unwrap_or(-1);
        let num = number(&p, "I", 0).unwrap_or(-1);
        let pid = number(&p, "p", 0).unwrap_or(-1);
        let quiet = number(&p, "q", 0).unwrap_or(-1);
        let wants_reply = id != 0 || num != 0 || p.get("a").map(String::as_str) == Some("q");
        let mut out = Outcome::default();
        let result = if id < 0 || num < 0 || pid < 0 || !(0..=2).contains(&quiet) || (id != 0 && num != 0) {
            Err("EINVAL: invalid image identifier or quiet flag".to_string())
        } else {
            self.kitty_command(&p, payload.as_bytes(), id as u32, num as u32, pid as u32, cell, codec, &mut out)
        };
        let chunk_pending = matches!(result, Ok(true));
        if result.is_err() {
            self.kitty = None;
        }
        let failed = result.as_ref().err().cloned();
        if !chunk_pending && wants_reply && quiet < 2 && (failed.is_some() || quiet == 0) {
            let mut fields = format!("i={}", id.max(0));
            if num > 0 {
                fields += &format!(",I={num}");
            }
            if pid > 0 {
                fields += &format!(",p={pid}");
            }
            let status = failed.unwrap_or_else(|| "OK".into());
            out.reply = Some(format!("\x1b_G{fields};{status}\x1b\\"));
        }
        out
    }

    /// Runs one kitty command. Ok(true) means a chunked transfer continues.
    #[allow(clippy::too_many_arguments)]
    fn kitty_command(
        &mut self,
        p: &HashMap<String, String>,
        payload: &[u8],
        mut id: u32,
        num: u32,
        pid: u32,
        cell: CellSize,
        codec: Option<&dyn Codec>,
        out: &mut Outcome,
    ) -> Res<bool> {
        for key in ["U", "P", "Q", "z"] {
            if p.get(key).is_some_and(|v| v != "0") {
                return Err("ENOTSUP: layered and virtual placements are not supported".into());
            }
        }
        let action = p.get("a").map_or("t", String::as_str);
        if action == "d" {
            out.delete = Some(self.kitty_delete(p, id, num)?);
            return Ok(false);
        }
        if !matches!(action, "t" | "T" | "p" | "q") {
            return Err("ENOTSUP: graphics action is not supported".into());
        }
        if p.get("t").is_some_and(|m| m != "d") {
            return Err("ENOTSUP: only in-band image data is supported".into());
        }
        let image = if action == "p" {
            if num != 0 {
                id = self.by_number(num);
            }
            self.images.get(&id).map(|c| c.image.clone()).ok_or("ENOENT: unknown image")?
        } else {
            let more = number(p, "m", 0)?;
            if more > 1 {
                return Err("EINVAL: invalid chunk flag".into());
            }
            let t = self.kitty.get_or_insert_with(|| Transfer { params: p.clone(), data: Vec::new() });
            if t.data.len() + payload.len() > MAX_SEQUENCE {
                return Err("E2BIG: image transfer exceeds limit".into());
            }
            t.data.extend_from_slice(payload);
            if more == 1 {
                return Ok(true);
            }
            let t = self.kitty.take().unwrap();
            let data = base64_decode(&t.data).ok_or("EINVAL: invalid base64")?;
            let image = decode_kitty(&data, p, codec)?;
            if action == "q" {
                return Ok(false);
            }
            image
        };
        if action == "T" || action == "p" {
            let mut placement = kitty_placement(image.clone(), p, cell)?;
            out.moves = number(p, "C", 0)? == 0;
            if action != "p" && id == 0 {
                id = self.allocate_id();
            }
            placement.kitty = Some((id, pid));
            if pid != 0 {
                out.delete = Some(Delete::Image(id, pid));
            }
            out.place = Some(placement);
        }
        if action != "p" {
            if id == 0 {
                id = self.allocate_id();
            }
            self.cache(id, num, image);
        }
        Ok(false)
    }

    fn kitty_delete(&mut self, p: &HashMap<String, String>, id: u32, num: u32) -> Res<Delete> {
        let kind = p.get("d").map_or("a", String::as_str);
        let mut chars = kind.chars();
        let (Some(c), None) = (chars.next(), chars.next()) else {
            return Err("EINVAL: invalid deletion selector".into());
        };
        let free = c.is_ascii_uppercase();
        let x = number(p, "x", 0)? as usize;
        let y = number(p, "y", 0)? as usize;
        let pid = number(p, "p", 0)? as u32;
        let id = if c.eq_ignore_ascii_case(&'n') { self.by_number(num) } else { id };
        let delete = match c.to_ascii_lowercase() {
            'a' => Delete::All,
            'i' | 'n' => Delete::Image(id, pid),
            'c' | 'p' => Delete::Cell(x, y),
            'x' => Delete::Column(x),
            'y' => Delete::Row(y),
            'r' => Delete::Range(x as u32, y as u32),
            _ => return Err("ENOTSUP: deletion selector is not supported".into()),
        };
        if free {
            match delete {
                Delete::All => {
                    self.images.clear();
                    self.bytes = 0;
                }
                Delete::Image(id, _) => self.drop_image(id),
                Delete::Range(lo, hi) => {
                    let ids: Vec<u32> = self.images.keys().copied().filter(|k| (lo..=hi).contains(k)).collect();
                    for k in ids {
                        self.drop_image(k);
                    }
                }
                _ => return Err("ENOTSUP: freeing images by position is not supported".into()),
            }
        }
        Ok(delete)
    }

    fn allocate_id(&mut self) -> u32 {
        loop {
            self.next_id = self.next_id.wrapping_add(1);
            if self.next_id != 0 && !self.images.contains_key(&self.next_id) {
                return self.next_id;
            }
        }
    }

    fn by_number(&self, number: u32) -> u32 {
        self.images.iter().filter(|(_, c)| c.number == number).max_by_key(|(_, c)| c.used).map_or(0, |(k, _)| *k)
    }

    fn cache(&mut self, id: u32, number: u32, image: Arc<Image>) {
        self.drop_image(id);
        while !self.images.is_empty()
            && (self.images.len() >= MAX_IMAGES || self.bytes + image.rgba.len() > MAX_CACHE_BYTES)
        {
            let oldest = self.images.iter().min_by_key(|(_, c)| c.used).map(|(k, _)| *k).unwrap();
            self.drop_image(oldest);
        }
        self.serial += 1;
        self.bytes += image.rgba.len();
        self.images.insert(id, Cached { image, number, used: self.serial });
    }

    fn drop_image(&mut self, id: u32) {
        if let Some(c) = self.images.remove(&id) {
            self.bytes -= c.image.rgba.len();
        }
    }

    /// Handles an OSC 1337 File, MultipartFile, FilePart, or FileEnd.
    pub fn iterm(&mut self, body: &[u8], cell: CellSize, codec: Option<&dyn Codec>) -> Outcome {
        match self.iterm_command(body, cell, codec) {
            Ok(Some(p)) => Outcome { place: Some(p), inline: true, moves: true, ..Outcome::default() },
            _ => Outcome::default(),
        }
    }

    fn iterm_command(&mut self, body: &[u8], cell: CellSize, codec: Option<&dyn Codec>) -> Res<Option<Placement>> {
        let (p, payload) = if let Some(rest) = body.strip_prefix(b"MultipartFile=") {
            let p = params(&String::from_utf8_lossy(rest), ';')?;
            self.iterm = Some(Transfer { params: p, data: Vec::new() });
            return Ok(None);
        } else if let Some(part) = body.strip_prefix(b"FilePart=") {
            let t = self.iterm.as_mut().ok_or("EINVAL: no multipart image")?;
            if t.data.len() + part.len() > MAX_SEQUENCE {
                self.iterm = None;
                return Err("E2BIG: multipart image exceeds limit".into());
            }
            t.data.extend_from_slice(part);
            return Ok(None);
        } else if body == b"FileEnd" {
            let t = self.iterm.take().ok_or("EINVAL: no multipart image")?;
            (t.params, t.data)
        } else if let Some(rest) = body.strip_prefix(b"File=") {
            self.iterm = None;
            let colon = rest.iter().position(|&b| b == b':').ok_or("EINVAL: missing image data")?;
            (params(&String::from_utf8_lossy(&rest[..colon]), ';')?, rest[colon + 1..].to_vec())
        } else {
            return Err("EINVAL: invalid image command".into());
        };
        // Downloads are never written to disk.
        if p.get("inline").map(String::as_str) != Some("1") {
            return Err("ENOTSUP: file downloads are not supported".into());
        }
        let data = base64_decode(&payload).ok_or("EINVAL: invalid base64")?;
        if number(&p, "size", data.len() as i64)? as usize != data.len() {
            return Err("EINVAL: file size does not match image data".into());
        }
        let image = decode_file(codec, &data)?;
        let (iw, ih) = (image.width, image.height);
        let mut w = iterm_dimension(p.get("width"), cell.cols, cell.width)?;
        let mut h = iterm_dimension(p.get("height"), cell.rows, cell.height)?;
        if w == 0 && h == 0 {
            (w, h) = (iw, ih);
        }
        if w == 0 {
            w = (h * iw / ih).max(1);
        }
        if h == 0 {
            h = (w * ih / iw).max(1);
        }
        if p.get("preserveAspectRatio").is_none_or(|v| v != "0") {
            if u64::from(w) * u64::from(ih) > u64::from(h) * u64::from(iw) {
                w = (h * iw / ih).max(1);
            } else {
                h = (w * ih / iw).max(1);
            }
        }
        let mut pl = whole(image);
        pl.size = [w, h];
        pl.cols = w.div_ceil(cell.width.max(1)) as usize;
        pl.rows = h.div_ceil(cell.height.max(1)) as usize;
        if pl.cols * pl.rows > MAX_CELLS || pl.rows > MAX_DIMENSION {
            return Err("E2BIG: placement exceeds cell limit".into());
        }
        Ok(Some(pl))
    }
}

fn iterm_dimension(value: Option<&String>, cells: usize, pixels: u32) -> Res<u32> {
    let Some(v) = value.map(String::as_str).filter(|v| !v.is_empty() && *v != "auto") else { return Ok(0) };
    let (n, mul, div) = if let Some(px) = v.strip_suffix("px") {
        (px, 1, 1)
    } else if let Some(pc) = v.strip_suffix('%') {
        (pc, cells as u32 * pixels, 100)
    } else {
        (v, pixels, 1)
    };
    let n: u32 =
        n.parse().ok().filter(|n| (1..=MAX_DIMENSION as u32).contains(n)).ok_or("EINVAL: invalid image size")?;
    Ok((n * mul / div).max(1))
}

fn decode_kitty(data: &[u8], p: &HashMap<String, String>, codec: Option<&dyn Codec>) -> Res<Arc<Image>> {
    let f = number(p, "f", 32)?;
    if !matches!(f, 24 | 32 | 100) {
        return Err("EINVAL: unsupported pixel format".into());
    }
    let inflated;
    let data = match p.get("o").map(String::as_str) {
        None | Some("") => data,
        Some("z") => {
            inflated = codec
                .ok_or("ENOTSUP: no zlib decoder")?
                .inflate(data, MAX_PIXELS * 4)
                .ok_or("EINVAL: invalid compressed image")?;
            &inflated
        }
        Some(_) => return Err("ENOTSUP: unsupported compression".into()),
    };
    if f == 100 {
        return decode_file(codec, data);
    }
    let (w, h) = (number(p, "s", 0)? as usize, number(p, "v", 0)? as usize);
    dimensions(w, h)?;
    let stride = (f / 8) as usize;
    if data.len() != w * h * stride {
        return Err("EINVAL: pixel data length does not match dimensions".into());
    }
    let rgba = if stride == 4 {
        data.to_vec()
    } else {
        data.as_chunks::<3>().0.iter().flat_map(|&[r, g, b]| [r, g, b, 255]).collect()
    };
    Ok(Image::new(w as u32, h as u32, rgba))
}

fn kitty_placement(image: Arc<Image>, p: &HashMap<String, String>, cell: CellSize) -> Res<Placement> {
    let n = |k| number(p, k, 0).map(|v| v as u32);
    let (x, y, mut w, mut h) = (n("x")?, n("y")?, n("w")?, n("h")?);
    let (ox, oy) = (n("X")?, n("Y")?);
    if ox >= cell.width || oy >= cell.height || n("C")? > 1 {
        return Err("EINVAL: invalid placement offset".into());
    }
    if w == 0 {
        w = image.width.saturating_sub(x);
    }
    if h == 0 {
        h = image.height.saturating_sub(y);
    }
    let w = w.min(image.width.saturating_sub(x));
    let h = h.min(image.height.saturating_sub(y));
    if w == 0 || h == 0 {
        return Err("EINVAL: crop is outside the image".into());
    }
    let placement = Placement { src: [x, y, w, h], offset: [ox, oy], ..whole(image) };
    size_placement(placement, n("c")? as usize, n("r")? as usize, cell)
}

struct Sixel {
    x: usize,
    y: usize,
    width: usize,
    height: usize,
    selected: usize,
    work: usize,
    palette: Box<[[u8; 4]; 256]>,
}

fn sixel_numbers(data: &[u8], mut i: usize) -> Res<(Vec<usize>, usize)> {
    let mut nums = Vec::new();
    while i < data.len() && (data[i].is_ascii_digit() || data[i] == b';') {
        let mut n = 0usize;
        while i < data.len() && data[i].is_ascii_digit() {
            n = n * 10 + usize::from(data[i] - b'0');
            i += 1;
            if n > MAX_DIMENSION {
                return Err("E2BIG: SIXEL parameter exceeds limit".into());
            }
        }
        nums.push(n);
        if nums.len() > 5 {
            return Err("EINVAL: too many SIXEL parameters".into());
        }
        if i == data.len() || data[i] != b';' {
            break;
        }
        i += 1;
        if i == data.len() {
            nums.push(0);
        }
    }
    Ok((nums, i))
}

fn hls(hue: usize, lightness: usize, saturation: usize) -> [u8; 4] {
    let h = ((hue + 240) % 360) as f64 / 60.0;
    let (l, s) = (lightness as f64 / 100.0, saturation as f64 / 100.0);
    let c = (1.0 - (2.0 * l - 1.0).abs()) * s;
    let x = c * (1.0 - (h % 2.0 - 1.0).abs());
    let (r, g, b) = match h {
        h if h < 1.0 => (c, x, 0.0),
        h if h < 2.0 => (x, c, 0.0),
        h if h < 3.0 => (0.0, c, x),
        h if h < 4.0 => (0.0, x, c),
        h if h < 5.0 => (x, 0.0, c),
        _ => (c, 0.0, x),
    };
    let m = l - c / 2.0;
    let v = |v: f64| ((v + m) * 255.0).round().clamp(0.0, 255.0) as u8;
    [v(r), v(g), v(b), 255]
}

fn default_palette() -> [[u8; 4]; 256] {
    const BASE: [[u8; 3]; 16] = [
        [0, 0, 0],
        [51, 51, 204],
        [204, 33, 33],
        [51, 204, 51],
        [204, 51, 204],
        [51, 204, 204],
        [204, 204, 51],
        [135, 135, 135],
        [66, 66, 66],
        [84, 84, 255],
        [255, 84, 84],
        [84, 255, 84],
        [255, 84, 255],
        [84, 255, 255],
        [255, 255, 84],
        [255, 255, 255],
    ];
    let mut p = [[0u8; 4]; 256];
    for (i, c) in p.iter_mut().enumerate() {
        *c = [i as u8, i as u8, i as u8, 255];
    }
    for (i, c) in BASE.iter().enumerate() {
        p[i] = [c[0], c[1], c[2], 255];
    }
    p
}

impl Sixel {
    fn new(palette: Box<[[u8; 4]; 256]>) -> Self {
        Self { x: 0, y: 0, width: 0, height: 0, selected: 0, work: 0, palette }
    }

    /// Parses SIXEL data, measuring it, and painting when given an image.
    fn parse(&mut self, data: &[u8], mut image: Option<(&mut [u8], usize, usize)>) -> Res<()> {
        let mut i = 0;
        while i < data.len() {
            let c = data[i];
            i += 1;
            match c {
                b'?'..=b'~' => self.paint(c, 1, &mut image)?,
                b'!' => {
                    let start = i;
                    while i < data.len() && data[i].is_ascii_digit() {
                        i += 1;
                    }
                    let (nums, _) = sixel_numbers(&data[start..i], 0)?;
                    if nums.len() != 1 || nums[0] == 0 || i == data.len() || !(b'?'..=b'~').contains(&data[i]) {
                        return Err("EINVAL: invalid SIXEL repeat".into());
                    }
                    self.paint(data[i], nums[0], &mut image)?;
                    i += 1;
                }
                b'$' => self.x = 0,
                b'-' => {
                    self.x = 0;
                    self.y += 6;
                }
                b'"' | b'#' => {
                    let (nums, end) = sixel_numbers(data, i)?;
                    i = end;
                    if c == b'"' {
                        if !(2..=4).contains(&nums.len()) {
                            return Err("EINVAL: invalid SIXEL raster attributes".into());
                        }
                        if nums.len() == 4 {
                            self.width = self.width.max(nums[2]);
                            self.height = self.height.max(nums[3]);
                        }
                    } else {
                        if nums.is_empty() || nums[0] >= 256 {
                            return Err("EINVAL: invalid SIXEL palette index".into());
                        }
                        self.selected = nums[0];
                        if nums.len() != 1 {
                            if nums.len() != 5 {
                                return Err("EINVAL: invalid SIXEL colour".into());
                            }
                            self.palette[self.selected] = match nums[1] {
                                2 if nums[2..].iter().all(|v| *v <= 100) => {
                                    let s = |v: usize| ((v * 255 + 50) / 100) as u8;
                                    [s(nums[2]), s(nums[3]), s(nums[4]), 255]
                                }
                                1 if nums[2] <= 360 && nums[3] <= 100 && nums[4] <= 100 => {
                                    hls(nums[2], nums[3], nums[4])
                                }
                                _ => return Err("EINVAL: invalid SIXEL colour".into()),
                            };
                        }
                    }
                }
                b'\r' | b'\n' | b'\t' => {}
                _ => return Err("EINVAL: invalid SIXEL byte".into()),
            }
            if self.y > MAX_DIMENSION || self.width > MAX_DIMENSION || self.height > MAX_DIMENSION {
                return Err("E2BIG: SIXEL dimensions exceed limit".into());
            }
        }
        Ok(())
    }

    fn paint(&mut self, c: u8, count: usize, image: &mut Option<(&mut [u8], usize, usize)>) -> Res<()> {
        if count > MAX_DIMENSION || self.x + count > MAX_DIMENSION || self.y >= MAX_DIMENSION {
            return Err("E2BIG: SIXEL run exceeds limit".into());
        }
        self.work += count * 6;
        if self.work > MAX_PIXELS * 16 {
            return Err("E2BIG: SIXEL drawing work exceeds limit".into());
        }
        self.width = self.width.max(self.x + count);
        self.height = self.height.max(self.y + 6);
        if let Some((rgba, w, h)) = image.as_mut() {
            let col = self.palette[self.selected];
            for bit in 0..6 {
                let y = self.y + bit;
                if (c - b'?') & (1 << bit) != 0 && y < *h {
                    for x in self.x..(self.x + count).min(*w) {
                        let at = (y * *w + x) * 4;
                        rgba[at..at + 4].copy_from_slice(&col);
                    }
                }
            }
        }
        self.x += count;
        Ok(())
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    const CELL: CellSize = CellSize { width: 10, height: 20, cols: 80, rows: 24 };

    #[test]
    fn sixel_decodes() {
        let mut g = Graphics::default();
        // A 4x6 red block: colour 1 set to RGB 100,0,0, four columns of all six bits.
        let out = g.sixel(b"#1;2;100;0;0#1!4~", false, CELL);
        let p = out.place.unwrap();
        assert_eq!((p.image.width, p.image.height), (4, 6));
        assert_eq!(&p.image.rgba[..4], &[255, 0, 0, 255]);
        assert_eq!((p.cols, p.rows), (1, 1));
        assert!(out.inline);
    }

    #[test]
    fn sixel_rejects_garbage() {
        assert!(Graphics::default().sixel(b"#1;2;100;0;0!0~", false, CELL).place.is_none());
        assert!(Graphics::default().sixel(b"\x07", false, CELL).place.is_none());
    }

    #[test]
    fn kitty_raw_rgb_and_reply() {
        let mut g = Graphics::default();
        // 2x1 RGB: base64 of [255,0,0, 0,255,0].
        let out = g.kitty(b"a=T,f=24,s=2,v=1,i=7;/wAAAP8A", CELL, None);
        let p = out.place.unwrap();
        assert_eq!(&p.image.rgba, &[255, 0, 0, 255, 0, 255, 0, 255]);
        assert_eq!(p.kitty, Some((7, 0)));
        assert_eq!(out.reply.as_deref(), Some("\x1b_Gi=7;OK\x1b\\"));
        // Placing it again by id reuses the cached image.
        let again = g.kitty(b"a=p,i=7,c=4,r=2", CELL, None);
        let p = again.place.unwrap();
        assert_eq!((p.cols, p.rows), (4, 2));
    }

    #[test]
    fn kitty_chunks() {
        let mut g = Graphics::default();
        assert!(g.kitty(b"a=T,f=24,s=2,v=1,m=1;/wAA", CELL, None).place.is_none());
        let out = g.kitty(b"m=0;AP8A", CELL, None);
        assert!(out.place.is_some());
    }

    #[test]
    fn kitty_errors_reply() {
        let mut g = Graphics::default();
        let out = g.kitty(b"a=p,i=99", CELL, None);
        assert_eq!(out.reply.as_deref(), Some("\x1b_Gi=99;ENOENT: unknown image\x1b\\"));
        let out = g.kitty(b"a=T,t=f,i=3;L2V0Yy9wYXNzd2Q=", CELL, None);
        assert!(out.reply.unwrap().contains("ENOTSUP"), "file transfers are refused");
    }

    #[test]
    fn kitty_delete() {
        let mut g = Graphics::default();
        g.kitty(b"a=t,f=24,s=2,v=1,i=5;/wAAAP8A", CELL, None);
        assert_eq!(g.kitty(b"a=d,d=I,i=5", CELL, None).delete, Some(Delete::Image(5, 0)));
        assert!(g.kitty(b"a=p,i=5", CELL, None).place.is_none(), "freed images are gone");
    }

    #[test]
    fn iterm_needs_inline() {
        let mut g = Graphics::default();
        assert!(g.iterm(b"File=name=eA==:aGk=", CELL, None).place.is_none());
    }

    #[test]
    fn iterm_dimensions() {
        let s = |v: &str| Some(v.to_string());
        assert_eq!(iterm_dimension(s("10px").as_ref(), 80, 10), Ok(10));
        assert_eq!(iterm_dimension(s("3").as_ref(), 80, 10), Ok(30));
        assert_eq!(iterm_dimension(s("50%").as_ref(), 80, 10), Ok(400));
        assert_eq!(iterm_dimension(s("auto").as_ref(), 80, 10), Ok(0));
        assert!(iterm_dimension(s("-1").as_ref(), 80, 10).is_err());
    }
}
