package vt10x

import (
	"fmt"
	"strconv"
	"strings"
)

// StatusString returns a DECRQSS reply, including a negative reply for settings
// we do not implement. The caller serializes access with other terminal reads.
func (t *State) StatusString(setting string) string {
	var value string
	switch setting {
	case "m":
		value = sgrStatus(t.cur.Attr)
	case "r":
		value = fmt.Sprintf("%d;%dr", t.top+1, t.bottom+1)
	case " q":
		value = fmt.Sprintf("%d q", t.cur.Shape)
	default:
		return "\x1bP0$r\x1b\\"
	}
	return "\x1bP1$r" + value + "\x1b\\"
}

func sgrStatus(g Glyph) string {
	params := []string{"0"}
	for _, attr := range []struct {
		bit  int16
		code int
	}{
		{attrBold, 1}, {attrDim, 2}, {attrItalic, 3}, {attrBlink, 5},
		{attrReverse, 7}, {attrInvisible, 8}, {attrStrikethrough, 9}, {attrOverline, 53},
	} {
		if g.Mode&attr.bit != 0 {
			params = append(params, strconv.Itoa(attr.code))
		}
	}
	if g.Mode&attrUnderline != 0 {
		style := (g.Mode&attrUnderlineStyleMask)/attrUnderlineStyleBit0 + 1
		params = append(params, fmt.Sprintf("4:%d", style))
	}
	for _, c := range []struct {
		code    int
		color   Color
		enabled bool
	}{
		{38, g.FG, g.FG != DefaultFG}, {48, g.BG, g.BG != DefaultBG},
		{58, g.UL, g.Mode&attrHasULColor != 0},
	} {
		if !c.enabled {
			continue
		}
		if c.color < 256 {
			params = append(params, fmt.Sprintf("%d;5;%d", c.code, c.color))
		} else if c.color < DefaultFG {
			params = append(params, fmt.Sprintf("%d;2;%d;%d;%d", c.code, c.color>>16&255, c.color>>8&255, c.color&255))
		}
	}
	return strings.Join(params, ";") + "m"
}
