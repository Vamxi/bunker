package graphics

import (
	"bytes"
	"compress/zlib"
	"fmt"
	"image"
	"io"
	"strings"
)

func (d *Decoder) handleKitty(body []byte, ctx Context) (result Result, err error) {
	header, payload, _ := bytes.Cut(body, []byte(";"))
	p, err := params(string(header), ",")
	if err != nil {
		d.kitty = nil
		return result, err
	}
	if p["a"] == "d" {
		d.kitty = nil
	}
	if d.kitty != nil {
		// Continuation chunks may only change m and q. A fresh command aborts
		// an unfinished transfer rather than accidentally inheriting its data.
		continuation := true
		for key := range p {
			if key != "m" && key != "q" {
				continuation = false
			}
		}
		if continuation {
			for key, value := range p {
				d.kitty.params[key] = value
			}
			if _, ok := p["m"]; !ok {
				d.kitty.params["m"] = "0"
			}
			p = d.kitty.params
		} else {
			d.kitty = nil
		}
	}
	id, e1 := number(p, "i", 0)
	num, e2 := number(p, "I", 0)
	pid, e3 := number(p, "p", 0)
	quiet, e4 := number(p, "q", 0)
	replyRequested := id != 0 || num != 0 || p["a"] == "q"
	defer func() {
		if err != nil {
			d.kitty = nil
		}
		if quiet >= 2 || (err == nil && quiet == 1) || !replyRequested {
			return
		}
		status := "OK"
		if err != nil {
			status = err.Error()
		}
		fields := fmt.Sprintf("i=%d", id)
		if num != 0 {
			fields += fmt.Sprintf(",I=%d", num)
		}
		if pid != 0 {
			fields += fmt.Sprintf(",p=%d", pid)
		}
		result.Reply = "\x1b_G" + fields + ";" + status + "\x1b\\"
	}()
	if e1 != nil || e2 != nil || e3 != nil || e4 != nil || (id != 0 && num != 0) || quiet > 2 {
		return result, fmt.Errorf("EINVAL: invalid image identifier or quiet flag")
	}
	for _, key := range []string{"U", "P", "Q", "z"} {
		if p[key] != "" && p[key] != "0" {
			return result, fmt.Errorf("ENOTSUP: layered and virtual placements are not supported")
		}
	}
	action := p["a"]
	if action == "" {
		action = "t"
	}
	if action == "d" {
		return d.deleteKitty(p, uint32(id), uint32(num), uint32(pid))
	}
	if action != "t" && action != "T" && action != "p" && action != "q" {
		return result, fmt.Errorf("ENOTSUP: graphics action is not supported")
	}
	if medium := p["t"]; medium != "" && medium != "d" {
		return result, fmt.Errorf("ENOTSUP: only in-band image data is supported")
	}
	var img *image.NRGBA
	if action == "p" {
		if num != 0 {
			id = int(d.byNumber(uint32(num)))
		}
		cached, ok := d.images[uint32(id)]
		if !ok {
			return result, fmt.Errorf("ENOENT: unknown image")
		}
		img = cached.image
	} else {
		more, e := number(p, "m", 0)
		if e != nil || more > 1 {
			return result, fmt.Errorf("EINVAL: invalid chunk flag")
		}
		if d.kitty == nil {
			d.kitty = &transfer{params: p}
		}
		if len(d.kitty.data)+len(payload) > MaxSequenceBytes {
			return result, fmt.Errorf("E2BIG: image transfer exceeds limit")
		}
		d.kitty.data = append(d.kitty.data, payload...)
		if more == 1 {
			// There is no acknowledgement until the final chunk.
			quiet = 2
			return result, nil
		}
		data, e := decodeBase64(d.kitty.data)
		d.kitty = nil
		if e != nil {
			return result, e
		}
		img, e = decodeKitty(data, p)
		if e != nil {
			return result, e
		}
		if action == "q" {
			return result, nil
		}
	}
	// Validate placement before committing the upload, so malformed geometry
	// cannot evict a valid cached image or clear its existing placements.
	if action == "T" || action == "p" {
		result.Paint, err = kittyPlacement(img, p, ctx)
		if err != nil {
			return Result{}, err
		}
	}
	if action != "p" {
		if id == 0 {
			id = int(d.allocateID())
		}
		if _, exists := d.images[uint32(id)]; exists {
			result.Delete = &Deletion{Kind: 'i', ID: uint32(id)}
		}
		d.cache(uint32(id), uint32(num), img)
	}
	if result.Paint != nil {
		result.Paint.ID, result.Paint.PlacementID = uint32(id), uint32(pid)
		if pid != 0 && result.Delete == nil {
			result.Delete = &Deletion{Kind: 'i', ID: uint32(id), PlacementID: uint32(pid)}
		}
	}
	return result, nil
}

