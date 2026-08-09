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

func TestAddServerValidatesBeforePersisting(t *testing.T) {
	const token = "pat-super-secret"
	cfg := config.Default()
	secrets := &fakeSecrets{}
	validator := &fakeValidator{userErr: fmt.Errorf("callback reflected %s", token)}
	saves := 0

	err := AddServer(context.Background(), &cfg, AddServerInput{
		URL: "https://chat.example.com/api/v4", Token: token,
	}, func(root, gotToken string) (ServerValidator, error) {
		if root != "https://chat.example.com" || gotToken != token {
			t.Fatalf("factory args = %q, %q", root, gotToken)
		}
		return validator, nil
	}, secrets, func(config.Config) error {
		saves++
		return nil
	})

	if err == nil || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
	if inspected := fmt.Sprintf("%#v", err); strings.Contains(inspected, token) {
		t.Fatalf("inspected error leaks PAT: %s", inspected)
	}
	if len(cfg.Servers) != 0 || len(secrets.sets) != 0 || saves != 0 || validator.teamCalls != 0 {
		t.Fatalf("validation failure wrote state: cfg=%v sets=%v saves=%d team calls=%d", cfg.Servers, secrets.sets, saves, validator.teamCalls)
	}
}

func TestAddServerPersistsValidatedServerAndToken(t *testing.T) {
	cfg := config.Default()
	secrets := &fakeSecrets{}
	validator := &fakeValidator{user: &User{ID: "user-1", Username: "alice"}, teams: []Team{{ID: "team-1"}}}
	var saved config.Config

	err := AddServer(context.Background(), &cfg, AddServerInput{
		URL: "https://chat.example.com/mattermost/api/v4/", Token: "new-token", DisplayName: "Engineering",
	}, func(string, string) (ServerValidator, error) { return validator, nil }, secrets, func(candidate config.Config) error {
		saved = candidate
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 1 || cfg.Servers[0] != saved.Servers[0] {
		t.Fatalf("saved/caller config mismatch: %#v %#v", cfg.Servers, saved.Servers)
	}
	server := cfg.Servers[0]
	if server.URL != "https://chat.example.com/mattermost" || server.DisplayName != "Engineering" || server.UserID != "user-1" || server.Username != "alice" {
		t.Fatalf("server = %#v", server)
	}
	if secrets.values[server.ID] != "new-token" || validator.teamCalls != 1 {
		t.Fatalf("secret/teams not persisted: %#v calls=%d", secrets.values, validator.teamCalls)
	}
}

func TestAddServerSetFailureLeavesConfigUnchanged(t *testing.T) {
	const token = "pat-secret"
	sentinel := errors.New("set failed")
	cfg := config.Default()
	secrets := &fakeSecrets{setErr: fmt.Errorf("%w: %s", sentinel, token)}
	validator := &fakeValidator{user: &User{ID: "user-1"}}
	saves := 0

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: "https://chat.example.com", Token: token},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(config.Config) error { saves++; return nil })
	if !errors.Is(err, sentinel) || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
	if len(cfg.Servers) != 0 || saves != 0 {
		t.Fatalf("state changed: cfg=%v saves=%d", cfg.Servers, saves)
	}
}

func TestAddServerSaveFailureDeletesNewSecret(t *testing.T) {
	const token = "pat-secret"
	saveErr := errors.New("save failed")
	cfg := config.Default()
	secrets := &fakeSecrets{}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: "https://chat.example.com", Token: token},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(config.Config) error { return fmt.Errorf("%w: %s", saveErr, token) })
	if !errors.Is(err, saveErr) || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
	if len(cfg.Servers) != 0 || len(secrets.deletes) != 1 || len(secrets.values) != 0 {
		t.Fatalf("rollback = cfg=%v deletes=%v values=%v", cfg.Servers, secrets.deletes, secrets.values)
	}
}

func TestAddServerSaveFailureRestoresUpdatedSecretAndOrder(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example.com")
	id := ServerID(root)
	original := []config.MattermostServer{
		{ID: "first", URL: "https://first.example.com", DisplayName: "First"},
		{ID: id, URL: root, DisplayName: "Old", UserID: "old-user"},
		{ID: "last", URL: "https://last.example.com", DisplayName: "Last"},
	}
	cfg := config.Default()
	cfg.Servers = append([]config.MattermostServer(nil), original...)
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	validator := &fakeValidator{user: &User{ID: "new-user", Username: "new-name"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: root + "/api/v4", Token: "new-token", DisplayName: "New"},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(candidate config.Config) error {
			if candidate.Servers[1].DisplayName != "New" {
				t.Fatalf("update did not preserve position: %#v", candidate.Servers)
			}
			return errors.New("save failed")
		})
	if err == nil {
		t.Fatal("expected save failure")
	}
	if fmt.Sprint(cfg.Servers) != fmt.Sprint(original) || secrets.values[id] != "old-token" {
		t.Fatalf("update rollback failed: cfg=%#v secret=%q", cfg.Servers, secrets.values[id])
	}
}

