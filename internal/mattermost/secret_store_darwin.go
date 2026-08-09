//go:build darwin && cgo

package mattermost

/*
Security.framework provides the Keychain Services API used below.
#cgo LDFLAGS: -framework Security -framework CoreFoundation
#include <CoreFoundation/CoreFoundation.h>
#include <Security/Security.h>
#include <stdlib.h>
#include <string.h>

static void mmk_zero_free(void *buffer, size_t length) {
	if (buffer != NULL) {
		memset(buffer, 0, length);
		free(buffer);
	}
}

static void mmk_release_keychain_item(SecKeychainItemRef item) {
	if (item != NULL) {
		CFRelease(item);
	}
}

static void mmk_zero_free_keychain_content(void *buffer, UInt32 length) {
	if (buffer != NULL) {
		memset(buffer, 0, length);
		SecKeychainItemFreeContent(NULL, buffer);
	}
}
*/
import "C"

import (
	"context"
	"errors"
	"fmt"
	"unsafe"
)

const errSecItemNotFound = C.OSStatus(-25300)

type nativeDarwinKeychainAPI struct{}

func (nativeDarwinKeychainAPI) find(service, account string) ([]byte, darwinKeychainItem, error) {
	serviceBytes := []byte(service)
	accountBytes := []byte(account)
	servicePtr := C.CBytes(serviceBytes)
	accountPtr := C.CBytes(accountBytes)
	defer C.free(servicePtr)
	defer C.free(accountPtr)

	var secretLength C.UInt32
	var secretPtr unsafe.Pointer
	var item C.SecKeychainItemRef
	status := C.SecKeychainFindGenericPassword(
		nil,
		C.UInt32(len(serviceBytes)), servicePtr,
		C.UInt32(len(accountBytes)), accountPtr,
		&secretLength, &secretPtr, &item,
	)
	if status == errSecItemNotFound {
		return nil, 0, errDarwinKeychainNotFound
	}
	if status != C.errSecSuccess {
		return nil, 0, darwinOSStatusError("SecKeychainFindGenericPassword", status)
	}
	defer C.mmk_zero_free_keychain_content(secretPtr, secretLength)
	secret := C.GoBytes(secretPtr, C.int(secretLength))
	return secret, darwinKeychainItem(uintptr(unsafe.Pointer(item))), nil
}

func (nativeDarwinKeychainAPI) add(service, account string, secret []byte) error {
	serviceBytes := []byte(service)
	accountBytes := []byte(account)
	servicePtr := C.CBytes(serviceBytes)
	accountPtr := C.CBytes(accountBytes)
	secretPtr := C.CBytes(secret)
	defer C.free(servicePtr)
	defer C.free(accountPtr)
	defer C.mmk_zero_free(secretPtr, C.size_t(len(secret)))

	status := C.SecKeychainAddGenericPassword(
		nil,
		C.UInt32(len(serviceBytes)), servicePtr,
		C.UInt32(len(accountBytes)), accountPtr,
		C.UInt32(len(secret)), secretPtr,
		nil,
	)
	if status != C.errSecSuccess {
		return darwinOSStatusError("SecKeychainAddGenericPassword", status)
	}
	return nil
}

func (nativeDarwinKeychainAPI) update(item darwinKeychainItem, secret []byte) error {
	secretPtr := C.CBytes(secret)
	defer C.mmk_zero_free(secretPtr, C.size_t(len(secret)))
	status := C.SecKeychainItemModifyAttributesAndData(
		C.SecKeychainItemRef(unsafe.Pointer(uintptr(item))),
		nil,
		C.UInt32(len(secret)), secretPtr,
	)
	if status != C.errSecSuccess {
		return darwinOSStatusError("SecKeychainItemModifyAttributesAndData", status)
	}
	return nil
}

func (nativeDarwinKeychainAPI) delete(item darwinKeychainItem) error {
	status := C.SecKeychainItemDelete(C.SecKeychainItemRef(unsafe.Pointer(uintptr(item))))
	if status == errSecItemNotFound {
		return errDarwinKeychainNotFound
	}
	if status != C.errSecSuccess {
		return darwinOSStatusError("SecKeychainItemDelete", status)
	}
	return nil
}

func (nativeDarwinKeychainAPI) release(item darwinKeychainItem) {
	C.mmk_release_keychain_item(C.SecKeychainItemRef(unsafe.Pointer(uintptr(item))))
}

func darwinOSStatusError(operation string, status C.OSStatus) error {
	return fmt.Errorf("%s failed with OSStatus %d", operation, int32(status))
}

func (s *OSSecretStore) Get(_ context.Context, serverID string) (string, error) {
	return darwinKeychainStore{api: nativeDarwinKeychainAPI{}}.get(serverID)
}

func (s *OSSecretStore) Set(_ context.Context, serverID, token string) error {
	return darwinKeychainStore{api: nativeDarwinKeychainAPI{}}.set(serverID, token)
}

func (s *OSSecretStore) Delete(_ context.Context, serverID string) error {
	err := darwinKeychainStore{api: nativeDarwinKeychainAPI{}}.delete(serverID)
	if errors.Is(err, errDarwinKeychainNotFound) {
		return nil
	}
	return err
}
