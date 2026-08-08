package mattermost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nosovk/mmk/internal/config"
)

var ErrSecretNotFound = errors.New("Mattermost credential not found")

// SecretStore persists Mattermost PATs outside the config file.
type SecretStore interface {
	Get(ctx context.Context, serverID string) (string, error)
	Set(ctx context.Context, serverID, token string) error
	Delete(ctx context.Context, serverID string) error
}

// ServerValidator is the authenticated subset needed during onboarding.
type ServerValidator interface {
	CurrentUser(ctx context.Context) (*User, error)
	TeamsForUser(ctx context.Context, userID string) ([]Team, error)
}

type ValidatorFactory func(canonicalRoot, token string) (ServerValidator, error)
type ConfigSaver func(config.Config) error

type AddServerInput struct {
	URL         string
	Token       string
	DisplayName string
}

// AddServer validates credentials and transactionally persists a server.
func AddServer(ctx context.Context, cfg *config.Config, input AddServerInput, newValidator ValidatorFactory, secrets SecretStore, save ConfigSaver) error {
	if cfg == nil || newValidator == nil || secrets == nil || save == nil {
		return errors.New("Mattermost server onboarding dependencies must not be nil")
	}
	root, err := CanonicalServerRoot(input.URL)
	if err != nil {
		return err
	}
	if strings.TrimSpace(input.Token) == "" {
		return errors.New("Mattermost token must not be empty")
	}

	validator, err := newValidator(root, input.Token)
	if err != nil {
		return redactError("create Mattermost client", err, input.Token)
	}
	user, err := validator.CurrentUser(ctx)
	if err != nil {
		return redactError("validate Mattermost user", err, input.Token)
	}
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return errors.New("Mattermost validation returned no authenticated user")
	}
	if _, err := validator.TeamsForUser(ctx, user.ID); err != nil {
		return redactError("validate Mattermost teams", err, input.Token)
	}

	id := ServerID(root)
	candidate := *cfg
	candidate.Servers = append([]config.MattermostServer(nil), cfg.Servers...)
	server := config.MattermostServer{
		ID:          id,
		URL:         root,
		DisplayName: strings.TrimSpace(input.DisplayName),
		UserID:      user.ID,
		Username:    user.Username,
	}
	serverIndex := -1
	for i := range candidate.Servers {
		if candidate.Servers[i].ID == id {
			serverIndex = i
			candidate.Servers[i] = server
			break
		}
	}
	if serverIndex < 0 {
		candidate.Servers = append(candidate.Servers, server)
	}

	var previousToken string
	hadPreviousToken := false
	if serverIndex >= 0 {
		previousToken, err = secrets.Get(ctx, id)
		if err == nil {
			hadPreviousToken = true
		} else if !errors.Is(err, ErrSecretNotFound) {
			return redactError("read previous Mattermost credential", err, input.Token)
		}
	}
	if err := secrets.Set(ctx, id, input.Token); err != nil {
		return redactError("store Mattermost credential", err, input.Token)
	}
	if err := save(candidate); err != nil {
		saveFailure := redactError("save Mattermost server config", err, input.Token)
		var rollbackErr error
		if hadPreviousToken {
			rollbackErr = secrets.Set(ctx, id, previousToken)
		} else {
			rollbackErr = secrets.Delete(ctx, id)
		}
		if rollbackErr != nil {
			return errors.Join(saveFailure, redactError("roll back Mattermost credential", rollbackErr, input.Token, previousToken))
		}
		return saveFailure
	}

	*cfg = candidate
	return nil
}

func NewValidator(canonicalRoot, token string) (ServerValidator, error) {
	client, err := NewClient(canonicalRoot, token)
	if err != nil {
		return nil, fmt.Errorf("create Mattermost client: %w", err)
	}
	return client, nil
}
