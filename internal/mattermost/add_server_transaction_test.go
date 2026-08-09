package mattermost

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"

	"github.com/nosovk/mmk/internal/config"
)

type fakeConfigTransaction struct {
	mu        sync.Mutex
	document  []byte
	loadCalls int
	saveHook  func([]byte) error
	loadErr   error
	unlockErr error
	unlocks   int
}

func (f *fakeConfigTransaction) Lock(context.Context) (func() error, error) {
	f.mu.Lock()
	return func() error {
		f.unlocks++
		f.mu.Unlock()
		return f.unlockErr
	}, nil
}

func (f *fakeConfigTransaction) Load(context.Context) (config.Config, []byte, error) {
	f.loadCalls++
	if f.loadErr != nil {
		return config.Config{}, nil, f.loadErr
	}
	cfg, err := config.LoadBytes(f.document)
	return cfg, append([]byte(nil), f.document...), err
}

func (f *fakeConfigTransaction) Save(_ context.Context, document []byte) error {
	f.document = append([]byte(nil), document...)
	if f.saveHook != nil {
		if err := f.saveHook(document); err != nil {
			return err
		}
	}
	return nil
}

func TestAddServerTransactionSerializesAndReloadsFreshConfig(t *testing.T) {
	tx := &fakeConfigTransaction{document: []byte("# keep\n[feature]\nenabled = true\n")}
	secrets := &fakeSecrets{}
	firstSaving := make(chan struct{})
	releaseFirst := make(chan struct{})
	secondValidated := make(chan struct{})
	secondLocked := make(chan struct{})
	loadSecond := make(chan struct{})
	var saveCount int
	tx.saveHook = func(document []byte) error {
		saveCount++
		if saveCount == 1 {
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
		_, err := AddServerTransaction(context.Background(), AddServerInput{URL: "https://two.example", Token: "token-two"}, validatorFor("user-two", secondValidated), secrets, lockingTransaction{ConfigTransaction: tx, locked: secondLocked, load: loadSecond})
		errCh <- err
	}()
	<-secondValidated
	if tx.loadCalls != 1 {
		t.Fatalf("second transaction loaded before lock release: %d loads", tx.loadCalls)
	}
	close(releaseFirst)
	<-secondLocked
	tx.document = append(tx.document, []byte("# concurrent unrelated edit\n")...)
	close(loadSecond)
	for range 2 {
		if err := <-errCh; err != nil {
			t.Fatal(err)
		}
	}
	text := string(tx.document)
	for _, want := range []string{"# keep", "# concurrent unrelated edit", "https://one.example", "https://two.example"} {
		if !strings.Contains(text, want) {
			t.Errorf("final document missing %q:\n%s", want, text)
		}
	}
	if tx.loadCalls != 2 {
		t.Fatalf("loads = %d, want one fresh load per transaction", tx.loadCalls)
	}
}

type lockingTransaction struct {
	ConfigTransaction
	locked chan<- struct{}
	load   <-chan struct{}
}

func (t lockingTransaction) Load(ctx context.Context) (config.Config, []byte, error) {
	<-t.load
	return t.ConfigTransaction.Load(ctx)
}

func (t lockingTransaction) Lock(ctx context.Context) (func() error, error) {
	unlock, err := t.ConfigTransaction.Lock(ctx)
	if err == nil {
		close(t.locked)
	}
	return unlock, err
}

func TestAddServerTransactionDoesNotClobberConcurrentCredentialOnRollback(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example")
	id := ServerID(root)
	tests := []struct {
		name     string
		document string
		oldToken string
	}{
		{name: "update", document: "[[mattermost_servers]]\nid='" + id + "'\nurl='" + root + "'\ndisplay_name=''\nuser_id='old'\nusername=''\n", oldToken: "old-token"},
		{name: "new", document: "# empty\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			secrets := &fakeSecrets{values: map[string]string{}}
			if tt.oldToken != "" {
				secrets.values[id] = tt.oldToken
			}
			tx := &fakeConfigTransaction{document: []byte(tt.document)}
			tx.saveHook = func([]byte) error {
				secrets.values[id] = "other-writer-token"
				return errors.New("save failed")
			}
			validator := &fakeValidator{user: &User{ID: "new-user"}}
			_, err := AddServerTransaction(context.Background(), AddServerInput{URL: root, Token: "our-token"}, func(string, string) (ServerValidator, error) { return validator, nil }, secrets, tx)
			if !errors.Is(err, ErrConcurrentCredentialChange) || strings.Contains(err.Error(), "our-token") || strings.Contains(err.Error(), "other-writer-token") {
				t.Fatalf("error = %v", err)
			}
			if secrets.values[id] != "other-writer-token" {
				t.Fatalf("concurrent credential was clobbered: %q", secrets.values[id])
			}
		})
	}
}

func TestAddServerTransactionTreatsConcurrentCredentialDeletionAsChange(t *testing.T) {
	root, _ := CanonicalServerRoot("https://chat.example")
	id := ServerID(root)
	secrets := &fakeSecrets{values: map[string]string{id: "old-token"}}
	tx := &fakeConfigTransaction{
		document: []byte("[[mattermost_servers]]\nid='" + id + "'\nurl='" + root + "'\n"),
		saveHook: func([]byte) error {
			delete(secrets.values, id)
			return errors.New("save failed")
		},
	}
	validator := &fakeValidator{user: &User{ID: "new-user"}}

	_, err := AddServerTransaction(context.Background(), AddServerInput{URL: root, Token: "our-token"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, secrets, tx)
	if !errors.Is(err, ErrConcurrentCredentialChange) || strings.Contains(err.Error(), "our-token") || strings.Contains(err.Error(), "old-token") {
		t.Fatalf("error = %v", err)
	}
	if _, ok := secrets.values[id]; ok {
		t.Fatalf("concurrently deleted credential was restored: %#v", secrets.values)
	}
}

func TestAddServerTransactionReportsUnlockFailureOnEarlyReturn(t *testing.T) {
	unlockErr := errors.New("unlock failed")
	tx := &fakeConfigTransaction{loadErr: errors.New("load failed"), unlockErr: unlockErr}
	validator := &fakeValidator{user: &User{ID: "user-1"}}

	_, err := AddServerTransaction(context.Background(), AddServerInput{URL: "https://chat.example", Token: "token"}, func(string, string) (ServerValidator, error) {
		return validator, nil
	}, &fakeSecrets{}, tx)
	if !errors.Is(err, unlockErr) {
		t.Fatalf("error = %v, want unlock failure in chain", err)
	}
	if tx.unlocks != 1 {
		t.Fatalf("unlock calls = %d, want 1", tx.unlocks)
	}
}
