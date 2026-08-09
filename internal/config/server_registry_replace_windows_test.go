//go:build windows

package config

import (
	"errors"
	"testing"

	"golang.org/x/sys/windows"
)

type fakeRegistryWindowsFileAPI struct {
	from  string
	to    string
	flags uint32
	err   error
}

func (f *fakeRegistryWindowsFileAPI) moveFileEx(from, to *uint16, flags uint32) error {
	f.from = windows.UTF16PtrToString(from)
	f.to = windows.UTF16PtrToString(to)
	f.flags = flags
	return f.err
}

func TestReplaceServerRegistryFileWindowsUsesReplaceAndWriteThrough(t *testing.T) {
	api := &fakeRegistryWindowsFileAPI{}
	if err := replaceServerRegistryFileWindows(`C:\config\.servers.tmp`, `C:\config\servers.toml`, api); err != nil {
		t.Fatal(err)
	}
	if api.from != `C:\config\.servers.tmp` || api.to != `C:\config\servers.toml` {
		t.Fatalf("paths = %q -> %q", api.from, api.to)
	}
	wantFlags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if api.flags != wantFlags {
		t.Fatalf("flags = %#x, want %#x", api.flags, wantFlags)
	}
}

func TestReplaceServerRegistryFileWindowsPreservesNativeError(t *testing.T) {
	sentinel := errors.New("MoveFileExW failed")
	err := replaceServerRegistryFileWindows(`C:\from`, `C:\to`, &fakeRegistryWindowsFileAPI{err: sentinel})
	if !errors.Is(err, sentinel) {
		t.Fatalf("error = %v", err)
	}
}
