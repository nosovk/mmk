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
	"github.com/nosovk/mmk/internal/lockfile"
	"github.com/nosovk/mmk/internal/mattermost"
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
	server, err := mattermost.AddServerTransaction(context.Background(), mattermost.AddServerInput{
		URL:         strings.TrimSpace(values.URL),
		Token:       values.Token,
		DisplayName: values.DisplayName,
	}, mattermost.NewValidator, mattermost.NewOSSecretStore(), fileConfigTransaction{path: configPath})
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

type fileConfigTransaction struct {
	path string
}

func (t fileConfigTransaction) Lock(ctx context.Context) (func() error, error) {
	lock, err := lockfile.Acquire(ctx, t.path+".lock")
	if err != nil {
		return nil, err
	}
	return lock.Release, nil
}

func (t fileConfigTransaction) Load(context.Context) (config.Config, []byte, error) {
	document, err := os.ReadFile(t.path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return config.Config{}, nil, err
	}
	if errors.Is(err, os.ErrNotExist) {
		document = nil
	}
	cfg, err := config.LoadBytes(document)
	return cfg, document, err
}

func (t fileConfigTransaction) Save(_ context.Context, document []byte) error {
	if err := os.MkdirAll(filepath.Dir(t.path), 0755); err != nil {
		return err
	}
	configWriteMu.Lock()
	defer configWriteMu.Unlock()
	return writeConfigAtomic(t.path, document)
}
