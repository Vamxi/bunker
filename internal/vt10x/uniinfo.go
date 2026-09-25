package vt10x

import (
	"sync/atomic"
	"unicode"
	"unicode/utf8"

	"github.com/rivo/uniseg"
)

// Per-rune Unicode facts the parser needs for every printed character. They
// come from uniseg itself, computed one 256-rune block at a time on first
// use, so the hot path is an array lookup instead of uniseg's binary
// searches, and the answers are uniseg's by construction.
const (
	uniWidth  = 7      // mask: cell width as uniseg.StringWidth reports it (0-4)
	uniStarts = 1 << 3 // grapheme property Any or Extended_Pictographic
	uniSticky = 1 << 4 // grapheme property Prepend or ZWJ
)

// uniseg 0.4.7 grapheme properties, as they appear in the state it returns
// (property << 4). TestUniInfoMatchesUniseg pins them.
const (
	uniShiftProp  = 4
	uniPropXX     = 0 // unlisted code points, same rules as Any
	uniPropAny    = 1
	uniPropPrep   = 2
	uniPropZWJ    = 14
	uniPropExtPic = 15
)

var uniBlocks [(unicode.MaxRune + 1) >> 8]atomic.Pointer[[256]uint8]

// uniInfo returns r's width and grapheme flags.
func uniInfo(r rune) uint8 {
	if r < 0 || r > unicode.MaxRune {
		r = utf8.RuneError
	}
	b := uniBlocks[r>>8].Load()
	if b == nil {
		b = fillUniBlock(r >> 8)
	}
	return b[r&0xff]
}

// fillUniBlock computes one block. Concurrent fills compute the same bytes,
// so whichever store lands is correct.
func fillUniBlock(hi rune) *[256]uint8 {
	var b [256]uint8
	for lo := range rune(256) {
		b[lo] = computeUniInfo(hi<<8 | lo)
	}
	uniBlocks[hi].Store(&b)
	return &b
}

func computeUniInfo(r rune) uint8 {
	s := string(r)
	info := uint8(uniseg.StringWidth(s))
	_, _, _, state := uniseg.FirstGraphemeClusterInString(s, -1)
	switch state >> uniShiftProp {
	case uniPropXX, uniPropAny, uniPropExtPic:
		info |= uniStarts
	case uniPropPrep, uniPropZWJ:
		info |= uniSticky
	}
	return info
}

// startsCluster reports whether c begins a new grapheme cluster after a
// cluster whose last rune is last. In uniseg's rules (UAX #29 without GB9c),
// a rune of property Any or Extended_Pictographic joins what precedes it only
// after Prepend (GB9b) or after ZWJ in an emoji sequence (GB11); for every
// other pair the caller asks uniseg.
func startsCluster(last, c rune) bool {
	return uniInfo(c)&uniStarts != 0 && uniInfo(last)&uniSticky == 0
}
