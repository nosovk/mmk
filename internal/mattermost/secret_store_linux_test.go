//go:build linux

package mattermost

import (
	"context"
	"errors"
	"testing"

	"r00t2.io/gosecret"
)

type fakeLinuxSecretSearchService struct {
	unlocked []*gosecret.Item
	locked   []*gosecret.Item
	err      error
	closed   bool
}

func (f *fakeLinuxSecretSearchService) SearchItems(map[string]string) ([]*gosecret.Item, []*gosecret.Item, error) {
	return f.unlocked, f.locked, f.err
}

func (f *fakeLinuxSecretSearchService) Close() error {
	f.closed = true
	return nil
}

type fakeLinuxSecretItemDeleter struct {
	calls int
	errAt int
	err   error
}

func (f *fakeLinuxSecretItemDeleter) delete(*gosecret.Item) error {
	f.calls++
	if f.calls == f.errAt {
		return f.err
	}
	return nil
}

func TestOSSecretStoreDeleteClearsDuplicateResults(t *testing.T) {
	first := gosecret.SecretValue("first-secret")
	duplicate := gosecret.SecretValue("duplicate-secret")
	service := &fakeLinuxSecretSearchService{unlocked: []*gosecret.Item{
		{Secret: &gosecret.Secret{Value: first}},
		{Secret: &gosecret.Secret{Value: duplicate}},
	}}
	deleter := &fakeLinuxSecretItemDeleter{}
	originalServiceFactory := newLinuxSecretSearchService
	originalDeleteItem := deleteLinuxSecretSearchItem
	newLinuxSecretSearchService = func() (linuxSecretSearchService, error) { return service, nil }
	deleteLinuxSecretSearchItem = deleter.delete
	t.Cleanup(func() {
		newLinuxSecretSearchService = originalServiceFactory
		deleteLinuxSecretSearchItem = originalDeleteItem
	})

	err := NewOSSecretStore().Delete(context.Background(), "server-id")
	if err != nil {
		t.Fatal(err)
	}
	if deleter.calls != 2 || !service.closed {
		t.Fatalf("delete calls = %d, closed = %v", deleter.calls, service.closed)
	}
	assertZeroedSecretValue(t, first)
	assertZeroedSecretValue(t, duplicate)
}

func TestOSSecretStoreDeleteClearsDuplicateResultsOnDeleteError(t *testing.T) {
	first := gosecret.SecretValue("first-secret")
	duplicate := gosecret.SecretValue("duplicate-secret")
	sentinel := errors.New("delete failed")
	service := &fakeLinuxSecretSearchService{unlocked: []*gosecret.Item{
		{Secret: &gosecret.Secret{Value: first}},
		{Secret: &gosecret.Secret{Value: duplicate}},
	}}
	deleter := &fakeLinuxSecretItemDeleter{errAt: 1, err: sentinel}
	originalServiceFactory := newLinuxSecretSearchService
	originalDeleteItem := deleteLinuxSecretSearchItem
	newLinuxSecretSearchService = func() (linuxSecretSearchService, error) { return service, nil }
	deleteLinuxSecretSearchItem = deleter.delete
	t.Cleanup(func() {
		newLinuxSecretSearchService = originalServiceFactory
		deleteLinuxSecretSearchItem = originalDeleteItem
	})

	err := NewOSSecretStore().Delete(context.Background(), "server-id")
	if !errors.Is(err, ErrSecretStoreUnavailable) || !service.closed {
		t.Fatalf("error = %v, closed = %v", err, service.closed)
	}
	assertZeroedSecretValue(t, first)
	assertZeroedSecretValue(t, duplicate)
}

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

func TestSecretFromSearchItemsClearsLockedAndSharedResultsOnSearchError(t *testing.T) {
	unlockedValue := gosecret.SecretValue("unlocked-secret")
	lockedValue := gosecret.SecretValue("locked-secret")
	sharedValue := gosecret.SecretValue("shared-secret")
	shared := &gosecret.Item{Secret: &gosecret.Secret{Value: sharedValue}}
	sentinel := errors.New("partial search failed")

	_, err := secretFromUnlockedItems(
		[]*gosecret.Item{{Secret: &gosecret.Secret{Value: unlockedValue}}, shared},
		[]*gosecret.Item{{Secret: &gosecret.Secret{Value: lockedValue}}, shared},
		sentinel,
	)
	if !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("error = %v", err)
	}
	assertZeroedSecretValue(t, unlockedValue)
	assertZeroedSecretValue(t, lockedValue)
	assertZeroedSecretValue(t, sharedValue)
}

func TestUniqueSecretSearchItemsDeduplicatesPointerIdentity(t *testing.T) {
	shared := &gosecret.Item{Secret: &gosecret.Secret{Value: gosecret.SecretValue("shared")}}
	other := &gosecret.Item{Secret: &gosecret.Secret{Value: gosecret.SecretValue("other")}}

	got := uniqueSecretSearchItems([]*gosecret.Item{shared, nil, shared}, []*gosecret.Item{shared, other, other})
	if len(got) != 2 || got[0] != shared || got[1] != other {
		t.Fatalf("unique items = %#v", got)
	}
}

func TestSecretSearchCleanupClearsLockedAndUnlockedOnOperationError(t *testing.T) {
	unlockedValue := gosecret.SecretValue("unlocked-secret")
	lockedValue := gosecret.SecretValue("locked-secret")
	sentinel := errors.New("operation failed")

	err := withSecretSearchItems(
		[]*gosecret.Item{{Secret: &gosecret.Secret{Value: unlockedValue}}},
		[]*gosecret.Item{{Secret: &gosecret.Secret{Value: lockedValue}}},
		func() error { return sentinel },
	)
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
	assertZeroedSecretValue(t, unlockedValue)
	assertZeroedSecretValue(t, lockedValue)
}

func TestOSSecretStoreDeleteClearsLockedAndSharedResultsOnDeleteError(t *testing.T) {
	unlockedValue := gosecret.SecretValue("unlocked-secret")
	lockedValue := gosecret.SecretValue("locked-secret")
	sharedValue := gosecret.SecretValue("shared-secret")
	shared := &gosecret.Item{Secret: &gosecret.Secret{Value: sharedValue}}
	sentinel := errors.New("delete failed")
	service := &fakeLinuxSecretSearchService{
		unlocked: []*gosecret.Item{{Secret: &gosecret.Secret{Value: unlockedValue}}, shared},
		locked:   []*gosecret.Item{{Secret: &gosecret.Secret{Value: lockedValue}}, shared},
	}
	deleter := &fakeLinuxSecretItemDeleter{errAt: 1, err: sentinel}
	originalServiceFactory := newLinuxSecretSearchService
	originalDeleteItem := deleteLinuxSecretSearchItem
	newLinuxSecretSearchService = func() (linuxSecretSearchService, error) { return service, nil }
	deleteLinuxSecretSearchItem = deleter.delete
	t.Cleanup(func() {
		newLinuxSecretSearchService = originalServiceFactory
		deleteLinuxSecretSearchItem = originalDeleteItem
	})

	err := NewOSSecretStore().Delete(context.Background(), "server-id")
	if !errors.Is(err, ErrSecretStoreUnavailable) {
		t.Fatalf("error = %v", err)
	}
	assertZeroedSecretValue(t, unlockedValue)
	assertZeroedSecretValue(t, lockedValue)
	assertZeroedSecretValue(t, sharedValue)
}

func assertZeroedSecretValue(t *testing.T, value gosecret.SecretValue) {
	t.Helper()
	for i, b := range value {
		if b != 0 {
			t.Fatalf("secret byte %d was not cleared", i)
		}
	}
}
