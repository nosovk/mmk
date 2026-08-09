package mattermost

import (
	"errors"
)

var errDarwinKeychainNotFound = errors.New("macOS Keychain item not found")

type darwinKeychainItem uintptr

type darwinKeychainAPI interface {
	find(service, account string) ([]byte, darwinKeychainItem, error)
	add(service, account string, secret []byte) error
	update(item darwinKeychainItem, secret []byte) error
	delete(item darwinKeychainItem) error
	release(item darwinKeychainItem)
}

type darwinKeychainStore struct {
	api darwinKeychainAPI
}

func (s darwinKeychainStore) get(serverID string) (string, error) {
	secret, item, err := s.api.find(SecretServiceName(), SecretAccountName(serverID))
	if errors.Is(err, errDarwinKeychainNotFound) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", darwinKeychainError("read macOS Keychain credential", err)
	}
	defer s.api.release(item)
	defer clear(secret)
	return string(secret), nil
}

func (s darwinKeychainStore) set(serverID, token string) error {
	secret := []byte(token)
	defer clear(secret)

	existingSecret, item, err := s.api.find(SecretServiceName(), SecretAccountName(serverID))
	clear(existingSecret)
	switch {
	case err == nil:
		defer s.api.release(item)
		err = s.api.update(item, secret)
	case errors.Is(err, errDarwinKeychainNotFound):
		err = s.api.add(SecretServiceName(), SecretAccountName(serverID), secret)
	default:
		return darwinKeychainError("find macOS Keychain credential", err, token)
	}
	if err != nil {
		return darwinKeychainError("write macOS Keychain credential", err, token)
	}
	return nil
}

func (s darwinKeychainStore) delete(serverID string) error {
	secret, item, err := s.api.find(SecretServiceName(), SecretAccountName(serverID))
	clear(secret)
	if errors.Is(err, errDarwinKeychainNotFound) {
		return nil
	}
	if err != nil {
		return darwinKeychainError("find macOS Keychain credential", err)
	}
	defer s.api.release(item)
	if err := s.api.delete(item); errors.Is(err, errDarwinKeychainNotFound) {
		return nil
	} else if err != nil {
		return darwinKeychainError("delete macOS Keychain credential", err)
	}
	return nil
}

func darwinKeychainError(operation string, err error, secrets ...string) error {
	message := err.Error()
	for _, secret := range secrets {
		message = redactSecret(message, secret)
	}
	return errors.Join(
		secretStoreUnavailable(errors.New(message)),
		redactError(operation, err, secrets...),
	)
}