func TestAddServerReAddUpdatesInPlaceAndReplacesToken(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example.com")
	id := ServerID(root)
	cfg := config.Default()
	cfg.Servers = []config.MattermostServer{
		{ID: "first", URL: "https://first.example.com"},
		{ID: id, URL: root, DisplayName: "Old", UserID: "old-user"},
		{ID: "last", URL: "https://last.example.com"},
	}
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	validator := &fakeValidator{user: &User{ID: "new-user", Username: "new-name"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: root + "/api/v4", Token: "new-token", DisplayName: "New"},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(config.Config) error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Servers) != 3 || cfg.Servers[0].ID != "first" || cfg.Servers[1].ID != id || cfg.Servers[2].ID != "last" {
		t.Fatalf("order changed: %#v", cfg.Servers)
	}
	if cfg.Servers[1].DisplayName != "New" || cfg.Servers[1].UserID != "new-user" || secrets.values[id] != "new-token" {
		t.Fatalf("update failed: server=%#v secret=%q", cfg.Servers[1], secrets.values[id])
	}
}

func TestAddServerReAddWithMissingPreviousSecretDeletesNewTokenOnSaveFailure(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example.com")
	id := ServerID(root)
	cfg := config.Default()
	cfg.Servers = []config.MattermostServer{{ID: id, URL: root, DisplayName: "Old"}}
	secrets := &fakeSecrets{values: map[string]string{}}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: root, Token: "new-token", DisplayName: "New"},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(config.Config) error { return errors.New("save failed") })
	if err == nil {
		t.Fatal("expected save failure")
	}
	if cfg.Servers[0].DisplayName != "Old" || len(secrets.deletes) != 1 || len(secrets.values) != 0 {
		t.Fatalf("rollback = cfg=%#v deletes=%v values=%v", cfg.Servers, secrets.deletes, secrets.values)
	}
}

func TestAddServerUpdateRollbackFailureMatchesBothErrorsWithoutPAT(t *testing.T) {
	const token = "pat-secret"
	root, _ := CanonicalServerRoot("https://chat.example.com")
	id := ServerID(root)
	saveErr := errors.New("save failed")
	rollbackErr := errors.New("restore failed")
	cfg := config.Default()
	cfg.Servers = []config.MattermostServer{{ID: id, URL: root}}
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	validator := &fakeValidator{user: &User{ID: "user-1"}}
	setCalls := 0
	secrets.setErr = nil

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: root, Token: token},
		func(string, string) (ServerValidator, error) { return validator, nil }, &setFailureOnSecondCallStore{
			fakeSecrets: secrets,
			err:         fmt.Errorf("%w: %s", rollbackErr, token),
			calls:       &setCalls,
		}, func(config.Config) error { return saveErr })
	if !errors.Is(err, saveErr) || !errors.Is(err, rollbackErr) || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
}

type setFailureOnSecondCallStore struct {
	*fakeSecrets
	err   error
	calls *int
}

func (s *setFailureOnSecondCallStore) Set(ctx context.Context, serverID, token string) error {
	*s.calls++
	if *s.calls == 2 {
		return s.err
	}
	return s.fakeSecrets.Set(ctx, serverID, token)
}

func TestAddServerReportsRollbackFailureWithoutPAT(t *testing.T) {
	const token = "pat-secret"
	saveErr := errors.New("save failed")
	rollbackErr := errors.New("rollback failed")
	cfg := config.Default()
	secrets := &fakeSecrets{deleteErr: fmt.Errorf("%w: %s", rollbackErr, token)}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: "https://chat.example.com", Token: token},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(config.Config) error { return saveErr })
	if !errors.Is(err, saveErr) || !errors.Is(err, rollbackErr) || strings.Contains(err.Error(), token) {
		t.Fatalf("error = %v", err)
	}
}

func TestAddServerDoesNotClobberConcurrentCredentialOnRollback(t *testing.T) {
	const token = "our-token"
	cfg := config.Default()
	secrets := &fakeSecrets{}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	err := AddServer(context.Background(), &cfg, AddServerInput{URL: "https://chat.example.com", Token: token},
		func(string, string) (ServerValidator, error) { return validator, nil }, secrets,
		func(candidate config.Config) error {
			secrets.values[candidate.Servers[0].ID] = "other-writer-token"
			return errors.New("save failed")
		})
	if !errors.Is(err, ErrConcurrentCredentialChange) || strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "other-writer-token") {
		t.Fatalf("error = %v", err)
	}
	if len(secrets.values) != 1 {
		t.Fatalf("credential was deleted: %#v", secrets.values)
	}
}
