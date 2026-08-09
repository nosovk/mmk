package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/nosovk/mmk/internal/config"
)

func TestParseTopLevelCommandRecognizesAddServerOnlyWithoutPATArgs(t *testing.T) {
	command, err := parseTopLevelCommand([]string{"mmk", "--add-server"})
	if err != nil || command != commandAddServer {
		t.Fatalf("command = %q, err = %v", command, err)
	}
	if _, err := parseTopLevelCommand([]string{"mmk", "--add-server", "pat-must-not-be-argv"}); err == nil {
		t.Fatal("add-server accepted a PAT-shaped positional argument")
	}
}

func TestHelpExposesAddServer(t *testing.T) {
	help := helpText("test")
	if !strings.Contains(help, "mmk --add-server") || !strings.Contains(help, "Mattermost") {
		t.Fatalf("help missing add-server:\n%s", help)
	}
}

func TestAddServerPATFieldIsMasked(t *testing.T) {
	input := &addServerFormValues{}
	fields := newAddServerFields(input)
	if len(fields) != 3 {
		t.Fatalf("field count = %d", len(fields))
	}
	_, ok := fields[1].(*huh.Input)
	if !ok {
		t.Fatalf("PAT field type = %T", fields[1])
	}
	if addServerPATEchoMode() != huh.EchoModePassword {
		t.Fatal("PAT input is not masked")
	}
}

func TestSaveMattermostRegistryUsesDedicatedPathsAndNeverTouchesConfig(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	configPath := filepath.Join(xdgConfig(), "config.toml")
	serversPath := filepath.Join(xdgConfig(), "servers.toml")
	lockPath := filepath.Join(xdgConfig(), "servers.lock")
	initial := "[appearance]\ntheme = 'dracula'\n"
	if err := os.MkdirAll(xdgConfig(), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, []byte(initial), 0644); err != nil {
		t.Fatal(err)
	}
	tx := fileServerRegistryTransaction{registryPath: serversPath, lockPath: lockPath}
	unlock, err := tx.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	registry, err := tx.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	registry.Servers = append(registry.Servers, config.MattermostServer{ID: "server-id", URL: "https://chat.example.com", UserID: "user-1"})
	if err := tx.Save(t.Context(), registry); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.LoadServerRegistry(serversPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != 1 || loaded.Servers[0].ID != "server-id" {
		t.Fatalf("servers = %#v", loaded.Servers)
	}
	raw, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != initial {
		t.Fatalf("config.toml changed:\n%s", raw)
	}
	info, err := os.Stat(serversPath)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("servers.lock missing: %v", err)
	}
}
