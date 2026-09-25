package main

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		got, _, ok := plainURLAt([][]vt10x.Glyph{glyphLine(tt.text)}, 0, tt.col)
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
	if u, segs, ok := plainURLAt([][]vt10x.Glyph{line}, 0, 5); !ok || u != "http://x.io" || !slices.Equal(segs, []linkSeg{{0, 3, 14}}) {
		t.Errorf("after a wide char: %q %v ok=%v", u, segs, ok)
	}
}

// wrapGrid lays text out in rows of width cols, marking the soft wraps the
// way the emulator does.
func wrapGrid(text string, cols int, softWrap bool) [][]vt10x.Glyph {
	var grid [][]vt10x.Glyph
	for _, line := range strings.Split(text, "\n") {
		g := glyphLine(line)
		for len(g) > cols {
			row := slices.Clone(g[:cols])
			if softWrap {
				row[cols-1].Mode |= vt10x.AttrWrap
			}
			grid = append(grid, row)
			g = g[cols:]
		}
		for len(g) < cols {
			g = append(g, vt10x.Glyph{Width: 1})
		}
		grid = append(grid, g)
	}
	return grid
}

func TestPlainURLAtWrapped(t *testing.T) {
	const u = "https://example.com/a/very/long/path?with=query"
	for _, soft := range []bool{true, false} {
		grid := wrapGrid("see "+u+" ok\nnext line", 20, soft)
		// Every row of the URL finds all of it.
		for row := 0; row < 3; row++ {
			got, segs, ok := plainURLAt(grid, row, 5)
			if !ok || got != u {
				t.Fatalf("soft=%v row %d: got %q ok=%v", soft, row, got, ok)
			}
			want := []linkSeg{{0, 4, 20}, {1, 0, 20}, {2, 0, 11}}
			if !slices.Equal(segs, want) {
				t.Errorf("soft=%v row %d: segs %v, want %v", soft, row, segs, want)
			}
		}
		if got, _, ok := plainURLAt(grid, 3, 1); ok {
			t.Errorf("soft=%v: the next line is not part of the URL: %q", soft, got)
		}
	}
	// A URL that ends where the row ends is not glued to the next row's text
	// unless the terminal wrapped it.
	grid := [][]vt10x.Glyph{glyphLine("go https://go.dev "), glyphLine("tail")}
	if got, _, _ := plainURLAt(grid, 0, 5); got != "https://go.dev" {
		t.Errorf("row ending in a space: %q", got)
	}
}

func TestExplicitLinkSegsWrapped(t *testing.T) {
	grid := wrapGrid("ab"+strings.Repeat("x", 25)+"cd", 10, true)
	for r := range grid {
		for c := range grid[r] {
			if i := r*10 + c; i >= 2 && i < 27 {
				grid[r][c].Link = 7
			}
		}
	}
	want := []linkSeg{{0, 2, 10}, {1, 0, 10}, {2, 0, 7}}
	for _, at := range [][2]int{{0, 3}, {1, 5}, {2, 0}} {
		if segs := explicitLinkSegs(grid, at[0], at[1]); !slices.Equal(segs, want) {
			t.Errorf("at %v: %v, want %v", at, segs, want)
		}
	}
}

func TestSafeLink(t *testing.T) {
	for u, want := range map[string]bool{
		"https://example.com":         true,
		"http://localhost:8080/x":     true,
		"mailto:me@example.com":       true,
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

func TestSafeLocalFile(t *testing.T) {
	dir := t.TempDir()
	doc := filepath.Join(dir, "notes.txt")
	script := filepath.Join(dir, "run.sh")
	launcher := filepath.Join(dir, "evil.desktop")
	os.WriteFile(doc, []byte("hi"), 0o644)                             //nolint:errcheck
	os.WriteFile(script, []byte("#!/bin/sh\n"), 0o755)                 //nolint:errcheck
	os.WriteFile(launcher, []byte("[Desktop Entry]\nExec=x\n"), 0o644) //nolint:errcheck
	for u, want := range map[string]bool{
		"file://" + doc:                           true,
		"file://" + dir:                           true,
		"file://" + script:                        false, // executable
		"file://" + launcher:                      false, // launcher, even when not executable
		"file://" + filepath.Join(dir, "missing"): false,
	} {
		if got := safeLink(u); got != want {
			t.Errorf("safeLink(%q) = %v, want %v", u, got, want)
		}
	}
}
