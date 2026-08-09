//go:build linux

package mattermost

import (
	"context"
	"errors"

	"r00t2.io/gosecret"
)

func secretAttributes(serverID string) map[string]string {
	return map[string]string{"service": SecretServiceName(), "account": SecretAccountName(serverID)}
}

func (s *OSSecretStore) Get(_ context.Context, serverID string) (string, error) {
	service, err := gosecret.NewService()
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	defer service.Close()
	unlocked, locked, err := service.SearchItems(secretAttributes(serverID))
	return secretFromUnlockedItems(unlocked, locked, err)
}

func (s *OSSecretStore) Set(_ context.Context, serverID, token string) error {
	service, err := gosecret.NewService()
	if err != nil {
		return secretStoreUnavailable(err)
	}
	defer service.Close()
	collection, err := service.GetCollection("default")
	if err != nil {
		return secretStoreUnavailable(err)
	}
	return withSecretBytes(token, func(value []byte) error {
		secret := gosecret.NewSecret(service.Session, nil, value, "text/plain")
		defer clear(secret.Value)
		if _, err := collection.CreateItem(SecretAccountName(serverID), secretAttributes(serverID), secret, true); err != nil {
			return secretStoreUnavailable(err)
		}
		return nil
	})
}

func (s *OSSecretStore) Delete(_ context.Context, serverID string) error {
	service, err := gosecret.NewService()
	if err != nil {
		return secretStoreUnavailable(err)
	}
	defer service.Close()
	unlocked, locked, err := service.SearchItems(secretAttributes(serverID))
	return withUnlockedSecretItems(unlocked, func() error {
		if err != nil {
			return secretStoreUnavailable(err)
		}
		if len(locked) > 0 {
			return secretStoreUnavailable(errors.New("credential is locked"))
		}
		for _, item := range unlocked {
			if item == nil {
				continue
			}
			if err := item.Delete(); err != nil {
				return secretStoreUnavailable(err)
			}
		}
		return nil
	})
}

func secretFromUnlockedItems(unlocked, locked []*gosecret.Item, searchErr error) (secret string, err error) {
	err = withUnlockedSecretItems(unlocked, func() error {
		if searchErr != nil {
			return secretStoreUnavailable(searchErr)
		}
		if len(unlocked) == 0 {
			if len(locked) > 0 {
				return secretStoreUnavailable(errors.New("credential is locked"))
			}
			return ErrSecretNotFound
		}
		if unlocked[0] == nil || unlocked[0].Secret == nil {
			return ErrSecretNotFound
		}
		secret = string(unlocked[0].Secret.Value)
		return nil
	})
	return secret, err
}

func withUnlockedSecretItems(items []*gosecret.Item, operation func() error) error {
	defer func() {
		for _, item := range items {
			if item != nil && item.Secret != nil {
				clear(item.Secret.Value)
			}
		}
	}()
	return operation()
}
