package main

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"charm.land/huh/v2"
	"github.com/nosovk/mmk/internal/config"
)

func TestParseTopLevelCommandRecognizesSupportedCommands(t *testing.T) {
	tests := []struct {
		args []string
		want topLevelCommand
	}{
		{[]string{"mmk"}, commandRun},
		{[]string{"mmk", "--add-server"}, commandAddServer},
		{[]string{"mmk", "--version"}, commandVersion},
		{[]string{"mmk", "--help"}, commandHelp},
	}
	for _, test := range tests {
		command, err := parseTopLevelCommand(test.args)
		if err != nil || command != test.want {
			t.Errorf("parseTopLevelCommand(%q) = %q, %v; want %q", test.args, command, err, test.want)
		}
	}

	if _, err := parseTopLevelCommand([]string{"mmk", "--add-server", "pat-must-not-be-argv"}); err == nil {
		t.Fatal("add-server accepted a PAT-shaped positional argument")
	}
	if _, err := parseTopLevelCommand([]string{"mmk", "--add-workspace"}); err == nil {
		t.Fatal("removed Slack command was accepted")
	}
	for _, alias := range []string{"-v", "version", "-h", "help"} {
		if _, err := parseTopLevelCommand([]string{"mmk", alias}); err == nil {
			t.Errorf("unsupported alias %q was accepted", alias)
		}
	}
}

func TestHelpIsMattermostOnly(t *testing.T) {
	help := helpText("test")
	if !strings.Contains(help, "mmk --add-server") || !strings.Contains(help, "Mattermost") {
		t.Fatalf("help missing add-server:\n%s", help)
	}
	for _, forbidden := range []string{"Slack", "workspace", "--remint", "--dump-", "--diagnostic"} {
		if strings.Contains(help, forbidden) {
			t.Fatalf("help contains removed Slack surface %q:\n%s", forbidden, help)
		}
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
	if runtime.GOOS != "windows" && info.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", info.Mode().Perm())
	}
	if _, err := os.Stat(lockPath); err != nil {
		t.Fatalf("servers.lock missing: %v", err)
	}
}
