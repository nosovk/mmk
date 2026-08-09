//go:build linux

package mattermost

import (
	"errors"
	"testing"

	"r00t2.io/gosecret"
)

func TestWithUnlockedSecretItemsClearsEveryReturnedValue(t *testing.T) {
	first := gosecret.SecretValue("first-secret")
	duplicate := gosecret.SecretValue("duplicate-secret")
	items := []*gosecret.Item{
		{Secret: &gosecret.Secret{Value: first}},
		{Secret: &gosecret.Secret{Value: duplicate}},
		{Secret: nil},
		nil,
	}

	err := withUnlockedSecretItems(items, func() error { return nil })
	if err != nil {
		t.Fatal(err)
	}
	assertZeroedSecretValue(t, first)
	assertZeroedSecretValue(t, duplicate)
}

func TestWithUnlockedSecretItemsClearsValuesWhenOperationFails(t *testing.T) {
	value := gosecret.SecretValue("search-result-secret")
	sentinel := errors.New("operation failed after search")

	err := withUnlockedSecretItems([]*gosecret.Item{{Secret: &gosecret.Secret{Value: value}}}, func() error {
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	assertZeroedSecretValue(t, value)
}

func TestSecretFromUnlockedItemsClearsDuplicateMatches(t *testing.T) {
	first := gosecret.SecretValue("selected-secret")
	duplicate := gosecret.SecretValue("duplicate-secret")

	got, err := secretFromUnlockedItems([]*gosecret.Item{
		{Secret: &gosecret.Secret{Value: first}},
		{Secret: &gosecret.Secret{Value: duplicate}},
	}, nil, nil)
	if err != nil || got != "selected-secret" {
		t.Fatalf("secret = %q, error = %v", got, err)
	}
	assertZeroedSecretValue(t, first)
	assertZeroedSecretValue(t, duplicate)
}

func TestSecretFromUnlockedItemsClearsValuesOnSearchError(t *testing.T) {
	value := gosecret.SecretValue("returned-before-error")
	sentinel := errors.New("partial search failed")

	_, err := secretFromUnlockedItems([]*gosecret.Item{{Secret: &gosecret.Secret{Value: value}}}, nil, sentinel)
	if !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("error = %v", err)
	}
	assertZeroedSecretValue(t, value)
}

func assertZeroedSecretValue(t *testing.T, value gosecret.SecretValue) {
	t.Helper()
	for i, b := range value {
		if b != 0 {
			t.Fatalf("secret byte %d was not cleared", i)
		}
	}
}
