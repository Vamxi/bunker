package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMakeCheckOrdersTestsBeforeBuild(t *testing.T) {
	makefile, err := filepath.Abs("Makefile")
	if err != nil {
		t.Fatal(err)
	}
	for _, fail := range []bool{false, true} {
		name := "success"
		if fail {
			name = "failing tests stop build"
		}
		t.Run(name, func(t *testing.T) {
			dir := t.TempDir()
			logPath := filepath.Join(dir, "steps")
			script := `#!/bin/sh
if [ "$1" = env ]; then printf '%s\n' "$BUNK_TEST_TOOLS"; exit; fi
case "$0" in
  */goimports) step=fmt ;;
  */golangci-lint) step=lint ;;
  *) step="$1" ;;
esac
printf '%s\n' "$step" >> "$BUNK_TEST_LOG"
if [ "$step" = test ] && [ "$BUNK_TEST_FAIL" = 1 ]; then exit 1; fi
if [ "$step" = build ]; then
  while [ "$#" -gt 0 ]; do
    if [ "$1" = -o ]; then shift; mkdir -p bin; touch "$1"; break; fi
    shift
  done
fi
`
			for _, tool := range []string{"go", "goimports", "golangci-lint"} {
				if err := os.WriteFile(filepath.Join(dir, tool), []byte(script), 0o700); err != nil {
					t.Fatal(err)
				}
			}
			cmd := exec.Command("make", "--no-print-directory", "-j8", "-f", makefile, "check", "MONOVA=", "HEADLESS=")
			cmd.Dir = dir
			cmd.Env = append(os.Environ(), "PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"), "BUNK_TEST_TOOLS="+dir, "BUNK_TEST_LOG="+logPath)
			if fail {
				cmd.Env = append(cmd.Env, "BUNK_TEST_FAIL=1")
			}
			output, err := cmd.CombinedOutput()
			if (err != nil) != fail {
				t.Fatalf("make: %v\n%s", err, output)
			}
			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatal(err)
			}
			steps := strings.Fields(string(data))
			want := "fmt vet test build lint"
			if fail {
				want = "fmt vet test"
			}
			if strings.Join(steps, " ") != want {
				t.Fatalf("steps = %v, want %s", steps, want)
			}
		})
	}
}
