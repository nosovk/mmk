package mattermost

import (
	"errors"
	"fmt"
)

var ErrSecretStoreUnavailable = errors.New("system credential store unavailable")

func SecretServiceName() string { return "mmk" }

func SecretAccountName(serverID string) string { return "mattermost:" + serverID }

func secretStoreUnavailable(cause error) error {
	return fmt.Errorf("%w: unlock your system credential store and ensure a desktop session is available: %v", ErrSecretStoreUnavailable, cause)
}

type OSSecretStore struct{}

func NewOSSecretStore() *OSSecretStore { return &OSSecretStore{} }