func decodeKitty(data []byte, p map[string]string) (*image.NRGBA, error) {
	f, err := number(p, "f", 32)
	if err != nil || (f != 24 && f != 32 && f != 100) {
		return nil, fmt.Errorf("EINVAL: unsupported pixel format")
	}
	if compression := p["o"]; compression != "" {
		if compression != "z" {
			return nil, fmt.Errorf("ENOTSUP: unsupported compression")
		}
		r, err := zlib.NewReader(bytes.NewReader(data))
		if err != nil {
			return nil, fmt.Errorf("EINVAL: invalid zlib image")
		}
		decoded, readErr := io.ReadAll(io.LimitReader(r, MaxPixels*4+1))
		closeErr := r.Close()
		if readErr != nil || closeErr != nil {
			return nil, fmt.Errorf("EINVAL: invalid compressed image")
		}
		if len(decoded) > MaxPixels*4 {
			return nil, fmt.Errorf("E2BIG: decompressed image exceeds limit")
		}
		data = decoded
	}
	if f == 100 {
		return decodeFile(data, true)
	}
	w, e1 := number(p, "s", 0)
	h, e2 := number(p, "v", 0)
	if e1 != nil || e2 != nil {
		return nil, fmt.Errorf("EINVAL: invalid image dimensions")
	}
	if err := dimensions(w, h); err != nil {
		return nil, err
	}
	stride := f / 8
	if len(data) != w*h*stride {
		return nil, fmt.Errorf("EINVAL: pixel data length does not match dimensions")
	}
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for src, dst := 0, 0; src < len(data); src, dst = src+stride, dst+4 {
		copy(img.Pix[dst:dst+3], data[src:src+3])
		img.Pix[dst+3] = 255
		if stride == 4 {
			img.Pix[dst+3] = data[src+3]
		}
	}
	return img, nil
}

func kittyPlacement(img *image.NRGBA, p map[string]string, ctx Context) (*Placement, error) {
	n := make(map[string]int)
	for _, key := range []string{"x", "y", "w", "h", "c", "r", "X", "Y", "C"} {
		value, err := number(p, key, 0)
		if err != nil {
			return nil, err
		}
		n[key] = value
	}
	if n["X"] >= ctx.CellWidth || n["Y"] >= ctx.CellHeight || n["C"] > 1 {
		return nil, fmt.Errorf("EINVAL: invalid placement offset")
	}
	w, h := n["w"], n["h"]
	if w == 0 {
		w = img.Bounds().Dx() - n["x"]
	}
	if h == 0 {
		h = img.Bounds().Dy() - n["y"]
	}
	source := image.Rect(n["x"], n["y"], n["x"]+w, n["y"]+h).Intersect(img.Bounds())
	if w <= 0 || h <= 0 || source.Empty() {
		return nil, fmt.Errorf("EINVAL: crop is outside the image")
	}
	return sizePlacement(&Placement{Image: img, Source: source, Move: n["C"] == 0, OffsetX: n["X"], OffsetY: n["Y"]}, n["c"], n["r"], ctx)
}

func (d *Decoder) allocateID() uint32 {
	for {
		d.nextID++
		if d.nextID != 0 {
			if _, exists := d.images[d.nextID]; !exists {
				return d.nextID
			}
		}
	}
}

func (d *Decoder) byNumber(number uint32) uint32 {
	var id uint32
	var used uint64
	for key, entry := range d.images {
		if entry.number == number && entry.used > used {
			id, used = key, entry.used
		}
	}
	return id
}

func (d *Decoder) cache(id, number uint32, img *image.NRGBA) {
	if d.images == nil {
		d.images = make(map[uint32]cachedImage)
	}
	d.drop(id)
	for len(d.images) >= maxImages || d.bytes+len(img.Pix) > MaxCacheBytes {
		var oldest uint32
		used := ^uint64(0)
		for key, entry := range d.images {
			if entry.used < used {
				oldest, used = key, entry.used
			}
		}
		d.drop(oldest)
	}
	d.serial++
	d.images[id] = cachedImage{image: img, number: number, used: d.serial}
	d.bytes += len(img.Pix)
}

func (d *Decoder) drop(id uint32) {
	if entry, ok := d.images[id]; ok {
		d.bytes -= len(entry.image.Pix)
		delete(d.images, id)
	}
}

func (d *Decoder) deleteKitty(p map[string]string, id, imageNumber, pid uint32) (Result, error) {
	kind := p["d"]
	if kind == "" {
		kind = "a"
	}
	if len(kind) != 1 {
		return Result{}, fmt.Errorf("EINVAL: invalid deletion selector")
	}
	lower := strings.ToLower(kind)[0]
	if !strings.ContainsRune("aincpxyr", rune(lower)) {
		return Result{}, fmt.Errorf("ENOTSUP: deletion selector is not supported")
	}
	x, e1 := number(p, "x", 0)
	y, e2 := number(p, "y", 0)
	if e1 != nil || e2 != nil {
		return Result{}, fmt.Errorf("EINVAL: invalid deletion coordinates")
	}
	if lower == 'n' {
		id = d.byNumber(imageNumber)
		lower = 'i'
	}
	free := kind[0] >= 'A' && kind[0] <= 'Z'
	if free {
		switch lower {
		case 'a':
			clear(d.images)
			d.bytes = 0
		case 'i':
			d.drop(id)
		case 'r':
			for key := range d.images {
				if key >= uint32(x) && key <= uint32(y) {
					d.drop(key)
				}
			}
		default:
			return Result{}, fmt.Errorf("ENOTSUP: freeing images by coordinates is not supported")
		}
	}
	return Result{Delete: &Deletion{Kind: lower, ID: id, PlacementID: pid, X: x, Y: y, Free: free}}, nil
}
