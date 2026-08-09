//go:build !windows

package config

import "os"

func replaceServerRegistryFile(oldPath, newPath string) error {
	return os.Rename(oldPath, newPath)
}

func syncRegistryDirectory(path string) error {
	directory, err := os.Open(path)
	if err != nil {
		return err
	}
	defer directory.Close()
	return directory.Sync()
}
