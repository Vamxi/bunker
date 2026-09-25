// desktop.go - the app icon and desktop integration.
//
// The icon ships inside the binary. At startup it is unpacked into the
// user's cache and added to GTK's icon search path, so windows have their
// icon without any install step. `bunker install-desktop` additionally
// installs a .desktop entry and the icons under ~/.local/share, which is
// what GNOME's app grid and dock use (they match the window's app id,
// dev.bunker.Bunker, to the entry).
package main

import (
	"bytes"
	"embed"
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

//go:embed assets/icon/bunker-16.png assets/icon/bunker-32.png assets/icon/bunker-48.png assets/icon/bunker-64.png assets/icon/bunker-128.png assets/icon/bunker-256.png assets/icon/bunker-512.png
var iconFS embed.FS

var iconSizes = []int{16, 32, 48, 64, 128, 256, 512}

// writeIcons lays the embedded icons out as a hicolor theme under root
// (…/hicolor/<n>x<n>/apps/<app id>.png). Unchanged files are left alone.
func writeIcons(root string) error {
	for _, n := range iconSizes {
		data, err := iconFS.ReadFile(fmt.Sprintf("assets/icon/bunker-%d.png", n))
		if err != nil {
			return fmt.Errorf("embedded %dpx icon: %w", n, err)
		}
		dir := filepath.Join(root, "hicolor", fmt.Sprintf("%dx%d", n, n), "apps")
		path := filepath.Join(dir, guiAppID+".png")
		if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, data) {
			continue
		}
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("icon directory: %w", err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			return fmt.Errorf("write icon: %w", err)
		}
	}
	return nil
}

// iconCacheDir is where startup unpacks the icon theme.
func iconCacheDir() string {
	base := os.Getenv("XDG_CACHE_HOME")
	if base == "" {
		home, _ := os.UserHomeDir()
		base = filepath.Join(home, ".cache")
	}
	return filepath.Join(base, "bunker", "icons")
}

func dataHome() string {
	if xdg := os.Getenv("XDG_DATA_HOME"); xdg != "" {
		return xdg
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share")
}

// desktopEntry is the launcher for this binary.
func desktopEntry(exe string) string {
	return fmt.Sprintf(`[Desktop Entry]
Type=Application
Name=bunker
GenericName=Terminal
Comment=A fast GTK4 terminal with bunk built in
Exec=%s
Icon=%s
Terminal=false
Categories=System;TerminalEmulator;
Keywords=terminal;shell;prompt;command;commandline;bunk;
StartupNotify=true
StartupWMClass=%s
`, exe, guiAppID, guiAppID)
}

var installDesktopCmd = &cobra.Command{
	Use:   "install-desktop",
	Short: "Add bunker to the desktop's app grid and dock (~/.local/share)",
	RunE: func(cmd *cobra.Command, args []string) error {
		exe, err := os.Executable()
		if err != nil {
			return fmt.Errorf("find the bunker binary: %w", err)
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return fmt.Errorf("find the bunker binary: %w", err)
		}
		share := dataHome()
		if err := writeIcons(filepath.Join(share, "icons")); err != nil {
			return fmt.Errorf("install icons: %w", err)
		}
		apps := filepath.Join(share, "applications")
		if err := os.MkdirAll(apps, 0o755); err != nil {
			return fmt.Errorf("applications directory: %w", err)
		}
		entry := filepath.Join(apps, guiAppID+".desktop")
		if err := os.WriteFile(entry, []byte(desktopEntry(exe)), 0o644); err != nil {
			return fmt.Errorf("write desktop entry: %w", err)
		}
		fmt.Printf("Installed %s\n  runs %s\n", entry, exe)
		return nil
	},
}

var themesCmd = &cobra.Command{
	Use:   "themes",
	Short: "List the built-in themes (use one with theme = \"name\")",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Println(systemThemeName + "  (default: follows the desktop)")
		for _, name := range themeNames() {
			if name != "terminal" {
				fmt.Println(name)
			}
		}
	},
}

func init() {
	rootCmd.AddCommand(installDesktopCmd)
	rootCmd.AddCommand(themesCmd)
}
