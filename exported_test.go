package main

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

func TestKeybindingString(t *testing.T) {
	for _, raw := range []string{"f1", "ctrl+q", "alt+up", "shift+pgup", "ctrl+shift+t"} {
		t.Run(raw, func(t *testing.T) {
			kb, err := parseKey(raw)
			if err != nil {
				t.Fatal(err)
			}
			if kb.String() != raw {
				t.Fatalf("String() = %q, want %q", kb.String(), raw)
			}
		})
	}
}

func TestPaneSetStatus(t *testing.T) {
	for _, tc := range []struct {
		name   string
		dur    time.Duration
		active bool
	}{
		{"shown for its duration", time.Minute, true},
		{"expired", -time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var p Pane
			p.SetStatus("COPIED", tc.dur)
			p.mu.Lock()
			defer p.mu.Unlock()
			active := p.statusMsg == "COPIED" && time.Now().Before(p.statusMsgEnd)
			if active != tc.active {
				t.Fatalf("status active = %v, want %v", active, tc.active)
			}
		})
	}
}

func TestExecuteVersion(t *testing.T) {
	var out bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetArgs([]string{"--version"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetArgs(nil)
	})
	Execute()
	if !strings.Contains(out.String(), Version) {
		t.Fatalf("--version printed %q, want the version %q", out.String(), Version)
	}
}

// The standards reserve short flags for --debug and --config.
func TestRootFlagShorthands(t *testing.T) {
	for _, tc := range []struct{ name, short string }{
		{"debug", "d"}, {"config", "c"}, {"trace", ""}, {"theme", ""}, {"version", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := rootCmd.Flags().Lookup(tc.name)
			if f == nil {
				f = rootCmd.PersistentFlags().Lookup(tc.name)
			}
			if f == nil {
				t.Fatalf("--%s is not defined", tc.name)
			}
			if f.Shorthand != tc.short {
				t.Fatalf("--%s shorthand %q, want %q", tc.name, f.Shorthand, tc.short)
			}
		})
	}
}
