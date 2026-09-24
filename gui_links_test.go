package main

import (
	"testing"

	"bunker/internal/vt10x"
)

func glyphLine(s string) []vt10x.Glyph {
	var line []vt10x.Glyph
	for _, r := range s {
		line = append(line, vt10x.Glyph{Char: r, Width: 1})
	}
	return line
}

func TestPlainURLAt(t *testing.T) {
	for _, tt := range []struct {
		text string
		col  int
		want string
	}{
		{"see https://example.com/a?b=1 now", 10, "https://example.com/a?b=1"},
		{"end of sentence https://go.dev.", 20, "https://go.dev"},
		{"(docs at https://en.wikipedia.org/wiki/Go_(language))", 15, "https://en.wikipedia.org/wiki/Go_(language)"},
		{"[link](https://x.io/p)", 10, "https://x.io/p"},
		{"mail mailto:me@example.com please", 8, "mailto:me@example.com"},
		{"no url here", 3, ""},
		{"https://a.io and https://b.io", 20, "https://b.io"},
		{"https://a.io and https://b.io", 13, ""}, // between them
	} {
		got, _, _, ok := plainURLAt(glyphLine(tt.text), tt.col)
		if !ok {
			got = ""
		}
		if got != tt.want {
			t.Errorf("%q @%d: got %q, want %q", tt.text, tt.col, got, tt.want)
		}
	}
	// Wide characters before the URL: the span is in cells.
	line := []vt10x.Glyph{{Char: '日', Width: 2}, {Width: -1}, {Char: ' ', Width: 1}}
	line = append(line, glyphLine("http://x.io")...)
	if u, c0, c1, ok := plainURLAt(line, 5); !ok || u != "http://x.io" || c0 != 3 || c1 != 14 {
		t.Errorf("after a wide char: %q [%d,%d) ok=%v", u, c0, c1, ok)
	}
}

func TestSafeLink(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com":         true,
		"http://localhost:8080/x":     true,
		"mailto:me@example.com":       true,
		"file:///home/me/notes.txt":   true,
		"file://localhost/etc/hosts":  true,
		"file://elsewhere/etc/passwd": false,
		"javascript:alert(1)":         false,
		"ssh://host":                  false,
		"x-scheme-handler:run":        false,
		"https://":                    false,
		"not a url":                   false,
	} {
		if got := safeLink(u); got != want {
			t.Errorf("safeLink(%q) = %v, want %v", u, got, want)
		}
	}
}
