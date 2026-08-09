//go:build windows

package mattermost

import (
	"context"
	"errors"
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	credentialTypeGeneric         = 1
	credentialPersistLocalMachine = 2
	maxCredentialBlobBytes        = 5 * 512
)

type windowsCredential struct {
	Flags              uint32
	Type               uint32
	TargetName         *uint16
	Comment            *uint16
	LastWritten        windows.Filetime
	CredentialBlobSize uint32
	CredentialBlob     *byte
	Persist            uint32
	AttributeCount     uint32
	Attributes         uintptr
	TargetAlias        *uint16
	UserName           *uint16
}

type windowsCredentialAPI interface {
	read(target string) ([]byte, error)
	write(target, username string, value []byte) error
	delete(target string) error
}

type windowsCredentialStore struct {
	api windowsCredentialAPI
}

func (s windowsCredentialStore) get(serverID string) (string, error) {
	value, err := s.api.read(WindowsCredentialTargetName(serverID))
	defer clear(value)
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return "", ErrSecretNotFound
	}
	if err != nil {
		return "", windowsCredentialError("read Windows credential", err)
	}
	return string(value), nil
}

func (s windowsCredentialStore) set(serverID, token string) error {
	if len(token) > maxCredentialBlobBytes {
		return fmt.Errorf("Mattermost token exceeds Windows Credential Manager's %d-byte limit", maxCredentialBlobBytes)
	}
	err := withSecretBytes(token, func(value []byte) error {
		return s.api.write(WindowsCredentialTargetName(serverID), SecretAccountName(serverID), value)
	})
	if err != nil {
		return windowsCredentialError("write Windows credential", err, token)
	}
	return nil
}

func (s windowsCredentialStore) delete(serverID string) error {
	err := s.api.delete(WindowsCredentialTargetName(serverID))
	if errors.Is(err, windows.ERROR_NOT_FOUND) {
		return nil
	}
	if err != nil {
		return windowsCredentialError("delete Windows credential", err)
	}
	return nil
}

func windowsCredentialError(operation string, err error, secrets ...string) error {
	message := err.Error()
	for _, secret := range secrets {
		message = redactSecret(message, secret)
	}
	unavailable := secretStoreUnavailable(errors.New(message))
	return errors.Join(unavailable, redactError(operation, err, secrets...))
}

func (s *OSSecretStore) Get(_ context.Context, serverID string) (string, error) {
	return windowsCredentialStore{api: nativeWindowsCredentialAPI{}}.get(serverID)
}

func (s *OSSecretStore) Set(_ context.Context, serverID, token string) error {
	return windowsCredentialStore{api: nativeWindowsCredentialAPI{}}.set(serverID, token)
}

func (s *OSSecretStore) Delete(_ context.Context, serverID string) error {
	return windowsCredentialStore{api: nativeWindowsCredentialAPI{}}.delete(serverID)
}

var (
	advapi32        = windows.NewLazySystemDLL("advapi32.dll")
	procCredReadW   = advapi32.NewProc("CredReadW")
	procCredWriteW  = advapi32.NewProc("CredWriteW")
	procCredDeleteW = advapi32.NewProc("CredDeleteW")
	procCredFree    = advapi32.NewProc("CredFree")
)

type nativeWindowsCredentialAPI struct{}

func (nativeWindowsCredentialAPI) read(target string) ([]byte, error) {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return nil, err
	}
	var credential *windowsCredential
	r1, _, callErr := procCredReadW.Call(
		uintptr(unsafe.Pointer(targetPtr)),
		credentialTypeGeneric,
		0,
		uintptr(unsafe.Pointer(&credential)),
	)
	if r1 == 0 {
		return nil, syscallFailure(callErr)
	}
	defer procCredFree.Call(uintptr(unsafe.Pointer(credential)))
	if credential.CredentialBlobSize == 0 {
		return []byte{}, nil
	}
	return copyAndClearCredentialBlob(unsafe.Slice(credential.CredentialBlob, credential.CredentialBlobSize)), nil
}

func copyAndClearCredentialBlob(blob []byte) []byte {
	owned := append([]byte(nil), blob...)
	clear(blob)
	return owned
}

func (nativeWindowsCredentialAPI) write(target, username string, value []byte) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	usernamePtr, err := windows.UTF16PtrFromString(username)
	if err != nil {
		return err
	}
	credential := windowsCredential{
		Type:               credentialTypeGeneric,
		TargetName:         targetPtr,
		CredentialBlobSize: uint32(len(value)),
		Persist:            credentialPersistLocalMachine,
		UserName:           usernamePtr,
	}
	if len(value) > 0 {
		credential.CredentialBlob = &value[0]
	}
	r1, _, callErr := procCredWriteW.Call(uintptr(unsafe.Pointer(&credential)), 0)
	if r1 == 0 {
		return syscallFailure(callErr)
	}
	return nil
}

func (nativeWindowsCredentialAPI) delete(target string) error {
	targetPtr, err := windows.UTF16PtrFromString(target)
	if err != nil {
		return err
	}
	r1, _, callErr := procCredDeleteW.Call(uintptr(unsafe.Pointer(targetPtr)), credentialTypeGeneric, 0)
	if r1 == 0 {
		return syscallFailure(callErr)
	}
	return nil
}

func syscallFailure(err error) error {
	if err == nil || errors.Is(err, windows.ERROR_SUCCESS) {
		return errors.New("Windows Credential Manager call failed")
	}
	return err
}
