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

func TestSaveMattermostConfigUsesAtomicConfigPath(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	initial := "# preserve me\n[appearance]\ntheme = 'dracula'\nunknown = 'keep'\n"
	if err := os.WriteFile(path, []byte(initial), 0640); err != nil {
		t.Fatal(err)
	}
	tx := fileConfigTransaction{path: path}
	unlock, err := tx.Lock(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, document, err := tx.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	edited, err := config.EditMattermostServer(document, config.MattermostServer{ID: "server-id", URL: "https://chat.example.com", UserID: "user-1"})
	if err != nil {
		t.Fatal(err)
	}
	if err := tx.Save(t.Context(), edited); err != nil {
		t.Fatal(err)
	}
	if err := unlock(); err != nil {
		t.Fatal(err)
	}
	loaded, err := config.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Servers) != 1 || loaded.Servers[0].ID != "server-id" {
		t.Fatalf("servers = %#v", loaded.Servers)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), "# preserve me") || !strings.Contains(string(raw), "unknown = 'keep'") {
		t.Fatalf("document trivia was lost:\n%s", raw)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0640 {
		t.Fatalf("mode = %o", info.Mode().Perm())
	}
}
