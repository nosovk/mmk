//go:build windows

package config

import (
	"fmt"

	"golang.org/x/sys/windows"
)

type registryWindowsFileAPI interface {
	moveFileEx(from, to *uint16, flags uint32) error
}

type nativeRegistryWindowsFileAPI struct{}

func (nativeRegistryWindowsFileAPI) moveFileEx(from, to *uint16, flags uint32) error {
	return windows.MoveFileEx(from, to, flags)
}

func replaceServerRegistryFile(oldPath, newPath string) error {
	return replaceServerRegistryFileWindows(oldPath, newPath, nativeRegistryWindowsFileAPI{})
}

func replaceServerRegistryFileWindows(oldPath, newPath string, api registryWindowsFileAPI) error {
	oldPathPtr, err := windows.UTF16PtrFromString(oldPath)
	if err != nil {
		return err
	}
	newPathPtr, err := windows.UTF16PtrFromString(newPath)
	if err != nil {
		return err
	}
	flags := uint32(windows.MOVEFILE_REPLACE_EXISTING | windows.MOVEFILE_WRITE_THROUGH)
	if err := api.moveFileEx(oldPathPtr, newPathPtr, flags); err != nil {
		return fmt.Errorf("replace Mattermost server registry with MoveFileExW: %w", err)
	}
	return nil
}

// MoveFileExW with WRITE_THROUGH requests replacement durability from Windows;
// there is no portable directory handle sync equivalent to perform afterward.
func syncRegistryDirectory(string) error { return nil }
