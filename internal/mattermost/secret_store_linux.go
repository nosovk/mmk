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
	if err != nil {
		return "", secretStoreUnavailable(err)
	}
	if len(unlocked) == 0 {
		if len(locked) > 0 {
			return "", secretStoreUnavailable(errors.New("credential is locked"))
		}
		return "", ErrSecretNotFound
	}
	if unlocked[0].Secret == nil {
		return "", ErrSecretNotFound
	}
	return secretString(unlocked[0].Secret.Value), nil
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
	if err != nil {
		return secretStoreUnavailable(err)
	}
	if len(locked) > 0 {
		return secretStoreUnavailable(errors.New("credential is locked"))
	}
	for _, item := range unlocked {
		if err := item.Delete(); err != nil {
			return secretStoreUnavailable(err)
		}
	}
	return nil
}
