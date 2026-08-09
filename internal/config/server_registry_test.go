package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestServerRegistryLoadMissingAndEmptyFiles(t *testing.T) {
	dir := t.TempDir()
	missing := filepath.Join(dir, "missing.toml")

	registry, err := LoadServerRegistry(missing)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Version != ServerRegistryVersion || len(registry.Servers) != 0 {
		t.Fatalf("missing registry = %#v", registry)
	}

	empty := filepath.Join(dir, "empty.toml")
	if err := os.WriteFile(empty, nil, 0600); err != nil {
		t.Fatal(err)
	}
	registry, err = LoadServerRegistry(empty)
	if err != nil {
		t.Fatal(err)
	}
	if registry.Version != ServerRegistryVersion || len(registry.Servers) != 0 {
		t.Fatalf("empty registry = %#v", registry)
	}
}

func TestServerRegistryRejectsInvalidDocuments(t *testing.T) {
	tests := []struct {
		name string
		toml string
		want string
	}{
		{name: "missing version", toml: "[[servers]]\nid='one'\nurl='https://one.example'\nuser_id='user-1'\n", want: "version"},
		{name: "unsupported version", toml: "version=2\n", want: "unsupported"},
		{name: "malformed TOML", toml: "version=[\n", want: "parse"},
		{name: "unknown root field", toml: "version=1\nowner='other'\n", want: "owner"},
		{name: "unknown server field", toml: "version=1\n[[servers]]\nid='one'\nurl='https://one.example'\nuser_id='user-1'\ntoken='secret'\n", want: "token"},
		{name: "empty ID", toml: "version=1\n[[servers]]\nid=' '\nurl='https://one.example'\nuser_id='user-1'\n", want: "ID"},
		{name: "duplicate ID", toml: "version=1\n[[servers]]\nid='one'\nurl='https://one.example'\nuser_id='user-1'\n[[servers]]\nid='one'\nurl='https://two.example'\nuser_id='user-2'\n", want: "duplicate"},
		{name: "empty URL", toml: "version=1\n[[servers]]\nid='one'\nurl=' '\nuser_id='user-1'\n", want: "URL"},
		{name: "empty user ID", toml: "version=1\n[[servers]]\nid='one'\nurl='https://one.example'\nuser_id=' '\n", want: "user ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "servers.toml")
			if err := os.WriteFile(path, []byte(tt.toml), 0600); err != nil {
				t.Fatal(err)
			}
			_, err := LoadServerRegistry(path)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), strings.ToLower(tt.want)) {
				t.Fatalf("error = %v, want text %q", err, tt.want)
			}
		})
	}
}

func TestServerRegistryRoundTripPreservesOrderAndContainsNoSecretField(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.toml")
	want := ServerRegistry{
		Version: ServerRegistryVersion,
		Servers: []MattermostServer{
			{ID: "one", URL: "https://one.example", DisplayName: "One", UserID: "user-1", Username: "alice"},
			{ID: "two", URL: "https://two.example", DisplayName: "Two", UserID: "user-2", Username: "bob"},
		},
	}
	if err := SaveServerRegistry(path, want); err != nil {
		t.Fatal(err)
	}
	got, err := LoadServerRegistry(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round trip = %#v, want %#v", got, want)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, "version = 1") || strings.Count(text, "[[servers]]") != 2 {
		t.Fatalf("unexpected schema:\n%s", text)
	}
	if strings.Contains(strings.ToLower(text), "token") || strings.Contains(strings.ToLower(text), "secret") || strings.Contains(text, "PAT") {
		t.Fatalf("registry contains a secret field:\n%s", text)
	}

	typeOfServer := reflect.TypeOf(MattermostServer{})
	for i := 0; i < typeOfServer.NumField(); i++ {
		field := typeOfServer.Field(i)
		nameAndTag := strings.ToLower(field.Name + " " + field.Tag.Get("toml"))
		if strings.Contains(nameAndTag, "token") || strings.Contains(nameAndTag, "secret") || strings.Contains(nameAndTag, "pat") {
			t.Fatalf("MattermostServer exposes secret-like field %s", field.Name)
		}
		if field.Tag.Get("toml") == "" {
			t.Fatalf("MattermostServer field %s has no explicit TOML tag", field.Name)
		}
	}
}

func TestServerRegistrySaveValidatesBeforeReplacingFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "servers.toml")
	original := []byte("version = 1\n")
	if err := os.WriteFile(path, original, 0600); err != nil {
		t.Fatal(err)
	}
	err := SaveServerRegistry(path, ServerRegistry{Version: ServerRegistryVersion, Servers: []MattermostServer{{ID: "one", URL: "", UserID: "user-1"}}})
	if err == nil {
		t.Fatal("invalid registry was saved")
	}
	got, readErr := os.ReadFile(path)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(got) != string(original) {
		t.Fatalf("registry changed after validation failure: %q", got)
	}
}

func TestServerRegistrySaveAtomicallyReplacesAndSecuresFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "servers.toml")
	if err := os.WriteFile(path, []byte("version = 1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	openBefore, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer openBefore.Close()

	registry := ServerRegistry{Version: ServerRegistryVersion, Servers: []MattermostServer{{ID: "one", URL: "https://one.example", UserID: "user-1"}}}
	if err := SaveServerRegistry(path, registry); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(before, after) {
		t.Fatal("save rewrote the existing inode instead of atomically replacing it")
	}
	if after.Mode().Perm() != 0600 {
		t.Fatalf("mode = %o, want 600", after.Mode().Perm())
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "servers.toml" {
		t.Fatalf("temporary files remain: %#v", entries)
	}
}
