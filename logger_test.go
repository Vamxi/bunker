package main

import (
	"bytes"
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenTraceFile(t *testing.T) {
	dir := t.TempDir()
	for _, tc := range []struct {
		name   string
		setup  func(path string)
		wantOK bool
	}{
		{"created 0600", func(string) {}, true},
		{"own file truncated and tightened", func(path string) {
			if err := os.WriteFile(path, []byte("old secrets"), 0o644); err != nil {
				t.Fatal(err)
			}
		}, true},
		{"symlink refused", func(path string) {
			target := filepath.Join(dir, "target")
			if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(target, path); err != nil {
				t.Fatal(err)
			}
		}, false},
		{"fifo refused", func(path string) {
			if err := mkfifo(path); err != nil {
				t.Skip("mkfifo:", err)
			}
		}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(dir, strings.ReplaceAll(tc.name, " ", "-")+".log")
			tc.setup(path)
			f, err := openTraceFile(path)
			if (err == nil) != tc.wantOK {
				t.Fatalf("openTraceFile: err = %v, want ok %v", err, tc.wantOK)
			}
			if f == nil {
				if data, _ := os.ReadFile(filepath.Join(dir, "target")); len(data) > 0 && string(data) != "keep" {
					t.Fatalf("symlink target was modified: %q", data)
				}
				return
			}
			defer f.Close() //nolint:errcheck // test cleanup
			info, err := f.Stat()
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode().Perm() != 0o600 || info.Size() != 0 {
				t.Fatalf("mode %v size %d, want 0600 and empty", info.Mode().Perm(), info.Size())
			}
		})
	}
}

func TestInitLoggerRouting(t *testing.T) {
	for _, tc := range []struct {
		name                 string
		opts                 logOptions
		tui                  bool
		wantStderr, wantFile []string // messages that must appear
		notStderr, notFile   []string // messages that must not
	}{
		{name: "no flags", wantStderr: []string{"warn"}, notStderr: []string{"debug", "trace"}, notFile: []string{"warn"}},
		{name: "debug", opts: logOptions{Debug: true}, wantStderr: []string{"debug", "warn"}, notStderr: []string{"trace"}, notFile: []string{"debug"}},
		{name: "trace", opts: logOptions{Trace: true}, wantStderr: []string{"warn"}, notStderr: []string{"debug"}, wantFile: []string{"trace", "debug", "warn"}},
		{name: "debug and trace", opts: logOptions{Debug: true, Trace: true}, wantStderr: []string{"debug"}, notStderr: []string{"trace"}, wantFile: []string{"trace"}},
		{name: "TUI debug goes to the file", opts: logOptions{Debug: true}, tui: true, wantFile: []string{"debug"}, notFile: []string{"trace"}},
		{name: "log_level without flags", opts: logOptions{Level: "info"}, wantStderr: []string{"info"}, notStderr: []string{"debug"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var stderr bytes.Buffer
			o := tc.opts
			o.TracePath = filepath.Join(t.TempDir(), "trace.log")
			if !tc.tui {
				o.Stderr = &stderr
			}
			old := L
			cleanup := initLogger(o)
			ctx := context.Background()
			L.Log(ctx, LevelTrace, "trace")
			L.Debug("debug")
			L.Info("info")
			L.Warn("warn")
			cleanup()
			L = old
			slog.SetDefault(old)
			file, _ := os.ReadFile(o.TracePath)
			check := func(where, text string, want, not []string) {
				for _, m := range want {
					if !strings.Contains(text, "msg="+m) {
						t.Errorf("%s lacks %q:\n%s", where, m, text)
					}
				}
				for _, m := range not {
					if strings.Contains(text, "msg="+m) {
						t.Errorf("%s has %q:\n%s", where, m, text)
					}
				}
			}
			check("stderr", stderr.String(), tc.wantStderr, tc.notStderr)
			check("trace file", string(file), tc.wantFile, tc.notFile)
		})
	}
}
