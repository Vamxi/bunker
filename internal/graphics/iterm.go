package graphics

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

func (d *Decoder) handleITerm(body []byte, ctx Context) (Result, error) {
	var p map[string]string
	var payload []byte
	switch {
	case bytes.HasPrefix(body, []byte("MultipartFile=")):
		d.iterm = nil
		parsed, err := params(string(body[len("MultipartFile="):]), ";")
		if err != nil {
			return Result{}, err
		}
		if parsed["inline"] != "1" {
			return Result{}, fmt.Errorf("ENOTSUP: file downloads are not supported")
		}
		d.iterm = &transfer{params: parsed}
		return Result{}, nil
	case bytes.HasPrefix(body, []byte("FilePart=")):
		if d.iterm == nil {
			return Result{}, fmt.Errorf("EINVAL: no multipart image")
		}
		part := body[len("FilePart="):]
		if len(d.iterm.data)+len(part) > MaxSequenceBytes {
			d.iterm = nil
			return Result{}, fmt.Errorf("E2BIG: multipart image exceeds limit")
		}
		d.iterm.data = append(d.iterm.data, part...)
		return Result{}, nil
	case bytes.Equal(body, []byte("FileEnd")):
		if d.iterm == nil {
			return Result{}, fmt.Errorf("EINVAL: no multipart image")
		}
		p, payload = d.iterm.params, d.iterm.data
		d.iterm = nil
	case bytes.HasPrefix(body, []byte("File=")):
		d.iterm = nil
		header, encoded, ok := bytes.Cut(body[len("File="):], []byte(":"))
		if !ok {
			return Result{}, fmt.Errorf("EINVAL: missing image data")
		}
		parsed, err := params(string(header), ";")
		if err != nil {
			return Result{}, err
		}
		p, payload = parsed, encoded
	default:
		return Result{}, fmt.Errorf("EINVAL: invalid image command")
	}
	if p["inline"] != "1" {
		return Result{}, fmt.Errorf("ENOTSUP: file downloads are not supported")
	}
	data, err := decodeBase64(payload)
	if err != nil {
		return Result{}, err
	}
	if size, err := number(p, "size", len(data)); err != nil || size != len(data) {
		return Result{}, fmt.Errorf("EINVAL: file size does not match image data")
	}
	img, err := decodeFile(data, false)
	if err != nil {
		return Result{}, err
	}
	w, err := itermDimension(p["width"], ctx.Cols, ctx.CellWidth)
	if err != nil {
		return Result{}, err
	}
	h, err := itermDimension(p["height"], ctx.Rows, ctx.CellHeight)
	if err != nil {
		return Result{}, err
	}
	preserve := p["preserveAspectRatio"]
	if preserve != "" && preserve != "0" && preserve != "1" {
		return Result{}, fmt.Errorf("EINVAL: invalid aspect ratio flag")
	}
	if w == 0 && h == 0 {
		w, h = img.Bounds().Dx(), img.Bounds().Dy()
	}
	if w == 0 {
		w = max(1, h*img.Bounds().Dx()/img.Bounds().Dy())
	}
	if h == 0 {
		h = max(1, w*img.Bounds().Dy()/img.Bounds().Dx())
	}
	if preserve != "0" {
		if w*img.Bounds().Dy() > h*img.Bounds().Dx() {
			w = max(1, h*img.Bounds().Dx()/img.Bounds().Dy())
		} else {
			h = max(1, w*img.Bounds().Dy()/img.Bounds().Dx())
		}
	}
	paint, err := placement(img, 0, 0, ctx, 0, 0)
	if err != nil {
		return Result{}, err
	}
	paint.PixelWidth, paint.PixelHeight = w, h
	paint.Cols, paint.Rows = (w+ctx.CellWidth-1)/ctx.CellWidth, (h+ctx.CellHeight-1)/ctx.CellHeight
	if paint.Rows <= 0 || paint.Cols <= 0 || paint.Rows > MaxDimension || paint.Cols > MaxDimension || paint.Cols > maxDisplayCells/paint.Rows {
		return Result{}, fmt.Errorf("E2BIG: placement exceeds cell limit")
	}
	paint.Inline = true
	return Result{Paint: paint}, nil
}

func itermDimension(value string, cells, pixels int) (int, error) {
	if value == "" || value == "auto" {
		return 0, nil
	}
	multiplier, divisor := pixels, 1
	if strings.HasSuffix(value, "px") {
		value = strings.TrimSuffix(value, "px")
		multiplier = 1
	} else if strings.HasSuffix(value, "%") {
		value = strings.TrimSuffix(value, "%")
		multiplier, divisor = cells*pixels, 100
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 || n > MaxDimension || multiplier < 1 || multiplier > 1<<20 {
		return 0, fmt.Errorf("EINVAL: invalid image size")
	}
	return max(1, n*multiplier/divisor), nil
}
