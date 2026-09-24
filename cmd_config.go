// cmd_config.go - "bunker config" subcommand tree.
package main

import (
	"fmt"
	"os"
	"path/filepath"

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
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}
	if err := os.WriteFile(path, []byte(DefaultConfigTOML()), 0o640); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	fmt.Printf("Written: %s\n", path)
	return nil
}
