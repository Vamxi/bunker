package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/gdamore/tcell/v2"
)

func TestCanonicalKey(t *testing.T) {
	for _, tc := range []struct{ in, want string }{
		{"f2", "f2"},
		{"F2", "f2"},
		{"shift+ctrl+T", "ctrl+shift+t"},
		{"ctrl+pagedown", "ctrl+pgdn"},
		{"alt+esc", "alt+escape"},
		{"ctrl+shift+0", "ctrl+shift+0"},
		{"ctrl+comma", "ctrl+comma"},
		{"none", "none"},
		{"<Control>Right", "ctrl+right"},
		{"<Primary><Shift>Page_Up", "ctrl+shift+pgup"},
		{"<Control>F24", "ctrl+f24"},
	} {
		t.Run(tc.in, func(t *testing.T) {
			got, err := canonicalKey(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("canonicalKey(%q) = %q, %v; want %q", tc.in, got, err, tc.want)
			}
		})
	}
	for _, bad := range []string{"", "ctrl+", "hyper+x", "ctrl+exclamation", "f25", "f01", "<Super>x"} {
		t.Run("rejects "+bad, func(t *testing.T) {
			if got, err := canonicalKey(bad); err == nil {
				t.Fatalf("canonicalKey(%q) = %q, want an error", bad, got)
			}
		})
	}
}

// The shipped config must load without a single shortcut problem.
func TestDefaultConfigHasNoKeyProblems(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(DefaultConfigTOML()), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(path, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.KeyProblems) != 0 {
		t.Fatalf("default config reports: %q", cfg.KeyProblems)
	}
	for _, w := range windowShortcuts {
		if want, _ := canonicalKey(w.def); cfg.WindowKeys[w.action] != want {
			t.Errorf("%s = %q, want the default %q", w.action, cfg.WindowKeys[w.action], want)
		}
	}
	if cfg.Cursor.Blink != "system" {
		t.Errorf("cursor.blink = %q, want system", cfg.Cursor.Blink)
	}
}

func TestResolveWindowKeys(t *testing.T) {
	keys, problems := resolveWindowKeys(map[string]string{"new_tab": "F2", "next_tab": "ctrl+nothing", "close_tab": "none"})
	if keys["new_tab"] != "f2" || keys["close_tab"] != "none" {
		t.Fatalf("overrides not applied: %v", keys)
	}
	if keys["next_tab"] != "ctrl+pgdn" || len(problems) != 1 || !strings.Contains(problems[0], "next_tab") {
		t.Fatalf("invalid override should keep the default and be reported: %q %q", keys["next_tab"], problems)
	}
}

func TestFindKeyClashes(t *testing.T) {
	defaults := func() (map[string]string, map[string]string) {
		win, _ := resolveWindowKeys(nil)
		pane := map[string]string{}
		for _, e := range keybindingDefaults {
			pane[e.action] = e.def
		}
		return win, pane
	}
	for _, tc := range []struct {
		name   string
		change func(win, pane map[string]string)
		want   string // substring of the one clash, "" for none
	}{
		{"defaults", func(map[string]string, map[string]string) {}, ""},
		{"window and pane", func(win, pane map[string]string) { win["new_tab"] = "f1" }, "F1 is set for both new_tab and split; new_tab wins"},
		{"pane and window, window wins", func(win, pane map[string]string) { pane["zoom"] = "ctrl+shift+t" }, "new_tab wins"},
		{"two panes", func(win, pane map[string]string) { pane["zoom"] = "f1" }, "F1 is set for both split and zoom; only one of them works"},
		{"pane and search never fire together", func(win, pane map[string]string) { pane["search_next"] = "f1" }, ""},
		{"window and search", func(win, pane map[string]string) { win["new_tab"] = "ctrl+n" }, "new_tab wins"},
		{"none never clashes", func(win, pane map[string]string) { win["new_tab"], win["close_tab"] = "none", "none" }, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			win, pane := defaults()
			tc.change(win, pane)
			got := findKeyClashes(win, pane)
			switch {
			case tc.want == "" && len(got) != 0:
				t.Fatalf("unexpected clashes %q", got)
			case tc.want != "" && (len(got) != 1 || !strings.Contains(got[0], tc.want)):
				t.Fatalf("clashes %q, want one containing %q", got, tc.want)
			}
		})
	}
}

func TestParseKeyNone(t *testing.T) {
	kb, err := parseKey("none")
	if err != nil {
		t.Fatal(err)
	}
	if kb.Matches(tcell.NewEventKey(tcell.KeyNUL, 0, tcell.ModNone)) || kb.Matches(tcell.NewEventKey(tcell.KeyF1, 0, tcell.ModNone)) {
		t.Fatal("a disabled binding matched a key")
	}
}

func TestConfigSetGetList(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	flagConfig = path
	t.Cleanup(func() { flagConfig = "" })
	run := func(args ...string) (string, error) {
		var out, errOut bytes.Buffer
		rootCmd.SetOut(&out)
		rootCmd.SetErr(&errOut)
		rootCmd.SetArgs(append([]string{"--config", path, "config"}, args...))
		t.Cleanup(func() { rootCmd.SetOut(nil); rootCmd.SetErr(nil); rootCmd.SetArgs(nil) })
		err := rootCmd.Execute()
		return out.String() + errOut.String(), err
	}
	for _, tc := range []struct {
		setting, value, want string
	}{
		{"keys.new_tab", "F2", "f2"},
		{"keys.next_tab", "<Control>Right", "ctrl+right"},
		{"keys.split", "none", "none"},
		{"cursor.blink", "off", "off"},
		{"tabs.position", "top", "top"},
		{"tabs.autohide", "true", "true"},
		{"window.padding", "4", "4"},
		{"theme", "nord", "nord"},
		{"ui.active_border", "#ff8800", "#ff8800"},
	} {
		t.Run(tc.setting, func(t *testing.T) {
			if out, err := run("set", tc.setting, tc.value); err != nil {
				t.Fatalf("set: %v\n%s", err, out)
			}
			out, err := run("get", tc.setting)
			if err != nil || strings.TrimSpace(out) != tc.want {
				t.Fatalf("get = %q, %v; want %q", out, err, tc.want)
			}
		})
	}
	for _, tc := range []struct{ setting, value string }{
		{"keys.new_tab", "ctrl+exclamation"},
		{"keys.split", "ctrl+comma"}, // bunk's reader has no comma
		{"cursor.blink", "sometimes"},
		{"tabs.width", "5"},
		{"theme", "no-such-theme"},
		{"no.such", "1"},
	} {
		t.Run("rejects "+tc.setting+"="+tc.value, func(t *testing.T) {
			before, _ := os.ReadFile(path)
			if _, err := run("set", tc.setting, tc.value); err == nil {
				t.Fatal("set accepted an invalid value")
			}
			if after, _ := os.ReadFile(path); !bytes.Equal(before, after) {
				t.Fatal("a rejected value changed the file")
			}
		})
	}
	out, err := run("list")
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range []string{`keys.new_tab = "f2"`, `cursor.blink = "off"`, `keys.close_tab = "ctrl+shift+w"`} {
		if !strings.Contains(out, line+"\n") {
			t.Errorf("list lacks %s:\n%s", line, out)
		}
	}
	if data, _ := os.ReadFile(path); !strings.Contains(string(data), "# bunker configuration") {
		t.Error("set dropped the file's comments")
	}
}
