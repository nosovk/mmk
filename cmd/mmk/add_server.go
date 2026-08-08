package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/mattermost"
	toml "github.com/pelletier/go-toml/v2"
)

type topLevelCommand string

const (
	commandNone      topLevelCommand = ""
	commandAddServer topLevelCommand = "add-server"
)

func parseTopLevelCommand(args []string) (topLevelCommand, error) {
	if len(args) < 2 || args[1] != "--add-server" {
		return commandNone, nil
	}
	if len(args) != 2 {
		return commandNone, errors.New("--add-server does not accept arguments; enter the PAT in the masked prompt")
	}
	return commandAddServer, nil
}

type addServerFormValues struct {
	URL         string
	Token       string
	DisplayName string
}

func addServerPATEchoMode() huh.EchoMode { return huh.EchoModePassword }

func newAddServerFields(values *addServerFormValues) []huh.Field {
	return []huh.Field{
		huh.NewInput().
			Title("Mattermost server URL").
			Description("Deployment root or a URL ending in /api/v4").
			Value(&values.URL),
		huh.NewInput().
			Title("Personal access token").
			EchoMode(addServerPATEchoMode()).
			Value(&values.Token),
		huh.NewInput().
			Title("Display name (optional)").
			Value(&values.DisplayName),
	}
}

func addServer() error {
	values := &addServerFormValues{}
	form := huh.NewForm(huh.NewGroup(newAddServerFields(values)...)).WithTheme(huh.ThemeFunc(huh.ThemeDracula))
	if err := form.Run(); err != nil {
		return errors.New("form cancelled")
	}

	configPath := filepath.Join(xdgConfig(), "config.toml")
	cfg, err := config.Load(configPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	if err := mattermost.AddServer(context.Background(), &cfg, mattermost.AddServerInput{
		URL:         strings.TrimSpace(values.URL),
		Token:       values.Token,
		DisplayName: values.DisplayName,
	}, mattermost.NewValidator, mattermost.NewOSSecretStore(), func(candidate config.Config) error {
		return saveMattermostConfig(configPath, candidate)
	}); err != nil {
		return err
	}

	root, _ := mattermost.CanonicalServerRoot(values.URL)
	serverID := mattermost.ServerID(root)
	server := cfg.Servers[len(cfg.Servers)-1]
	for i := range cfg.Servers {
		if cfg.Servers[i].ID == serverID {
			server = cfg.Servers[i]
			break
		}
	}
	name := server.DisplayName
	if name == "" {
		name = server.URL
	}
	fmt.Printf("Added Mattermost server %s for %s.\n", name, server.Username)
	return nil
}

func saveMattermostConfig(configPath string, cfg config.Config) error {
	data, err := toml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("encode config: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(configPath), 0755); err != nil {
		return err
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	return writeConfigAtomic(configPath, data)
}
