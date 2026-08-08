//go:build windows

package mattermost

import (
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"

	"github.com/billgraziano/dpapi"
)

func windowsCredentialPath(serverID string) (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "mmk", "credentials", serverID), nil
}

func (s *OSSecretStore) Get(_ context.Context, serverID string) (string, error) {
	path, err := windowsCredentialPath(serverID)
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	encrypted, err := base64.StdEncoding.DecodeString(string(data))
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	plain, err := dpapi.DecryptBytes(encrypted)
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	return string(plain), nil
}

func (s *OSSecretStore) Set(_ context.Context, serverID, token string) error {
	path, err := windowsCredentialPath(serverID)
	if err != nil {
		return secretStoreUnavailable(err)
	}
	encrypted, err := dpapi.EncryptBytes([]byte(token))
	if err != nil {
		return secretStoreUnavailable(err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return secretStoreUnavailable(err)
	}
	if err := os.WriteFile(path, []byte(base64.StdEncoding.EncodeToString(encrypted)), 0600); err != nil {
		return secretStoreUnavailable(err)
	}
	return nil
}

func (s *OSSecretStore) Delete(_ context.Context, serverID string) error {
	path, err := windowsCredentialPath(serverID)
	if err != nil {
		return secretStoreUnavailable(err)
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return secretStoreUnavailable(err)
	}
	return nil
}
