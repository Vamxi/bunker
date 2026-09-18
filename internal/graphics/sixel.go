package graphics

import (
	"bytes"
	"fmt"
	"image"
	"image/color"
	"math"
)

type sixelState struct {
	x, y, width, height, selected, work int
	palette                             [256]color.NRGBA
	image                               *image.NRGBA
}

func (d *Decoder) sixel(body []byte) (*image.NRGBA, error) {
	q := bytes.IndexByte(body, 'q')
	if q < 0 || q > 64 {
		return nil, fmt.Errorf("EINVAL: invalid SIXEL header")
	}
	header, _, err := sixelNumbers(body[:q], 0)
	if err != nil {
		return nil, err
	}
	transparent := len(header) > 1 && header[1] == 1
	palette := d.palette
	if !d.paletteSet {
		palette = defaultSixelPalette()
	}
	s := sixelState{palette: palette}
	if err := s.parse(body[q+1:]); err != nil {
		return nil, err
	}
	if err := dimensions(s.width, s.height); err != nil {
		return nil, err
	}
	img := image.NewNRGBA(image.Rect(0, 0, s.width, s.height))
	if !transparent {
		background := palette[0]
		for i := 0; i < len(img.Pix); i += 4 {
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = background.R, background.G, background.B, 255
		}
	}
	s = sixelState{palette: palette, image: img}
	if err := s.parse(body[q+1:]); err != nil {
		return nil, err
	}
	d.palette, d.paletteSet = s.palette, true
	return img, nil
}

func (s *sixelState) parse(data []byte) error {
	for i := 0; i < len(data); {
		c := data[i]
		i++
		switch {
		case c >= '?' && c <= '~':
			if err := s.paint(c, 1); err != nil {
				return err
			}
		case c == '!':
			start := i
			for i < len(data) && data[i] >= '0' && data[i] <= '9' {
				i++
			}
			nums, _, err := sixelNumbers(data[start:i], 0)
			if err != nil || len(nums) != 1 || nums[0] < 1 || i == len(data) || data[i] < '?' || data[i] > '~' {
				return fmt.Errorf("EINVAL: invalid SIXEL repeat")
			}
			if err := s.paint(data[i], nums[0]); err != nil {
				return err
			}
			i++
		case c == '$':
			s.x = 0
		case c == '-':
			s.x = 0
			s.y += 6
		case c == '"' || c == '#':
			nums, end, err := sixelNumbers(data, i)
			if err != nil {
				return err
			}
			i = end
			if c == '"' {
				if len(nums) < 2 || len(nums) > 4 {
					return fmt.Errorf("EINVAL: invalid SIXEL raster attributes")
				}
				if len(nums) == 4 {
					s.width, s.height = max(s.width, nums[2]), max(s.height, nums[3])
				}
			} else {
				if len(nums) == 0 || nums[0] >= len(s.palette) {
					return fmt.Errorf("EINVAL: invalid SIXEL palette index")
				}
				s.selected = nums[0]
				if len(nums) != 1 {
					if len(nums) != 5 {
						return fmt.Errorf("EINVAL: invalid SIXEL colour")
					}
					switch nums[1] {
					case 2:
						if nums[2] > 100 || nums[3] > 100 || nums[4] > 100 {
							return fmt.Errorf("EINVAL: invalid SIXEL RGB colour")
						}
						s.palette[s.selected] = color.NRGBA{uint8((nums[2]*255 + 50) / 100), uint8((nums[3]*255 + 50) / 100), uint8((nums[4]*255 + 50) / 100), 255}
					case 1:
						if nums[2] > 360 || nums[3] > 100 || nums[4] > 100 {
							return fmt.Errorf("EINVAL: invalid SIXEL HLS colour")
						}
						s.palette[s.selected] = sixelHLS(nums[2], nums[3], nums[4])
					default:
						return fmt.Errorf("EINVAL: invalid SIXEL colour space")
					}
				}
			}
		case c == '\r' || c == '\n' || c == '\t': // Transport whitespace is ignored.
		default:
			return fmt.Errorf("EINVAL: invalid SIXEL byte")
		}
		if s.y > MaxDimension || s.width > MaxDimension || s.height > MaxDimension {
			return fmt.Errorf("E2BIG: SIXEL dimensions exceed limit")
		}
	}
	return nil
}

func (s *sixelState) paint(c byte, count int) error {
	if count > MaxDimension || s.x+count > MaxDimension || s.y >= MaxDimension {
		return fmt.Errorf("E2BIG: SIXEL run exceeds limit")
	}
	// Include blank and repeatedly overpainted runs in the CPU-work budget.
	s.work += count * 6
	if s.work > MaxPixels*16 {
		return fmt.Errorf("E2BIG: SIXEL drawing work exceeds limit")
	}
	s.width, s.height = max(s.width, s.x+count), max(s.height, s.y+6)
	if s.image != nil {
		col := s.palette[s.selected]
		for bit := 0; bit < 6; bit++ {
			if (c-'?')&(1<<bit) != 0 {
				for x := s.x; x < s.x+count; x++ {
					s.image.SetNRGBA(x, s.y+bit, col)
				}
			}
		}
	}
	s.x += count
	return nil
}

func sixelNumbers(data []byte, start int) ([]int, int, error) {
	var nums []int
	i := start
	for i < len(data) && ((data[i] >= '0' && data[i] <= '9') || data[i] == ';') {
		n := 0
		for i < len(data) && data[i] >= '0' && data[i] <= '9' {
			n = n*10 + int(data[i]-'0')
			i++
			if n > MaxDimension {
				return nil, i, fmt.Errorf("E2BIG: SIXEL parameter exceeds limit")
			}
		}
		nums = append(nums, n)
		if len(nums) > 5 {
			return nil, i, fmt.Errorf("EINVAL: too many SIXEL parameters")
		}
		if i == len(data) || data[i] != ';' {
			break
		}
		i++
		if i == len(data) {
			nums = append(nums, 0)
		}
	}
	return nums, i, nil
}

func defaultSixelPalette() [256]color.NRGBA {
	var palette [256]color.NRGBA
	base := [16][3]uint8{{0, 0, 0}, {51, 51, 204}, {204, 33, 33}, {51, 204, 51}, {204, 51, 204}, {51, 204, 204}, {204, 204, 51}, {135, 135, 135}, {66, 66, 66}, {84, 84, 255}, {255, 84, 84}, {84, 255, 84}, {255, 84, 255}, {84, 255, 255}, {255, 255, 84}, {255, 255, 255}}
	for i := range palette {
		palette[i] = color.NRGBA{uint8(i), uint8(i), uint8(i), 255}
	}
	for i, c := range base {
		palette[i] = color.NRGBA{c[0], c[1], c[2], 255}
	}
	return palette
}

func sixelHLS(hue, lightness, saturation int) color.NRGBA {
	h := float64((hue+240)%360) / 60
	l, s := float64(lightness)/100, float64(saturation)/100
	c := (1 - math.Abs(2*l-1)) * s
	x := c * (1 - math.Abs(math.Mod(h, 2)-1))
	var r, g, b float64
	switch {
	case h < 1:
		r, g = c, x
	case h < 2:
		r, g = x, c
	case h < 3:
		g, b = c, x
	case h < 4:
		g, b = x, c
	case h < 5:
		r, b = x, c
	default:
		r, b = c, x
	}
	m := l - c/2
	return color.NRGBA{uint8(math.Round((r + m) * 255)), uint8(math.Round((g + m) * 255)), uint8(math.Round((b + m) * 255)), 255}
}
