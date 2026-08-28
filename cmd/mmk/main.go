package main

import (
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/debuglog"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/styles"
	"golang.design/x/clipboard"
)

// Build-time version info, injected via -ldflags by GoReleaser.
var (
	version = "dev"
	commit  = "none"
	date    = "unknown"
)

type appIdentity struct {
	executable      string
	configDirectory string
	displayName     string
}

func applicationIdentity() appIdentity {
	return appIdentity{
		executable:      "mmk",
		configDirectory: "mmk",
		displayName:     "mmk",
	}
}

func main() {
	debugFile, err := debuglog.Init()
	if err != nil {
		fmt.Fprintf(os.Stderr, "mmk: could not open debug log: %v\n", err)
	} else if debugFile != nil {
		defer debugFile.Close()
		debuglog.General("=== mmk debug session started ===")
	}

	command, err := parseTopLevelCommand(os.Args)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
	switch command {
	case commandAddServer:
		err = addServer()
	case commandVersion:
		fmt.Print(versionText(version, commit, date))
		return
	case commandHelp:
		printHelp()
		return
	case commandRun:
		err = run()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}
}

func printHelp() {
	fmt.Print(helpText(version))
}

func helpText(version string) string {
	return fmt.Sprintf(`mmk %s -- a Mattermost TUI

Usage:
  mmk               Launch the TUI
  mmk --add-server  Add or update a Mattermost server (interactive)
  mmk --version     Print version and exit
  mmk --help        Show this help

Config:  ~/.config/mmk/config.toml
Servers: ~/.config/mmk/servers.toml
Data:    ~/.local/share/mmk/

Docs:    https://github.com/nosovk/mmk
`, version)
}

func versionText(version, commit, date string) string {
	return fmt.Sprintf("mmk %s (commit %s, built %s)\n", version, commit, date)
}

func run() error {
	configDir := xdgConfig()
	dataDir := xdgData()

	registry, err := config.LoadServerRegistry(filepath.Join(configDir, "servers.toml"))
	if err != nil {
		return fmt.Errorf("loading Mattermost server registry: %w", err)
	}
	if err := requireMattermostServers(len(registry.Servers)); err != nil {
		return err
	}

	cfg, err := config.Load(filepath.Join(configDir, "config.toml"))
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}
	styles.LoadCustomThemes(filepath.Join(configDir, "themes"))
	styles.Apply(cfg.Appearance.Theme, cfg.Theme)

	clipboardOK, clipboardReader := initializeClipboard()

	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return fmt.Errorf("creating data dir: %w", err)
	}
	db, err := cache.New(filepath.Join(dataDir, "cache.db"))
	if err != nil {
		return fmt.Errorf("opening cache: %w", err)
	}
	defer db.Close()

	return runMattermost(registry, cfg, db, clipboardOK, clipboardReader)
}

func requireMattermostServers(count int) error {
	if count == 0 {
		return errors.New("no Mattermost servers configured; run mmk --add-server first")
	}
	return nil
}

func initializeClipboard() (bool, func(clipboard.Format) []byte) {
	if ui.IsWayland() {
		if ui.HasWlPaste() {
			return true, ui.WaylandClipboardReader()
		}
		log.Printf("Warning: WAYLAND_DISPLAY set but wl-paste not on PATH; install wl-clipboard for paste-to-upload. Ctrl+V image paste disabled.")
		return false, nil
	}
	if err := clipboard.Init(); err != nil {
		log.Printf("Warning: clipboard init failed (%v); Ctrl+V image paste disabled", err)
		return false, nil
	}
	return true, nil
}

func xdgConfig() string {
	identity := applicationIdentity()
	if dir := os.Getenv("XDG_CONFIG_HOME"); dir != "" {
		return filepath.Join(dir, identity.configDirectory)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".config", identity.configDirectory)
}

func xdgData() string {
	identity := applicationIdentity()
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, identity.configDirectory)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".local", "share", identity.configDirectory)
}

func init() {
	if os.Getenv("MMK_DEBUG") == "" {
		log.SetOutput(io.Discard)
	}
}
