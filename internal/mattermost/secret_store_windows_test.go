//go:build windows

package mattermost

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"golang.org/x/sys/windows"
)

type fakeWindowsCredentialAPI struct {
	value         []byte
	readErr       error
	writeErr      error
	deleteErr     error
	written       []byte
	writtenTarget string
	writtenUser   string
	deletedTarget string
}

func (f *fakeWindowsCredentialAPI) read(target string) ([]byte, error) {
	return append([]byte(nil), f.value...), f.readErr
}

func (f *fakeWindowsCredentialAPI) write(target, username string, value []byte) error {
	f.writtenTarget = target
	f.writtenUser = username
	f.written = append([]byte(nil), value...)
	return f.writeErr
}

func (f *fakeWindowsCredentialAPI) delete(target string) error {
	f.deletedTarget = target
	return f.deleteErr
}

func TestWindowsCredentialStoreUsesGenericTargetAndAccount(t *testing.T) {
	const serverID = "server-id"
	api := &fakeWindowsCredentialAPI{value: []byte("old-token")}
	store := windowsCredentialStore{api: api}

	got, err := store.get(serverID)
	if err != nil || got != "old-token" {
		t.Fatalf("get = %q, %v", got, err)
	}
	if err := store.set(serverID, "new-token"); err != nil {
		t.Fatal(err)
	}
	if api.writtenTarget != WindowsCredentialTargetName(serverID) || api.writtenUser != SecretAccountName(serverID) || string(api.written) != "new-token" {
		t.Fatalf("write = target %q user %q value %q", api.writtenTarget, api.writtenUser, api.written)
	}
	if err := store.delete(serverID); err != nil {
		t.Fatal(err)
	}
	if api.deletedTarget != WindowsCredentialTargetName(serverID) {
		t.Fatalf("deleted target = %q", api.deletedTarget)
	}
}

func TestWindowsCredentialStoreMapsNotFound(t *testing.T) {
	store := windowsCredentialStore{api: &fakeWindowsCredentialAPI{readErr: windows.ERROR_NOT_FOUND}}
	if _, err := store.get("server-id"); !errors.Is(err, ErrSecretNotFound) {
		t.Fatalf("get error = %v", err)
	}

	store.api = &fakeWindowsCredentialAPI{deleteErr: windows.ERROR_NOT_FOUND}
	if err := store.delete("server-id"); err != nil {
		t.Fatalf("delete error = %v", err)
	}
}

func TestWindowsCredentialStoreRedactsTokenFromWriteErrors(t *testing.T) {
	const token = "pat-super-secret"
	sentinel := errors.New("write sentinel")
	store := windowsCredentialStore{api: &fakeWindowsCredentialAPI{writeErr: fmt.Errorf("%w: %s", sentinel, token)}}

	err := store.set("server-id", token)
	if !errors.Is(err, sentinel) || !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("error chain = %v", err)
	}
	for _, inspected := range []string{err.Error(), fmt.Sprintf("%#v", err)} {
		if strings.Contains(inspected, token) {
			t.Fatalf("error leaks token: %s", inspected)
		}
	}
}
