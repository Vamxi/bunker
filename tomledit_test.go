package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

func TestSetTOMLKey(t *testing.T) {
	const base = `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"
`
	tests := []struct {
		name, section, key, literal, want string
	}{
		{"replace keeps comment", "", "theme", `"nord"`, `# bunker configuration
theme = "nord"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"
`},
		{"uncommented default goes under its comment", "", "scrollback", `5000`, `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000
scrollback = 5000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"
`},
		{"new top-level key before next table's comment", "", "font", `"Monospace 12"`, `# bunker configuration
theme = "default"  # pick one
font = "Monospace 12"
# scrollback = 10000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"
`},
		{"key inside table", "tabs", "position", `"top"`, `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "top"

[keys]
copy = "ctrl+c"
`},
		{"new key appended to table", "tabs", "width", `240`, `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "left"
width = 240

[keys]
copy = "ctrl+c"
`},
		{"new key in last table", "keys", "paste", `"ctrl+v"`, `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"
paste = "ctrl+v"
`},
		{"missing table appended", "window", "padding", `12`, `# bunker configuration
theme = "default"  # pick one
# scrollback = 10000

# Tabs
[tabs]
position = "left"

[keys]
copy = "ctrl+c"

[window]
padding = 12
`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := setTOMLKey(base, tt.section, tt.key, tt.literal)
			if got != tt.want {
				t.Fatalf("got:\n%s\nwant:\n%s", got, tt.want)
			}
			var v map[string]any
			if _, err := toml.Decode(got, &v); err != nil {
				t.Fatalf("result is not valid TOML: %v", err)
			}
		})
	}
}

func TestSetTOMLKeyEmptyFile(t *testing.T) {
	got := setTOMLKey("", "", "theme", `"nord"`)
	got = setTOMLKey(got, "tabs", "position", `"right"`)
	var v struct {
		Theme string
		Tabs  struct{ Position string }
	}
	if _, err := toml.Decode(got, &v); err != nil || v.Theme != "nord" || v.Tabs.Position != "right" {
		t.Fatalf("decode %q: %+v %v", got, v, err)
	}
}

func TestSetTOMLKeyStringValues(t *testing.T) {
	// Quoted values containing '#' and escaped quotes must be replaced whole.
	src := "font = \"A \\\"B\\\" #1\" # note\n"
	got := setTOMLKey(src, "", "font", tomlString(`Fira "Code" #2`))
	var v struct{ Font string }
	if _, err := toml.Decode(got, &v); err != nil || v.Font != `Fira "Code" #2` {
		t.Fatalf("got %q -> %+v %v", got, v, err)
	}
	if want := "font = \"Fira \\\"Code\\\" #2\" # note\n"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if s := tomlString("a\tb\\c"); s != `"a\u0009b\\c"` {
		t.Fatalf("tomlString = %s", s)
	}
}

func TestWriteConfigKey(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bunker", "config.toml")

	// Missing file: created from the documented template, then edited.
	changed, err := writeConfigKey(path, "", "theme", tomlString("nord"))
	if err != nil || !changed {
		t.Fatalf("first write: changed=%v err=%v", changed, err)
	}
	data, _ := os.ReadFile(path)
	if !strings.Contains(string(data), "# bunker configuration") {
		t.Fatal("new file should start from the documented template")
	}
	cfg, err := LoadConfig(path, "")
	if err != nil || cfg.ThemeName != "nord" {
		t.Fatalf("load after write: theme=%q err=%v", cfg.ThemeName, err)
	}

	// Same value again: no rewrite.
	if changed, err := writeConfigKey(path, "", "theme", tomlString("nord")); err != nil || changed {
		t.Fatalf("idempotent write: changed=%v err=%v", changed, err)
	}

	// Table keys round-trip through LoadConfig with validation applied.
	if _, err := writeConfigKey(path, "tabs", "position", tomlString("top")); err != nil {
		t.Fatal(err)
	}
	if _, err := writeConfigKey(path, "window", "padding", "999"); err != nil {
		t.Fatal(err)
	}
	cfg, err = LoadConfig(path, "")
	if err != nil || cfg.Tabs.Position != "top" || cfg.Padding != maxPadding {
		t.Fatalf("got tabs=%q padding=%d err=%v", cfg.Tabs.Position, cfg.Padding, err)
	}

	// A literal that would corrupt the file is refused and the file kept.
	before, _ := os.ReadFile(path)
	if _, err := writeConfigKey(path, "", "theme", `"unterminated`); err == nil {
		t.Fatal("invalid literal must be refused")
	}
	if after, _ := os.ReadFile(path); string(after) != string(before) {
		t.Fatal("refused write must leave the file untouched")
	}
}

func TestLoadConfigGUIFieldsDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("font = \"\"\n[tabs]\nposition = \"diagonal\"\nwidth = 5\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Font != defaultFont || cfg.Tabs.Position != "left" || cfg.Tabs.Width != minTabsWidth || cfg.Padding != defaultPadding {
		t.Fatalf("defaults not applied: %+v", cfg)
	}
}
