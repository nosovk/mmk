//go:build darwin && !cgo

package mattermost

import (
	"context"
	"errors"
)

func darwinCGOUnavailable() error {
	return secretStoreUnavailable(errors.New("mmk was built without macOS Keychain Services support"))
}

func (s *OSSecretStore) Get(context.Context, string) (string, error) {
	return "", darwinCGOUnavailable()
}

func (s *OSSecretStore) Set(context.Context, string, string) error {
	return darwinCGOUnavailable()
}

func (s *OSSecretStore) Delete(context.Context, string) error {
	return darwinCGOUnavailable()
}
