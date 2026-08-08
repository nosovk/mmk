//go:build darwin

package mattermost

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
)

func (s *OSSecretStore) Get(_ context.Context, serverID string) (string, error) {
	cmd := exec.Command("/usr/bin/security", "find-generic-password", "-w", "-s", SecretServiceName(), "-a", SecretAccountName(serverID))
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return "", ErrSecretNotFound
		}
		return "", secretStoreUnavailable(err)
	}
	return strings.TrimRight(out.String(), "\r\n"), nil
}

func (s *OSSecretStore) Set(_ context.Context, serverID, token string) error {
	cmd := exec.Command("/usr/bin/security", "add-generic-password", "-U", "-s", SecretServiceName(), "-a", SecretAccountName(serverID))
	cmd.Stdin = strings.NewReader(token + "\n")
	if err := cmd.Run(); err != nil {
		return secretStoreUnavailable(err)
	}
	return nil
}

func (s *OSSecretStore) Delete(_ context.Context, serverID string) error {
	cmd := exec.Command("/usr/bin/security", "delete-generic-password", "-s", SecretServiceName(), "-a", SecretAccountName(serverID))
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 44 {
			return nil
		}
		return secretStoreUnavailable(err)
	}
	return nil
}
