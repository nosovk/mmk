package mattermost

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/config"
)

type fakeValidator struct {
	user      *User
	teams     []Team
	userErr   error
	teamsErr  error
	teamCalls int
}

func (f *fakeValidator) CurrentUser(context.Context) (*User, error) { return f.user, f.userErr }
func (f *fakeValidator) TeamsForUser(_ context.Context, userID string) ([]Team, error) {
	f.teamCalls++
	if f.user != nil && userID != f.user.ID {
		return nil, fmt.Errorf("unexpected user ID %q", userID)
	}
	return f.teams, f.teamsErr
}

type fakeSecrets struct {
	values    map[string]string
	setErr    error
	deleteErr error
	sets      []string
	deletes   []string
}

func (f *fakeSecrets) Get(_ context.Context, serverID string) (string, error) {
	value, ok := f.values[serverID]
	if !ok {
		return "", ErrSecretNotFound
	}
	return value, nil
}

func (f *fakeSecrets) Set(_ context.Context, serverID, token string) error {
	f.sets = append(f.sets, serverID+"="+token)
	if f.setErr != nil {
		return f.setErr
	}
	if f.values == nil {
		f.values = map[string]string{}
	}
	f.values[serverID] = token
	return nil
}

func (f *fakeSecrets) Delete(_ context.Context, serverID string) error {
	f.deletes = append(f.deletes, serverID)
	if f.deleteErr != nil {
		return f.deleteErr
	}
	delete(f.values, serverID)
	return nil
}

func TestAddServerTransactionValidatesBeforeLocking(t *testing.T) {
	const token = "pat-super-secret"
	tx := &fakeRegistryTransaction{}
	secrets := &fakeSecrets{}
	validator := &fakeValidator{userErr: fmt.Errorf("callback reflected %s", token)}

	_, err := AddServerTransaction(t.Context(), AddServerInput{URL: "https://chat.example/api/v4", Token: token}, func(root, gotToken string) (ServerValidator, error) {
		if root != "https://chat.example" || gotToken != token {
			t.Fatalf("factory args = %q, %q", root, gotToken)
		}
		return validator, nil
	}, secrets, tx)
	if err == nil || strings.Contains(err.Error(), token) || strings.Contains(fmt.Sprintf("%#v", err), token) {
		t.Fatalf("error = %#v", err)
	}
	if tx.lockCalls != 0 || tx.loadCalls != 0 || tx.saveCalls != 0 || len(secrets.sets) != 0 || validator.teamCalls != 0 {
		t.Fatalf("validation failure touched state: tx=%#v sets=%v team calls=%d", tx, secrets.sets, validator.teamCalls)
	}
}

func TestAddServerTransactionPersistsValidatedServerAndToken(t *testing.T) {
	tx := &fakeRegistryTransaction{registry: config.NewServerRegistry()}
	secrets := &fakeSecrets{}
	eventStore := eventSecrets{fakeSecrets: secrets, tx: tx}
	validator := &fakeValidator{user: &User{ID: "user-1", Username: "alice"}, teams: []Team{{ID: "team-1"}}}

	server, err := AddServerTransaction(t.Context(), AddServerInput{URL: "https://chat.example/mattermost/api/v4/", Token: "new-token", DisplayName: " Engineering "}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, eventStore, tx)
	if err != nil {
		t.Fatal(err)
	}
	if server.URL != "https://chat.example/mattermost" || server.DisplayName != "Engineering" || server.UserID != "user-1" || server.Username != "alice" {
		t.Fatalf("server = %#v", server)
	}
	if len(tx.registry.Servers) != 1 || tx.registry.Servers[0] != server || secrets.values[server.ID] != "new-token" || validator.teamCalls != 1 {
		t.Fatalf("registry=%#v secrets=%#v team calls=%d", tx.registry, secrets.values, validator.teamCalls)
	}
	if tx.events != "lock,load,set,save,unlock" {
		t.Fatalf("transaction sequence = %q", tx.events)
	}
}

func TestAddServerTransactionSaveFailureRestoresPreviousCredential(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example")
	id := ServerID(root)
	saveErr := errors.New("save failed")
	tx := &fakeRegistryTransaction{
		registry: config.ServerRegistry{Version: config.ServerRegistryVersion, Servers: []config.MattermostServer{{ID: id, URL: root, DisplayName: "Old", UserID: "old-user"}}},
		saveErr:  saveErr,
	}
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	tx.secrets = secrets
	validator := &fakeValidator{user: &User{ID: "new-user"}}

	_, err := AddServerTransaction(t.Context(), AddServerInput{URL: root, Token: "new-token", DisplayName: "New"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, secrets, tx)
	if !errors.Is(err, saveErr) || strings.Contains(err.Error(), "new-token") || strings.Contains(err.Error(), "old-token") {
		t.Fatalf("error = %v", err)
	}
	if secrets.values[id] != "old-token" || len(tx.registry.Servers) != 1 || tx.registry.Servers[0].DisplayName != "Old" {
		t.Fatalf("rollback failed: secrets=%#v registry=%#v", secrets.values, tx.registry)
	}
}

func TestAddServerTransactionDoesNotClobberConcurrentCredentialOnRollback(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example")
	id := ServerID(root)
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	tx := &fakeRegistryTransaction{
		registry: config.ServerRegistry{Version: config.ServerRegistryVersion, Servers: []config.MattermostServer{{ID: id, URL: root, UserID: "old-user"}}},
		secrets:  secrets,
		saveHook: func(config.ServerRegistry) error {
			secrets.values[id] = "other-writer-token"
			return errors.New("save failed")
		},
	}
	validator := &fakeValidator{user: &User{ID: "new-user"}}

	_, err := AddServerTransaction(t.Context(), AddServerInput{URL: root, Token: "our-token"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, secrets, tx)
	if !errors.Is(err, ErrConcurrentCredentialChange) || strings.Contains(err.Error(), "our-token") || strings.Contains(err.Error(), "other-writer-token") {
		t.Fatalf("error = %v", err)
	}
	if secrets.values[id] != "other-writer-token" {
		t.Fatalf("concurrent credential was clobbered: %#v", secrets.values)
	}
}
