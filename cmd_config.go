// cmd_config.go - "bunker config" subcommand tree.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/spf13/cobra"
)

func init() {
	configCmd.AddCommand(configInitCmd)
	rootCmd.AddCommand(configCmd)
}

var configCmd = &cobra.Command{
	Use:   "config",
	Short: "Manage bunker configuration",
}

var configInitCmd = &cobra.Command{
	Use:   "init",
	Short: "Write a default config.toml to " + "~/.config/bunker/",
	Long: `Creates ~/.config/bunker/config.toml with all options documented.
Exits with an error if the file already exists (use --force to overwrite).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		force, _ := cmd.Flags().GetBool("force")
		return writeDefaultConfig(force)
	},
}

func init() {
	configInitCmd.Flags().Bool("force", false, "overwrite an existing config file")
}

// writeDefaultConfig writes DefaultConfigTOML to the default config path.
func writeDefaultConfig(force bool) error {
	path := DefaultConfigPath()
	if _, err := os.Stat(path); err == nil && !force {
		return fmt.Errorf("config file already exists: %s\nUse --force to overwrite", path)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(DefaultConfigTOML()), 0o600); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("Written: %s\n", path)
	return nil
}

// setting is one config value that "bunker config" can read and write.
type setting struct {
	name         string // dotted: "tabs.position", "keys.new_tab"
	section, key string
	// literal validates a value typed on the command line and returns it
	// as a TOML literal.
	literal func(value string) (string, error)
	get     func(cfg Config) string
}

func stringLiteral(check func(string) error) func(string) (string, error) {
	return func(v string) (string, error) {
		if check != nil {
			if err := check(v); err != nil {
				return "", err
			}
		}
		return tomlString(v), nil
	}
}

func oneOf(values []string) func(string) error {
	return func(v string) error {
		if !slices.Contains(values, v) {
			return fmt.Errorf("must be one of %s", strings.Join(values, ", "))
		}
		return nil
	}
}

func intLiteral(lo, hi int) func(string) (string, error) {
	return func(v string) (string, error) {
		n, err := strconv.Atoi(v)
		if err != nil || n < lo || n > hi {
			return "", fmt.Errorf("must be a whole number from %d to %d", lo, hi)
		}
		return strconv.Itoa(n), nil
	}
}

func floatLiteral(lo float64) func(string) (string, error) {
	return func(v string) (string, error) {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < lo {
			return "", fmt.Errorf("must be a number of at least %g", lo)
		}
		return strconv.FormatFloat(f, 'f', -1, 64), nil
	}
}

func boolLiteral(v string) (string, error) {
	b, err := strconv.ParseBool(v)
	if err != nil {
		return "", fmt.Errorf("must be true or false")
	}
	return strconv.FormatBool(b), nil
}

func hexLiteral(v string) (string, error) {
	if v != "" && hexColor(v) == tcell.ColorDefault {
		return "", fmt.Errorf("must be a colour like #1e1e2e, or empty for the theme's")
	}
	return tomlString(v), nil
}

// shortcutLiteral validates a shortcut for action; bunk's pane actions also
// need a key bunk's own key reader knows.
func shortcutLiteral(pane bool) func(string) (string, error) {
	return func(v string) (string, error) {
		c, err := canonicalKey(v)
		if err != nil {
			return "", err
		}
		if pane {
			if _, err := parseKey(c); err != nil {
				return "", fmt.Errorf("pane actions take letters, f1-f12, arrows, and navigation keys: %w", err)
			}
		}
		return tomlString(c), nil
	}
}

// settings lists every value "bunker config" handles, in config order.
func settings() []setting {
	themes := append([]string{systemThemeName}, themeNames()...)
	s := []setting{
		{"theme", "", "theme", stringLiteral(oneOf(themes)), func(c Config) string { return c.ThemeName }},
		{"font", "", "font", stringLiteral(nil), func(c Config) string { return c.Font }},
		{"window.padding", "window", "padding", intLiteral(0, maxPadding), func(c Config) string { return strconv.Itoa(c.Padding) }},
		{"tabs.position", "tabs", "position", stringLiteral(oneOf(tabPositions)), func(c Config) string { return c.Tabs.Position }},
		{"tabs.width", "tabs", "width", intLiteral(minTabsWidth, maxTabsWidth), func(c Config) string { return strconv.Itoa(c.Tabs.Width) }},
		{"tabs.autohide", "tabs", "autohide", boolLiteral, func(c Config) string { return strconv.FormatBool(c.Tabs.Autohide) }},
		{"tabs.collapsed", "tabs", "collapsed", boolLiteral, func(c Config) string { return strconv.FormatBool(c.Tabs.Collapsed) }},
		{"cursor.blink", "cursor", "blink", stringLiteral(oneOf(cursorBlinkModes)), func(c Config) string { return c.Cursor.Blink }},
		{"notify.desktop", "notify", "desktop", stringLiteral(oneOf(notifyDesktopModes)), func(c Config) string { return c.Notify.Desktop }},
		{"notify.command_seconds", "notify", "command_seconds", intLiteral(0, 86400), func(c Config) string { return strconv.Itoa(c.Notify.CommandSeconds) }},
		{"notify.bell", "notify", "bell", boolLiteral, func(c Config) string { return strconv.FormatBool(c.Notify.Bell) }},
		{"scrollback", "", "scrollback", intLiteral(100, 10_000_000), func(c Config) string { return strconv.Itoa(c.Scrollback) }},
		{"scrollback_mb", "", "scrollback_mb", floatLiteral(0), func(c Config) string { return strconv.Itoa(c.ScrollbackBytes >> 20) }},
		{"cell_aspect", "", "cell_aspect", floatLiteral(0), func(c Config) string { return strconv.FormatFloat(c.CellAspect, 'f', -1, 64) }},
		{"log_file", "", "log_file", stringLiteral(nil), func(c Config) string { return c.LogFile }},
		{"log_level", "", "log_level", stringLiteral(oneOf([]string{"trace", "debug", "info", "warn", "error"})), func(c Config) string { return c.LogLevel }},
	}
	for _, key := range []struct{ name, field string }{
		{"active_border", "ActiveBorder"}, {"inactive_border", "InactiveBorder"},
		{"scrollbar_thumb", "ScrollThumb"}, {"scrollbar_track", "ScrollTrack"},
	} {
		s = append(s, setting{"ui." + key.name, "ui", key.name, hexLiteral, func(c Config) string { return c.UIOverrides[key.name] }})
	}
	for _, w := range windowShortcuts {
		s = append(s, setting{"keys." + w.action, "keys", w.action, shortcutLiteral(false), func(c Config) string { return c.WindowKeys[w.action] }})
	}
	for _, e := range keybindingDefaults {
		s = append(s, setting{"keys." + e.action, "keys", e.action, shortcutLiteral(true), func(c Config) string { return e.field(&c.Keybindings).raw }})
	}
	return s
}

func findSetting(name string) (setting, error) {
	for _, s := range settings() {
		if s.name == name {
			return s, nil
		}
	}
	return setting{}, fmt.Errorf("unknown setting %q (bunker config list shows them all)", name)
}

var configListCmd = &cobra.Command{
	Use:   "list",
	Short: "Show every setting and its current value",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, _ []string) error {
		cfg, err := LoadConfig(flagConfig, "")
		if err != nil {
			return err
		}
		for _, s := range settings() {
			if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", s.name, tomlString(s.get(cfg))); err != nil {
				return fmt.Errorf("write: %w", err)
			}
		}
		return nil
	},
}

var configGetCmd = &cobra.Command{
	Use:   "get SETTING",
	Short: "Show one setting, e.g. bunker config get keys.new_tab",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := findSetting(args[0])
		if err != nil {
			return err
		}
		cfg, err := LoadConfig(flagConfig, "")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintln(cmd.OutOrStdout(), s.get(cfg)); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		return nil
	},
}

var configSetCmd = &cobra.Command{
	Use:   "set SETTING VALUE",
	Short: "Change one setting, e.g. bunker config set keys.new_tab f2",
	Long: `Changes one setting in the config file, keeping its comments. A running
bunker applies it at once. "bunker config list" shows every setting.`,
	Args: cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		s, err := findSetting(args[0])
		if err != nil {
			return err
		}
		value := strings.Trim(args[1], `"'`)
		lit, err := s.literal(value)
		if err != nil {
			return fmt.Errorf("%s: %w", s.name, err)
		}
		path := flagConfig
		if path == "" {
			path = DefaultConfigPath()
		}
		if _, err := writeConfigKey(path, s.section, s.key, lit); err != nil {
			return err
		}
		cfg, err := LoadConfig(path, "")
		if err != nil {
			return err
		}
		if _, err := fmt.Fprintf(cmd.OutOrStdout(), "%s = %s\n", s.name, tomlString(s.get(cfg))); err != nil {
			return fmt.Errorf("write: %w", err)
		}
		for _, p := range cfg.KeyProblems {
			cmd.PrintErrln("note: " + p)
		}
		return nil
	},
}

func init() {
	configCmd.AddCommand(configListCmd, configGetCmd, configSetCmd)
}
