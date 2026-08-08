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
