package mattermost

import (
	"errors"
	"strings"
	"testing"
)

func TestMattermostCredentialNaming(t *testing.T) {
	const serverID = "chat-example-com-0123456789abcdef"
	if got := SecretServiceName(); got != "mmk" {
		t.Errorf("service = %q", got)
	}
	if got := SecretAccountName(serverID); got != "mattermost:"+serverID {
		t.Errorf("account = %q", got)
	}
	if got := WindowsCredentialTargetName(serverID); got != "mmk/mattermost:"+serverID {
		t.Errorf("Windows target name = %q", got)
	}
}

func TestSecretStoreUnavailableErrorIsActionable(t *testing.T) {
	err := secretStoreUnavailable(errors.New("dbus unavailable"))
	if !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("errors.Is = false: %v", err)
	}
	for _, want := range []string{"system credential store", "unlock", "desktop session"} {
		if !strings.Contains(strings.ToLower(err.Error()), want) {
			t.Errorf("error %q missing %q", err, want)
		}
	}
}

func TestSecretStringClearsOwnedBytes(t *testing.T) {
	owned := []byte("secret-value")
	if got := secretString(owned); got != "secret-value" {
		t.Fatalf("secretString = %q", got)
	}
	for i, value := range owned {
		if value != 0 {
			t.Fatalf("owned byte %d was not cleared", i)
		}
	}
}

func TestWithSecretBytesClearsOwnedBytes(t *testing.T) {
	var owned []byte
	if err := withSecretBytes("secret-value", func(value []byte) error {
		owned = value
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	for i, value := range owned {
		if value != 0 {
			t.Fatalf("owned byte %d was not cleared", i)
		}
	}
}
