package main

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// The RPM ships exactly what install-desktop stages under bin/pkgroot, so
// nfpm.yaml must list every staged file, and nothing else from there.
func TestNFPMShipsWhatInstallDesktopStages(t *testing.T) {
	root := t.TempDir()
	share := filepath.Join(root, "usr", "share")
	installDesktopDataDir, installDesktopExec = share, "/usr/bin/bunker"
	t.Cleanup(func() { installDesktopDataDir, installDesktopExec = "", "" })
	if err := installDesktopCmd.RunE(installDesktopCmd, nil); err != nil {
		t.Fatal(err)
	}

	var staged []string
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(root, path)
			staged = append(staged, "bin/pkgroot/"+rel)
		}
		return err
	})
	if err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile("nfpm.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var shipped []string
	for _, line := range strings.Split(string(data), "\n") {
		if src, ok := strings.CutPrefix(strings.TrimSpace(line), "- src: "); ok && strings.HasPrefix(src, "bin/pkgroot/") {
			shipped = append(shipped, src)
		}
	}
	slices.Sort(staged)
	slices.Sort(shipped)
	if !slices.Equal(staged, shipped) {
		t.Fatalf("install-desktop stages\n  %s\nnfpm.yaml ships\n  %s", strings.Join(staged, "\n  "), strings.Join(shipped, "\n  "))
	}

	entry, err := os.ReadFile(filepath.Join(share, "applications", guiAppID+".desktop"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(entry), "\nExec=/usr/bin/bunker\n") {
		t.Fatalf("desktop entry does not run /usr/bin/bunker:\n%s", entry)
	}
}
