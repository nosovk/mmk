package main

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"charm.land/huh/v2"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/lockfile"
	"github.com/nosovk/mmk/internal/mattermost"
)

type topLevelCommand string

const (
	commandRun       topLevelCommand = "run"
	commandAddServer topLevelCommand = "add-server"
	commandVersion   topLevelCommand = "version"
	commandHelp      topLevelCommand = "help"
)

func parseTopLevelCommand(args []string) (topLevelCommand, error) {
	if len(args) < 2 {
		return commandRun, nil
	}
	if len(args) != 2 {
		if args[1] == "--add-server" {
			return commandRun, errors.New("--add-server does not accept arguments; enter the PAT in the masked prompt")
		}
		return commandRun, fmt.Errorf("unexpected arguments; run %s --help", args[0])
	}
	switch args[1] {
	case "--add-server":
		return commandAddServer, nil
	case "--version":
		return commandVersion, nil
	case "--help":
		return commandHelp, nil
	default:
		return commandRun, fmt.Errorf("unknown command %q; run %s --help", args[1], args[0])
	}
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

	configDir := xdgConfig()
	registryPath := filepath.Join(configDir, "servers.toml")
	lockPath := filepath.Join(configDir, "servers.lock")
	server, err := mattermost.AddServerTransaction(context.Background(), mattermost.AddServerInput{
		URL:         strings.TrimSpace(values.URL),
		Token:       values.Token,
		DisplayName: values.DisplayName,
	}, mattermost.NewValidator, mattermost.NewOSSecretStore(), fileServerRegistryTransaction{registryPath: registryPath, lockPath: lockPath})
	if err != nil {
		return err
	}

	name := server.DisplayName
	if name == "" {
		name = server.URL
	}
	fmt.Printf("Added Mattermost server %s for %s.\n", name, server.Username)
	return nil
}

type fileServerRegistryTransaction struct {
	registryPath string
	lockPath     string
}

func (t fileServerRegistryTransaction) Lock(ctx context.Context) (func() error, error) {
	lock, err := lockfile.Acquire(ctx, t.lockPath)
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}

func (t fileServerRegistryTransaction) Load(context.Context) (config.ServerRegistry, error) {
	return config.LoadServerRegistry(t.registryPath)
}

func (t fileServerRegistryTransaction) Save(_ context.Context, registry config.ServerRegistry) error {
	return config.SaveServerRegistry(t.registryPath, registry)
}
