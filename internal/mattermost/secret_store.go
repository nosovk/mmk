package mattermost

import (
	"errors"
	"fmt"
)

var ErrSecretStoreUnavailable = errors.New("system credential store unavailable")

func SecretServiceName() string { return "mmk" }

func SecretAccountName(serverID string) string { return "mattermost:" + serverID }

// WindowsCredentialTargetName combines the cross-platform service and account
// names into the generic credential key stored by Windows Credential Manager.
func WindowsCredentialTargetName(serverID string) string {
	return SecretServiceName() + "/" + SecretAccountName(serverID)
}

func secretStoreUnavailable(cause error) error {
	return fmt.Errorf("%w: unlock your system credential store and ensure a desktop session is available: %v", ErrSecretStoreUnavailable, cause)
}

type OSSecretStore struct{}

func NewOSSecretStore() *OSSecretStore { return &OSSecretStore{} }
