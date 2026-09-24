package main

import (
	"strconv"
	"testing"
)

func TestPtyxisPalettesLoad(t *testing.T) {
	for _, name := range []string{"gnome", "gnome-light", "solarized", "solarized-light", "tango", "vs-code", "horizon", "high-contrast", "dark-pastel"} {
		if _, ok := BuiltinThemes[name]; !ok {
			t.Errorf("theme %q missing", name)
		}
	}
	// bunk's own themes keep their colours when Ptyxis has one by that name.
	if BuiltinThemes["dracula"].Background != "#282A36" || BuiltinThemes["default"].Background != "#1C1C1F" {
		t.Errorf("a Ptyxis palette replaced one of bunk's themes")
	}
	for name, d := range BuiltinThemes {
		if name == "terminal" {
			continue
		}
		rt := resolveTheme(d)
		if rt.bg == hexColor("") || rt.fg == hexColor("") {
			t.Errorf("theme %q has an unusable background or foreground", name)
		}
	}
}

func TestParsePtyxisPalette(t *testing.T) {
	single := "[Palette]\nName=My Theme+\nBackground=#000000\nForeground=#FFFFFF\nCursor=#FFFFFF\n"
	for i := range 16 {
		single += "Color" + strconv.Itoa(i) + "=#101010\n"
	}
	got := parsePtyxisPalette(single)
	if _, ok := got["my-theme-plus"]; !ok || len(got) != 1 {
		t.Fatalf("single variant: %v", keys(got))
	}
	// Missing a colour: skipped, not guessed.
	if got := parsePtyxisPalette("[Palette]\nName=Broken\nBackground=#000000\nForeground=#FFFFFF\n"); len(got) != 0 {
		t.Fatalf("incomplete palette accepted: %v", keys(got))
	}
}

func keys(m map[string]ThemeDef) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	return out
}
