package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	toml "github.com/pelletier/go-toml/v2"
)

const ServerRegistryVersion = 1

var ErrServerRegistryDurability = errors.New("Mattermost server registry installed but durability could not be confirmed")

// ServerRegistrySaveError reports whether the replacement commit point was
// crossed before a save failed. Callers must not roll back related state when
// Committed returns true because the new registry is already visible.
type ServerRegistrySaveError struct {
	err       error
	committed bool
}

func (e *ServerRegistrySaveError) Error() string { return e.err.Error() }
func (e *ServerRegistrySaveError) Unwrap() error { return e.err }
func (e *ServerRegistrySaveError) Committed() bool {
	return e != nil && e.committed
}

func NewCommittedServerRegistryError(cause error) error {
	return &ServerRegistrySaveError{err: errors.Join(ErrServerRegistryDurability, cause), committed: true}
}

func RegistrySaveCommitted(err error) bool {
	var saveErr interface{ Committed() bool }
	return errors.As(err, &saveErr) && saveErr.Committed()
}

type registrySaveOperations struct {
	replace func(oldPath, newPath string) error
	syncDir func(path string) error
}

// ServerRegistry is the complete, strictly owned servers.toml document.
type ServerRegistry struct {
	Version int                `toml:"version"`
	Servers []MattermostServer `toml:"servers"`
}

// MattermostServer contains only non-secret server identity and user metadata.
// Authentication tokens are stored exclusively through SecretStore.
type MattermostServer struct {
	ID          string `toml:"id"`
	URL         string `toml:"url"`
	DisplayName string `toml:"display_name"`
	UserID      string `toml:"user_id"`
	Username    string `toml:"username"`
}

func NewServerRegistry() ServerRegistry {
	return ServerRegistry{Version: ServerRegistryVersion}
}

// LoadServerRegistry loads the strictly owned registry. Missing and empty
// files represent an empty version-1 registry.
func LoadServerRegistry(path string) (ServerRegistry, error) {
	if err := rejectServerRegistrySymlink(path); err != nil {
		return ServerRegistry{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return NewServerRegistry(), nil
		}
		return ServerRegistry{}, err
	}
	return LoadServerRegistryBytes(data)
}

func LoadServerRegistryBytes(data []byte) (ServerRegistry, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return NewServerRegistry(), nil
	}
	var registry ServerRegistry
	decoder := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields()
	if err := decoder.Decode(&registry); err != nil {
		var strictErr *toml.StrictMissingError
		if errors.As(err, &strictErr) {
			return ServerRegistry{}, fmt.Errorf("parse Mattermost server registry: %s", strictErr.String())
		}
		return ServerRegistry{}, fmt.Errorf("parse Mattermost server registry: %w", err)
	}
	if err := validateServerRegistry(registry); err != nil {
		return ServerRegistry{}, err
	}
	return registry, nil
}

// SaveServerRegistry validates and replaces servers.toml with atomic visibility
// through a same-directory temporary file. A returned error may report
// Committed() == true when replacement succeeded but durability confirmation
// failed. Existing secure permissions are preserved; otherwise the registry is
// restricted to 0600.
func SaveServerRegistry(path string, registry ServerRegistry) error {
	return saveServerRegistry(path, registry, registrySaveOperations{
		replace: replaceServerRegistryFile,
		syncDir: syncRegistryDirectory,
	})
}

func saveServerRegistry(path string, registry ServerRegistry, operations registrySaveOperations) error {
	if err := rejectServerRegistrySymlink(path); err != nil {
		return err
	}
	if err := validateServerRegistry(registry); err != nil {
		return err
	}
	data, err := toml.Marshal(registry)
	if err != nil {
		return fmt.Errorf("encode Mattermost server registry: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return err
	}
	mode := os.FileMode(0600)
	if info, statErr := os.Stat(path); statErr == nil && info.Mode().Perm()&0077 == 0 {
		mode = info.Mode().Perm()
	}

	file, err := os.CreateTemp(filepath.Dir(path), ".servers-*.toml.tmp")
	if err != nil {
		return err
	}
	temporaryPath := file.Name()
	defer os.Remove(temporaryPath)
	closeWithError := func(saveErr error) error {
		if closeErr := file.Close(); closeErr != nil {
			return errors.Join(saveErr, closeErr)
		}
		return saveErr
	}
	if _, err := file.Write(data); err != nil {
		return closeWithError(err)
	}
	if err := file.Chmod(mode); err != nil {
		return closeWithError(err)
	}
	if err := file.Sync(); err != nil {
		return closeWithError(err)
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := operations.replace(temporaryPath, path); err != nil {
		return &ServerRegistrySaveError{err: err}
	}
	if err := operations.syncDir(filepath.Dir(path)); err != nil {
		return NewCommittedServerRegistryError(err)
	}
	return nil
}

func rejectServerRegistrySymlink(path string) error {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("Mattermost server registry path %q must not be a symlink", path)
	}
	return nil
}

func validateServerRegistry(registry ServerRegistry) error {
	if registry.Version != ServerRegistryVersion {
		if registry.Version == 0 {
			return errors.New("Mattermost server registry version is missing")
		}
		return fmt.Errorf("unsupported Mattermost server registry version %d", registry.Version)
	}
	seen := make(map[string]struct{}, len(registry.Servers))
	for i, server := range registry.Servers {
		if strings.TrimSpace(server.ID) == "" {
			return fmt.Errorf("Mattermost server %d has an empty ID", i)
		}
		if _, duplicate := seen[server.ID]; duplicate {
			return fmt.Errorf("duplicate Mattermost server ID %q", server.ID)
		}
		seen[server.ID] = struct{}{}
		if strings.TrimSpace(server.URL) == "" {
			return fmt.Errorf("Mattermost server %q has an empty URL", server.ID)
		}
		if strings.TrimSpace(server.UserID) == "" {
			return fmt.Errorf("Mattermost server %q has an empty user ID", server.ID)
		}
	}
	return nil
}
