// Package graphics decodes bounded, in-band terminal images without filesystem
// access. Its output is rasterized into ordinary terminal cells by vt10x.
package graphics

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"image"
	"image/color"
	"image/draw"
	_ "image/gif"
	_ "image/jpeg"
	_ "image/png"
	"strconv"
	"strings"
)

const (
	// MaxSequenceBytes bounds both a graphics control and a multipart transfer.
	MaxSequenceBytes = 8 << 20
	// MaxPixels bounds decoded image allocations, including compressed images.
	MaxPixels = 4 << 20
	// MaxDimension bounds source image dimensions and SIXEL repeat counts.
	MaxDimension = 4096
	// MaxCacheBytes bounds reusable Kitty image data per terminal.
	MaxCacheBytes   = 32 << 20
	maxImages       = 32
	maxDisplayCells = 256 << 10
)

// Context describes the pane and the virtual pixel size of a character cell.
type Context struct{ Cols, Rows, CellWidth, CellHeight int }

// Placement is an immutable source image and its cell-local display geometry.
type Placement struct {
	Image                   *image.NRGBA
	Source                  image.Rectangle
	ID, PlacementID         uint32
	PixelWidth, PixelHeight int
	OffsetX, OffsetY        int
	Cols, Rows              int
	Move                    bool
	Inline                  bool // inline protocols leave the cursor below the image, at its left
}

// Deletion selects complete placements, not individual image pixels.
type Deletion struct {
	Kind            byte
	ID, PlacementID uint32
	X, Y            int
	Free            bool
}

// Result contains the effects of a complete graphics command.
type Result struct {
	Paint  *Placement
	Delete *Deletion
	Reply  string
}

type cachedImage struct {
	image  *image.NRGBA
	number uint32
	used   uint64
}
type transfer struct {
	params map[string]string
	data   []byte
}

// Decoder owns per-terminal transfers and reusable images. Images are immutable;
// Clone shares pixels but copies mutable state for resize replay.
type Decoder struct {
	images       map[uint32]cachedImage
	kitty, iterm *transfer
	palette      [256]color.NRGBA
	paletteSet   bool
	bytes        int
	serial       uint64
	nextID       uint32
}

// Abort discards incomplete uploads after a cancelled or oversized control.
func (d *Decoder) Abort() { d.kitty, d.iterm = nil, nil }

// Clone copies reusable image data, but not incomplete uploads.
func (d *Decoder) Clone() *Decoder {
	if d == nil {
		return &Decoder{}
	}
	c := *d
	c.kitty, c.iterm = nil, nil
	c.images = make(map[uint32]cachedImage, len(d.images))
	for id, img := range d.images {
		c.images[id] = img
	}
	return &c
}

// IsCommand recognizes a graphics string from its introducer and a short prefix.
func IsCommand(typ rune, body []byte) bool {
	switch typ {
	case '_':
		return len(body) > 0 && body[0] == 'G'
	case ']':
		return bytes.HasPrefix(body, []byte("1337;File=")) || bytes.HasPrefix(body, []byte("1337;MultipartFile=")) || bytes.HasPrefix(body, []byte("1337;FilePart=")) || bytes.Equal(body, []byte("1337;FileEnd"))
	case 'P':
		for i, c := range body {
			if i > 64 {
				return false
			}
			if c == 'q' {
				return true
			}
			if (c < '0' || c > '9') && c != ';' {
				return false
			}
		}
	}
	return false
}

// Handle consumes a recognized string, returning a protocol reply even on error.
func (d *Decoder) Handle(typ rune, body []byte, ctx Context) (Result, error) {
	if !IsCommand(typ, body) {
		return Result{}, fmt.Errorf("EINVAL: not a graphics command")
	}
	if len(body) > MaxSequenceBytes {
		d.Abort()
		return Result{}, fmt.Errorf("E2BIG: graphics transfer exceeds limit")
	}
	switch typ {
	case '_':
		return d.handleKitty(body[1:], ctx)
	case ']':
		return d.handleITerm(body[len("1337;"):], ctx)
	case 'P':
		img, err := d.sixel(body)
		if err != nil {
			return Result{}, err
		}
		p, err := placement(img, 0, 0, ctx, 0, 0)
		if err != nil {
			return Result{}, err
		}
		p.Inline = true
		return Result{Paint: p}, nil
	}
	return Result{}, fmt.Errorf("ENOTSUP: unknown graphics protocol")
}

func decodeFile(data []byte, pngOnly bool) (*image.NRGBA, error) {
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("EINVAL: image header: %w", err)
	}
	if pngOnly && format != "png" {
		return nil, fmt.Errorf("EINVAL: expected PNG")
	}
	if err := dimensions(cfg.Width, cfg.Height); err != nil {
		return nil, err
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("EINVAL: image data: %w", err)
	}
	if err := dimensions(img.Bounds().Dx(), img.Bounds().Dy()); err != nil {
		return nil, err
	}
	if n, ok := img.(*image.NRGBA); ok {
		return n, nil
	}
	n := image.NewNRGBA(image.Rect(0, 0, img.Bounds().Dx(), img.Bounds().Dy()))
	draw.Draw(n, n.Bounds(), img, img.Bounds().Min, draw.Src)
	return n, nil
}

