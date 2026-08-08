package config

import (
	"reflect"
	"strings"
	"testing"

	toml "github.com/pelletier/go-toml/v2"
)

func TestMattermostServersRoundTripWithExplicitTOMLFields(t *testing.T) {
	cfg := Default()
	cfg.Servers = []MattermostServer{{
		ID:          "chat-example-com-deadbeef",
		URL:         "https://chat.example.com/mattermost",
		DisplayName: "Engineering Chat",
		UserID:      "user-1",
		Username:    "alice",
	}}

	data, err := toml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	text := string(data)
	for _, want := range []string{
		"[[mattermost_servers]]",
		`id = 'chat-example-com-deadbeef'`,
		`url = 'https://chat.example.com/mattermost'`,
		`display_name = 'Engineering Chat'`,
		`user_id = 'user-1'`,
		`username = 'alice'`,
	} {
		if !strings.Contains(text, want) {
			t.Errorf("TOML missing %q:\n%s", want, text)
		}
	}
	if strings.Contains(strings.ToLower(text), "token") || strings.Contains(text, "PAT") {
		t.Fatalf("serialized config contains a secret field:\n%s", text)
	}

	var decoded Config
	if err := toml.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if len(decoded.Servers) != 1 || decoded.Servers[0] != cfg.Servers[0] {
		t.Fatalf("round trip = %#v", decoded.Servers)
	}
}

func TestMattermostServerHasNoSerializableSecretField(t *testing.T) {
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
