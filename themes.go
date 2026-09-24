// themes.go - bundled theme collections.
//
// themes/ptyxis holds a lean set of Ptyxis (GNOME's terminal) palettes;
// see its README. Every .palette file there is bundled. They are embedded and parsed at startup into
// BuiltinThemes alongside bunk's own themes, which keep their names when both
// define one.
//
// Ptyxis .palette format (INI): a [Palette] section with Name and either
// the colours directly, or [Light] and [Dark] sections with them:
//
//	Background, Foreground, Cursor, Color0 … Color15   (#RRGGBB)
//
// A single-variant palette becomes one theme ("VS Code" → vs-code). A
// two-variant one becomes name (dark) and name-light.
package main

import (
	"bufio"
	"embed"
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

//go:embed themes/ptyxis/*.palette
var ptyxisPalettes embed.FS

func init() {
	entries, err := fs.Glob(ptyxisPalettes, "themes/ptyxis/*.palette")
	if err != nil {
		return
	}
	for _, path := range entries {
		data, err := ptyxisPalettes.ReadFile(path)
		if err != nil {
			continue
		}
		for name, def := range parsePtyxisPalette(string(data)) {
			if _, taken := BuiltinThemes[name]; !taken {
				BuiltinThemes[name] = def
			}
		}
	}
}

var themeSlugInvalid = regexp.MustCompile(`[^a-z0-9]+`)

// themeSlug turns a display name into a config-friendly theme name.
func themeSlug(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = strings.NewReplacer("é", "e", "è", "e", "ê", "e", "ü", "u", "ö", "o", "ä", "a", "+", " plus ").Replace(s)
	return strings.Trim(themeSlugInvalid.ReplaceAllString(s, "-"), "-")
}

// parsePtyxisPalette returns the palette's themes keyed by name. Incomplete
// variants (missing a colour) are skipped rather than guessed.
func parsePtyxisPalette(text string) map[string]ThemeDef {
	sections := map[string]map[string]string{}
	section := ""
	sc := bufio.NewScanner(strings.NewReader(text))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, ";") {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = strings.TrimSpace(line[1 : len(line)-1])
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		if sections[section] == nil {
			sections[section] = map[string]string{}
		}
		sections[section][strings.TrimSpace(k)] = strings.TrimSpace(v)
	}
	name := themeSlug(sections["Palette"]["Name"])
	if name == "" {
		return nil
	}
	out := map[string]ThemeDef{}
	dark, hasDark := paletteTheme(sections["Dark"])
	light, hasLight := paletteTheme(sections["Light"])
	switch {
	case hasDark || hasLight:
		if hasDark {
			out[name] = dark
		}
		if hasLight {
			key := name + "-light"
			if !hasDark {
				key = name
			}
			out[key] = light
		}
	default:
		if def, ok := paletteTheme(sections["Palette"]); ok {
			out[name] = def
		}
	}
	return out
}

// paletteTheme builds a ThemeDef from one variant's keys. Pane borders and
// the scrollbar follow the palette like bunk's themes: the light grey
// (colour 7) for focus and the thumb, the dark grey (colour 8) for the rest.
func paletteTheme(kv map[string]string) (ThemeDef, bool) {
	if kv == nil {
		return ThemeDef{}, false
	}
	valid := func(s string) bool { return hexColor(s) != hexColor("") }
	var d ThemeDef
	d.Background, d.Foreground = kv["Background"], kv["Foreground"]
	if !valid(d.Background) || !valid(d.Foreground) {
		return ThemeDef{}, false
	}
	for i := range d.Palette {
		c := kv["Color"+strconv.Itoa(i)]
		if !valid(c) {
			return ThemeDef{}, false
		}
		d.Palette[i] = c
	}
	d.ActiveBorder, d.InactiveBorder = d.Palette[7], d.Palette[8]
	d.ScrollThumb = d.Palette[7]
	d.ScrollTrack = blendHex(d.Background, d.Foreground, 0.15)
	return d, true
}

// blendHex mixes two #RRGGBB colours, t=0 → a.
func blendHex(a, b string, t float64) string {
	ar, ag, ab := hexColor(a).RGB()
	br, bg, bb := hexColor(b).RGB()
	mix := func(x, y int32) int32 { return int32(float64(x)*(1-t) + float64(y)*t) }
	return fmt.Sprintf("#%02X%02X%02X", mix(ar, br), mix(ag, bg), mix(ab, bb))
}

// themeNames lists every built-in theme, sorted.
func themeNames() []string {
	var names []string
	for n := range BuiltinThemes {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}