func dimensions(w, h int) error {
	if w <= 0 || h <= 0 || w > MaxDimension || h > MaxDimension || w > MaxPixels/h {
		return fmt.Errorf("E2BIG: image dimensions exceed limit")
	}
	return nil
}

func decodeBase64(data []byte) ([]byte, error) {
	if len(data) > MaxSequenceBytes {
		return nil, fmt.Errorf("E2BIG: encoded image exceeds limit")
	}
	out := make([]byte, base64.StdEncoding.DecodedLen(len(data)))
	n, err := base64.StdEncoding.Decode(out, data)
	if err != nil {
		return nil, fmt.Errorf("EINVAL: invalid base64: %w", err)
	}
	return out[:n], nil
}

func params(data string, separator string) (map[string]string, error) {
	if len(data) > 4096 {
		return nil, fmt.Errorf("E2BIG: graphics header exceeds limit")
	}
	p := make(map[string]string)
	if data == "" {
		return p, nil
	}
	for _, field := range strings.Split(data, separator) {
		k, v, ok := strings.Cut(field, "=")
		if !ok || k == "" {
			return p, fmt.Errorf("EINVAL: malformed graphics header")
		}
		if _, exists := p[k]; exists {
			return p, fmt.Errorf("EINVAL: duplicate graphics parameter")
		}
		p[k] = v
	}
	return p, nil
}

func number(p map[string]string, key string, def int) (int, error) {
	v, ok := p[key]
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil || n < 0 || n > 1<<32-1 {
		return 0, fmt.Errorf("EINVAL: invalid numeric graphics parameter")
	}
	return int(n), nil
}

func placement(img *image.NRGBA, cols, rows int, ctx Context, id, pid uint32) (*Placement, error) {
	p := &Placement{Image: img, Source: img.Bounds(), ID: id, PlacementID: pid, Move: true}
	return sizePlacement(p, cols, rows, ctx)
}

func sizePlacement(p *Placement, cols, rows int, ctx Context) (*Placement, error) {
	w, h := p.Source.Dx(), p.Source.Dy()
	if ctx.CellWidth < 1 || ctx.CellWidth > 256 || ctx.CellHeight < 2 || ctx.CellHeight > 256 || w < 1 || h < 1 || cols < 0 || rows < 0 || cols > 4096 || rows > 4096 {
		return nil, fmt.Errorf("EINVAL: invalid placement dimensions")
	}
	pw, ph := w, h
	if cols > 0 {
		pw = cols*ctx.CellWidth - p.OffsetX
	}
	if rows > 0 {
		ph = rows*ctx.CellHeight - p.OffsetY
	}
	if cols > 0 && rows == 0 {
		ph = max(1, h*pw/w)
	}
	if rows > 0 && cols == 0 {
		pw = max(1, w*ph/h)
	}
	p.Cols = (pw + p.OffsetX + ctx.CellWidth - 1) / ctx.CellWidth
	p.Rows = (ph + p.OffsetY + ctx.CellHeight - 1) / ctx.CellHeight
	if pw < 1 || ph < 1 || p.Rows < 1 || p.Cols < 1 || p.Rows > MaxDimension || p.Cols > MaxDimension || p.Cols > maxDisplayCells/p.Rows {
		return nil, fmt.Errorf("E2BIG: placement exceeds cell limit")
	}
	p.PixelWidth, p.PixelHeight = pw, ph
	return p, nil
}

// Sample averages a pixel rectangle in the displayed placement. Alpha is
// averaged premultiplied, so transparent borders do not introduce dark fringes.
func (p *Placement) Sample(rect image.Rectangle) color.NRGBA {
	area := image.Rect(p.OffsetX, p.OffsetY, p.OffsetX+p.PixelWidth, p.OffsetY+p.PixelHeight)
	visible := rect.Intersect(area)
	if visible.Empty() {
		return color.NRGBA{}
	}
	w, h := p.Source.Dx(), p.Source.Dy()
	x0 := (visible.Min.X-area.Min.X)*w/p.PixelWidth + p.Source.Min.X
	x1 := (visible.Max.X-area.Min.X)*w/p.PixelWidth + p.Source.Min.X
	y0 := (visible.Min.Y-area.Min.Y)*h/p.PixelHeight + p.Source.Min.Y
	y1 := (visible.Max.Y-area.Min.Y)*h/p.PixelHeight + p.Source.Min.Y
	x1, y1 = min(p.Source.Max.X, max(x0+1, x1)), min(p.Source.Max.Y, max(y0+1, y1))
	var r, g, b, a, count uint64
	for y := y0; y < y1; y++ {
		for x := x0; x < x1; x++ {
			c := p.Image.NRGBAAt(x, y)
			r += uint64(c.R) * uint64(c.A)
			g += uint64(c.G) * uint64(c.A)
			b += uint64(c.B) * uint64(c.A)
			a += uint64(c.A)
			count++
		}
	}
	if a == 0 || count == 0 {
		return color.NRGBA{}
	}
	alpha := a / count * uint64(visible.Dx()*visible.Dy()) / uint64(rect.Dx()*rect.Dy())
	return color.NRGBA{R: uint8(r / a), G: uint8(g / a), B: uint8(b / a), A: uint8(alpha)}
}
