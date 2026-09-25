package vt10x

import (
	"testing"
	"unicode"

	"github.com/rivo/uniseg"
)

// Every code point: the table's width is uniseg's, and whenever
// startsCluster says a rune begins a new cluster, uniseg agrees after a
// context in each of its grapheme states (the rune before being anything
// but Prepend or ZWJ, which startsCluster leaves to uniseg).
func TestUniInfoMatchesUniseg(t *testing.T) {
	for r, want := range map[rune]int{'a': uniPropAny, '中': uniPropXX, 0x1F600: uniPropExtPic, 0x0600: uniPropPrep, 0x200D: uniPropZWJ} {
		if _, _, _, st := uniseg.FirstGraphemeClusterInString(string(r), -1); st>>uniShiftProp != want {
			t.Fatalf("uniseg property of %U is %d, want %d: update the uniProp constants", r, st>>uniShiftProp, want)
		}
	}
	contexts := []string{
		"a", "中", "\u200b", // Any, XX, Control
		"😀", "😀\u0301", // Extended_Pictographic, then Extend
		"\u0301", "e\u0903", // Extend, SpacingMark
		"🇦", "🇦🇦", // Regional indicators, odd and even
		"ᄀ", "ᅡ", "ᆨ", "가", "각", // Hangul L, V, T, LV, LVT
	}
	last := func(s string) rune { r := []rune(s); return r[len(r)-1] }
	step := rune(1)
	if testing.Short() || raceBuild {
		step = 97
	}
	for r := rune(0); r <= unicode.MaxRune; r += step {
		s := string(r)
		info := uniInfo(r)
		if w := uniseg.StringWidth(s); int(info&uniWidth) != w {
			t.Fatalf("%U: width %d, uniseg %d", r, info&uniWidth, w)
		}
		for _, ctx := range contexts {
			if !startsCluster(last(ctx), r) {
				continue
			}
			if cluster, _, _, _ := uniseg.FirstGraphemeClusterInString(ctx+s, -1); cluster != ctx {
				t.Fatalf("%U after %+q: startsCluster says new cluster, uniseg joins %+q", r, ctx, cluster)
			}
		}
	}
	for _, sticky := range []rune{0x0600, 0x200D} {
		if startsCluster(sticky, 'a') || startsCluster(sticky, 0x1F600) {
			t.Fatalf("after %U the rune may join; startsCluster must defer to uniseg", sticky)
		}
	}
}

func BenchmarkRuneCellWidth(b *testing.B) {
	for b.Loop() {
		for r := rune(0x4E00); r < 0x4E00+256; r++ {
			runeCellWidth(r)
		}
	}
}
