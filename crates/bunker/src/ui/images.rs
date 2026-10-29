//! Image decoding for the terminal core, using GTK's own decoders
//! (gdk-pixbuf for PNG, JPEG, and GIF; GIO's zlib), so no codec crates are
//! needed. Both run on the terminal's reader thread.

use std::io::Read;

use bunker_vt::Codec;
use gtk::prelude::*;
use gtk::{gdk_pixbuf, gio, glib};

/// The frontend's [`Codec`].
pub struct GtkCodec;

impl Codec for GtkCodec {
    fn decode(&self, data: &[u8]) -> Option<(u32, u32, Vec<u8>)> {
        let loader = gdk_pixbuf::PixbufLoader::new();
        loader.write(data).ok()?;
        loader.close().ok()?;
        let pixbuf = loader.pixbuf()?;
        let pixbuf = if pixbuf.has_alpha() { pixbuf } else { pixbuf.add_alpha(false, 0, 0, 0).ok()? };
        let (w, h) = (pixbuf.width() as usize, pixbuf.height() as usize);
        let stride = pixbuf.rowstride() as usize;
        let bytes = pixbuf.read_pixel_bytes();
        let mut rgba = Vec::with_capacity(w * h * 4);
        for y in 0..h {
            let row = bytes.get(y * stride..y * stride + w * 4)?;
            rgba.extend_from_slice(row);
        }
        Some((w as u32, h as u32, rgba))
    }

    fn inflate(&self, data: &[u8], limit: usize) -> Option<Vec<u8>> {
        let converter = gio::ZlibDecompressor::new(gio::ZlibCompressorFormat::Zlib);
        let input = gio::MemoryInputStream::from_bytes(&glib::Bytes::from(data));
        let stream = gio::ConverterInputStream::new(&input, &converter);
        let mut out = Vec::new();
        let mut reader = stream.into_read();
        let mut buf = [0u8; 64 << 10];
        loop {
            let n = reader.read(&mut buf).ok()?;
            if n == 0 {
                return Some(out);
            }
            if out.len() + n > limit {
                return None;
            }
            out.extend_from_slice(&buf[..n]);
        }
    }
}

#[cfg(test)]
mod tests {
    use super::*;

    #[test]
    fn decodes_png() {
        // A 1x1 red PNG.
        let png = bunker_vt::base64_decode(
            b"iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP4z8DwHwAFAAH/iZk9HQAAAABJRU5ErkJggg==",
        )
        .unwrap();
        let (w, h, rgba) = GtkCodec.decode(&png).unwrap();
        assert_eq!((w, h), (1, 1));
        assert_eq!(rgba, [255, 0, 0, 255]);
        assert!(GtkCodec.decode(b"not an image").is_none());
    }

    #[test]
    fn inflates_zlib() {
        // zlib of "hello".
        let z = [0x78, 0x9c, 0xcb, 0x48, 0xcd, 0xc9, 0xc9, 0x07, 0x00, 0x06, 0x2c, 0x02, 0x15];
        assert_eq!(GtkCodec.inflate(&z, 100).unwrap(), b"hello");
        assert!(GtkCodec.inflate(&z, 3).is_none(), "the limit holds");
    }
}
