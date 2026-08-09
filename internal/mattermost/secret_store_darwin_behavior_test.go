package mattermost

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

type fakeDarwinKeychainAPI struct {
	value       []byte
	findErr     error
	addErr      error
	updateErr   error
	deleteErr   error
	added       []byte
	updated     []byte
	deleteCalls int
	returned    []byte
}

func (f *fakeDarwinKeychainAPI) find(service, account string) ([]byte, darwinKeychainItem, error) {
	f.returned = append([]byte(nil), f.value...)
	return f.returned, darwinKeychainItem(1), f.findErr
}

func (f *fakeDarwinKeychainAPI) add(service, account string, secret []byte) error {
	f.added = append([]byte(nil), secret...)
	return f.addErr
}

func (f *fakeDarwinKeychainAPI) update(_ darwinKeychainItem, secret []byte) error {
	f.updated = append([]byte(nil), secret...)
	return f.updateErr
}

func (f *fakeDarwinKeychainAPI) delete(darwinKeychainItem) error {
	f.deleteCalls++
	return f.deleteErr
}

func (f *fakeDarwinKeychainAPI) release(darwinKeychainItem) {}

func TestDarwinKeychainStoreAddsOrUpdatesCredential(t *testing.T) {
	const token = "new-token"

	missing := &fakeDarwinKeychainAPI{findErr: errDarwinKeychainNotFound}
	if err := (darwinKeychainStore{api: missing}).set("server-id", token); err != nil {
		t.Fatal(err)
	}
	if string(missing.added) != token || len(missing.updated) != 0 {
		t.Fatalf("missing item: added=%q updated=%q", missing.added, missing.updated)
	}

	existing := &fakeDarwinKeychainAPI{value: []byte("old-token")}
	if err := (darwinKeychainStore{api: existing}).set("server-id", token); err != nil {
		t.Fatal(err)
	}
	if string(existing.updated) != token || len(existing.added) != 0 {
		t.Fatalf("existing item: added=%q updated=%q", existing.added, existing.updated)
	}
	for i, value := range existing.returned {
		if value != 0 {
			t.Fatalf("existing secret byte %d was not cleared", i)
		}
	}
}

func TestDarwinKeychainStoreMapsNotFound(t *testing.T) {
	store := darwinKeychainStore{api: &fakeDarwinKeychainAPI{findErr: errDarwinKeychainNotFound}}
	if _, err := store.get("server-id"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("get error = %v", err)
	}
	if err := store.delete("server-id"); err != nil {
		t.Fatalf("delete error = %v", err)
	}

	store.api = &fakeDarwinKeychainAPI{deleteErr: errDarwinKeychainNotFound}
	if err := store.delete("server-id"); err != nil {
		t.Fatalf("delete after concurrent removal = %v", err)
	}

	existing := &fakeDarwinKeychainAPI{value: []byte("old-token")}
	if err := (darwinKeychainStore{api: existing}).delete("server-id"); err != nil {
		t.Fatal(err)
	}
	for i, value := range existing.returned {
		if value != 0 {
			t.Fatalf("deleted secret byte %d was not cleared", i)
		}
	}
}

func TestDarwinKeychainStoreRedactsTokenErrors(t *testing.T) {
	const token = "pat-super-secret"
	sentinel := errors.New("update sentinel")
	api := &fakeDarwinKeychainAPI{updateErr: fmt.Errorf("%w: %s", sentinel, token)}

	err := (darwinKeychainStore{api: api}).set("server-id", token)
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("error chain = %v", err)
	}
	for _, inspected := range []string{err.Error(), fmt.Sprintf("%#v", err)} {
		if strings.Contains(inspected, token) {
			t.Fatalf("error leaks token: %s", inspected)
		}
	}
}
