package mattermost

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/nosovk/mmk/internal/config"
)

type fakeRegistryTransaction struct {
	mu        sync.Mutex
	registry  config.ServerRegistry
	secrets   *fakeSecrets
	loadErr   error
	saveErr   error
	saveHook  func(config.ServerRegistry) error
	unlockErr error
	lockCalls int
	loadCalls int
	saveCalls int
	unlocks   int
	events    string
}

func (f *fakeRegistryTransaction) event(name string) {
	if f.events != "" {
		f.events += ","
	}
	f.events += name
}

func (f *fakeRegistryTransaction) Lock(context.Context) (func() error, error) {
	f.lockCalls++
	f.mu.Lock()
	f.event("lock")
	return func() error {
		f.unlocks++
		f.event("unlock")
		f.mu.Unlock()
		return f.unlockErr
	}, nil
}

func (f *fakeRegistryTransaction) Load(context.Context) (config.ServerRegistry, error) {
	f.loadCalls++
	f.event("load")
	if f.loadErr != nil {
		return config.ServerRegistry{}, f.loadErr
	}
	registry := f.registry
	registry.Servers = append([]config.MattermostServer(nil), f.registry.Servers...)
	return registry, nil
}

func (f *fakeRegistryTransaction) Save(_ context.Context, registry config.ServerRegistry) error {
	f.saveCalls++
	f.event("save")
	if f.saveHook != nil {
		if err := f.saveHook(registry); err != nil {
			return err
		}
	}
	if f.saveErr != nil {
		if config.RegistrySaveCommitted(f.saveErr) {
			f.registry = registry
			f.registry.Servers = append([]config.MattermostServer(nil), registry.Servers...)
		}
		return f.saveErr
	}
	f.registry = registry
	f.registry.Servers = append([]config.MattermostServer(nil), registry.Servers...)
	return nil
}

type eventSecrets struct {
	*fakeSecrets
	tx *fakeRegistryTransaction
}

func (s eventSecrets) Get(ctx context.Context, serverID string) (string, error) {
	s.tx.event("get")
	return s.fakeSecrets.Get(ctx, serverID)
}

func (s eventSecrets) Set(ctx context.Context, serverID, token string) error {
	s.tx.event("set")
	return s.fakeSecrets.Set(ctx, serverID, token)
}

func (s eventSecrets) Delete(ctx context.Context, serverID string) error {
	s.tx.event("delete")
	return s.fakeSecrets.Delete(ctx, serverID)
}

func TestAddServerTransactionReloadsUnderLockAndPreservesConcurrentAddition(t *testing.T) {
	tx := &fakeRegistryTransaction{registry: config.NewServerRegistry()}
	secrets := &fakeSecrets{}
	firstSaving := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondValidated := make(chan struct{})
	var saves int
	tx.saveHook = func(config.ServerRegistry) error {
		saves++
		if saves == 1 {
			close(firstSaving)
			<-releaseFirst
		}
		return nil
	}
	validatorFor := func(userID string, validated chan<- struct{}) ValidatorFactory {
		return func(string, string) (ServerValidator, error) {
			if validated != nil {
				close(validated)
			}
			return &fakeValidator{user: &User{ID: userID, Username: userID}}, nil
		}
	}
	errCh := make(chan error, 2)
	go func() {
		_, err := AddServerTransaction(context.Background(), AddServerInput{URL: "https://one.example", Token: "token-one"}, validatorFor("user-one", nil), secrets, tx)
		errCh <- err
	}()
	<-firstSaving
	go func() {
		_, err := AddServerTransaction(context.Background(), AddServerInput{URL: "https://two.example", Token: "token-two"}, validatorFor("user-two", secondValidated), secrets, tx)
		errCh <- err
	}()
	<-secondValidated
	if tx.loadCalls != 1 {
		t.Fatalf("second transaction loaded before lock release: %d loads", tx.loadCalls)
	}
	close(releaseFirst)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	if tx.loadCalls != 2 || len(tx.registry.Servers) != 2 || tx.registry.Servers[0].URL != "https://one.example" || tx.registry.Servers[1].URL != "https://two.example" {
		t.Fatalf("registry = %#v, loads=%d", tx.registry, tx.loadCalls)
	}
}

func TestAddServerTransactionUpdatesExistingServerInPlace(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example")
	id := ServerID(root)
	tx := &fakeRegistryTransaction{registry: config.ServerRegistry{Version: config.ServerRegistryVersion, Servers: []config.MattermostServer{
		{ID: "first", URL: "https://first.example", UserID: "first-user"},
		{ID: id, URL: root, DisplayName: "Old", UserID: "old-user"},
		{ID: "last", URL: "https://last.example", UserID: "last-user"},
	}}}
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	validator := &fakeValidator{user: &User{ID: "new-user", Username: "alice"}}

	_, err := AddServerTransaction(t.Context(), AddServerInput{URL: root, Token: "new-token", DisplayName: "New"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, secrets, tx)
	if err != nil {
		t.Fatal(err)
	}
	if len(tx.registry.Servers) != 3 || tx.registry.Servers[0].ID != "first" || tx.registry.Servers[1].ID != id || tx.registry.Servers[2].ID != "last" {
		t.Fatalf("order changed: %#v", tx.registry.Servers)
	}
	if tx.registry.Servers[1].DisplayName != "New" || tx.registry.Servers[1].UserID != "new-user" || secrets.values[id] != "new-token" {
		t.Fatalf("update failed: registry=%#v secrets=%#v", tx.registry, secrets.values)
	}
}

func TestAddServerTransactionReportsUnlockFailureOnEarlyReturn(t *testing.T) {
	unlockErr := errors.New("unlock failed")
	tx := &fakeRegistryTransaction{registry: config.NewServerRegistry(), loadErr: errors.New("load failed"), unlockErr: unlockErr}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	_, err := AddServerTransaction(t.Context(), AddServerInput{URL: "https://chat.example", Token: "token"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, &fakeSecrets{}, tx)
	if !errors.Is(err, unlockErr) || tx.unlocks != 1 {
		t.Fatalf("error=%v unlocks=%d", err, tx.unlocks)
	}
}
