package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	for _, tc := range []struct {
		name, content             string
		explicit, exists, wantErr bool
	}{
		{name: "missing default"},
		{name: "missing explicit", explicit: true, wantErr: true},
		{name: "valid explicit", explicit: true, exists: true, content: "scrollback = 123\n"},
		{name: "documented defaults", exists: true, content: DefaultConfigTOML()},
		{name: "invalid syntax", exists: true, content: "theme = [", wantErr: true},
		{name: "invalid type", explicit: true, exists: true, content: "scrollback = 'many'", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("XDG_CONFIG_HOME", t.TempDir())
			path := DefaultConfigPath()
			if tc.exists {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(tc.content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			arg := ""
			if tc.explicit {
				arg = path
			}
			cfg, err := LoadConfig(arg, "")
			if tc.wantErr {
				if err == nil || !strings.Contains(err.Error(), path) {
					t.Fatalf("error must identify config path: %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			want := defaultScrollbackLines
			if tc.name == "valid explicit" {
				want = 123
			}
			if cfg.Scrollback != want {
				t.Fatalf("scrollback = %d, want %d", cfg.Scrollback, want)
			}
		})
	}
	t.Run("unreadable file", func(t *testing.T) {
		if _, err := LoadConfig(t.TempDir(), ""); err == nil {
			t.Fatal("directory accepted as configuration")
		}
	})
	t.Run("startup fails before terminal initialization", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "missing.toml")
		if err := run(path, "", false, false); err == nil || !strings.Contains(err.Error(), path) {
			t.Fatalf("startup error = %v", err)
		}
	})
}
