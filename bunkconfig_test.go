package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBunkConfigWorksInBunker: a bunk config.toml copied as-is into
// ~/.config/bunker/ loads, keeps its settings, and gets bunker's defaults
// for bunker-only keys. testdata/bunk-config.toml is bunk's own documented
// default (jsnjack/bunk config.go at the fork point).
func TestBunkConfigWorksInBunker(t *testing.T) {
	data, err := os.ReadFile("testdata/bunk-config.toml")
	if err != nil {
		t.Fatal(err)
	}
	// A typical customisation on top of bunk's template.
	text := setTOMLKey(string(data), "", "theme", `"dracula"`)
	text = setTOMLKey(text, "keys", "split", `"f2"`)
	text = setTOMLKey(text, "", "scrollback", "5000")
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatalf("bunk config rejected: %v", err)
	}
	if cfg.ThemeName != "dracula" || cfg.Scrollback != 5000 || cfg.Keybindings.Split.raw != "f2" {
		t.Errorf("bunk settings lost: theme %q, scrollback %d, split %q", cfg.ThemeName, cfg.Scrollback, cfg.Keybindings.Split.raw)
	}
	if cfg.Font != defaultFont || cfg.Tabs.Position != "left" || cfg.Padding != defaultPadding {
		t.Errorf("bunker defaults missing: font %q, tabs %q, padding %d", cfg.Font, cfg.Tabs.Position, cfg.Padding)
	}

	// Preferences can edit the copied file without breaking it.
	if _, err := writeConfigKey(path, "tabs", "position", `"top"`); err != nil {
		t.Fatal(err)
	}
	after, _ := os.ReadFile(path)
	if !strings.Contains(string(after), `split         = "f2"`) && !strings.Contains(string(after), `split = "f2"`) {
		t.Errorf("editing the copied file lost the [keys] override:\n%s", after)
	}

	// bunk's "terminal" theme inherits host colours; a window uses default.
	text = setTOMLKey(string(data), "", "theme", `"terminal"`)
	if err := os.WriteFile(path, []byte(text), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if g := guiConfig(cfg); g.ThemeName != defaultThemeName {
		t.Errorf("terminal theme in the GUI resolved to %q", g.ThemeName)
	}
}
