package mattermost

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/nosovk/mmk/internal/config"
)

var ErrSecretNotFound = errors.New("Mattermost credential not found")
var ErrConcurrentCredentialChange = errors.New("Mattermost credential changed concurrently")

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

// ConfigTransaction serializes credential/config changes and supplies the
// latest config document while the lock is held.
type ConfigTransaction interface {
	Lock(ctx context.Context) (unlock func() error, err error)
	Load(ctx context.Context) (config.Config, []byte, error)
	Save(ctx context.Context, document []byte) error
}

type AddServerInput struct {
	URL         string
	Token       string
	DisplayName string
}

// AddServerTransaction validates credentials, then serializes the credential
// and document update against all other processes using the same transaction.
func AddServerTransaction(ctx context.Context, input AddServerInput, newValidator ValidatorFactory, secrets SecretStore, transaction ConfigTransaction) (server config.MattermostServer, err error) {
	if newValidator == nil || secrets == nil || transaction == nil {
		return config.MattermostServer{}, errors.New("Mattermost server onboarding dependencies must not be nil")
	}
	root, user, err := validateServer(ctx, input, newValidator)
	if err != nil {
		return config.MattermostServer{}, err
	}

	unlock, err := transaction.Lock(ctx)
	if err != nil {
		return config.MattermostServer{}, fmt.Errorf("lock Mattermost onboarding transaction: %w", err)
	}
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			server = config.MattermostServer{}
			err = errors.Join(err, fmt.Errorf("unlock Mattermost onboarding transaction: %w", unlockErr))
		}
	}()

	cfg, document, err := transaction.Load(ctx)
	if err != nil {
		return config.MattermostServer{}, fmt.Errorf("load current config: %w", err)
	}
	server, previousToken, hadPreviousToken, err := prepareServerCredential(ctx, cfg, input, root, user, secrets)
	if err != nil {
		return config.MattermostServer{}, err
	}
	edited, err := config.EditMattermostServer(document, server)
	if err != nil {
		return config.MattermostServer{}, rollbackServerCredential(ctx, err, server.ID, input.Token, previousToken, hadPreviousToken, secrets)
	}
	if err := transaction.Save(ctx, edited); err != nil {
		return config.MattermostServer{}, rollbackServerCredential(ctx, err, server.ID, input.Token, previousToken, hadPreviousToken, secrets)
	}
	return server, nil
}

func validateServer(ctx context.Context, input AddServerInput, newValidator ValidatorFactory) (string, *User, error) {
	root, err := CanonicalServerRoot(input.URL)
	if err != nil {
		return "", nil, err
	}
	if strings.TrimSpace(input.Token) == "" {
		return "", nil, errors.New("Mattermost token must not be empty")
	}
	validator, err := newValidator(root, input.Token)
	if err != nil {
		return "", nil, redactError("create Mattermost client", err, input.Token)
	}
	user, err := validator.CurrentUser(ctx)
	if err != nil {
		return "", nil, redactError("validate Mattermost user", err, input.Token)
	}
	if user == nil || strings.TrimSpace(user.ID) == "" {
		return "", nil, errors.New("Mattermost validation returned no authenticated user")
	}
	if _, err := validator.TeamsForUser(ctx, user.ID); err != nil {
		return "", nil, redactError("validate Mattermost teams", err, input.Token)
	}
	return root, user, nil
}

func prepareServerCredential(ctx context.Context, cfg config.Config, input AddServerInput, root string, user *User, secrets SecretStore) (config.MattermostServer, string, bool, error) {
	server := config.MattermostServer{ID: ServerID(root), URL: root, DisplayName: strings.TrimSpace(input.DisplayName), UserID: user.ID, Username: user.Username}
	exists := false
	for _, configured := range cfg.Servers {
		if configured.ID == server.ID {
			exists = true
			break
		}
	}
	var previousToken string
	hadPreviousToken := false
	if exists {
		var err error
		previousToken, err = secrets.Get(ctx, server.ID)
		if err == nil {
			hadPreviousToken = true
		} else if !errors.Is(err, ErrSecretNotFound) {
			return config.MattermostServer{}, "", false, redactError("read previous Mattermost credential", err, input.Token)
		}
	}
	if err := secrets.Set(ctx, server.ID, input.Token); err != nil {
		return config.MattermostServer{}, "", false, redactError("store Mattermost credential", err, input.Token)
	}
	return server, previousToken, hadPreviousToken, nil
}

func rollbackServerCredential(ctx context.Context, saveErr error, serverID, writtenToken, previousToken string, hadPreviousToken bool, secrets SecretStore) error {
	saveFailure := redactError("save Mattermost server config", saveErr, writtenToken, previousToken)
	current, err := secrets.Get(ctx, serverID)
	if errors.Is(err, ErrSecretNotFound) {
		return errors.Join(saveFailure, ErrConcurrentCredentialChange)
	}
	if err != nil {
		return errors.Join(saveFailure, redactError("check Mattermost credential before rollback", err, writtenToken, previousToken))
	}
	if current != writtenToken {
		return errors.Join(saveFailure, ErrConcurrentCredentialChange)
	}
	if hadPreviousToken {
		err = secrets.Set(ctx, serverID, previousToken)
	} else {
		err = secrets.Delete(ctx, serverID)
	}
	if err != nil {
		return errors.Join(saveFailure, redactError("roll back Mattermost credential", err, writtenToken, previousToken))
	}
	return saveFailure
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
		return rollbackServerCredential(ctx, err, id, input.Token, previousToken, hadPreviousToken, secrets)
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
