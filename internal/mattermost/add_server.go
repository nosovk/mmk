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
var ErrOnboardingTransactionCommitted = errors.New("Mattermost onboarding transaction committed")

// OnboardingTransactionError indicates that server metadata and credentials
// were committed even though a later transaction step returned an error.
type OnboardingTransactionError struct {
	err error
}

func (e *OnboardingTransactionError) Error() string { return e.err.Error() }
func (e *OnboardingTransactionError) Unwrap() error { return e.err }
func (e *OnboardingTransactionError) Committed() bool {
	return e != nil
}

func NewCommittedOnboardingTransactionError(cause error) error {
	return &OnboardingTransactionError{err: errors.Join(ErrOnboardingTransactionCommitted, cause)}
}

func OnboardingTransactionCommitted(err error) bool {
	var transactionErr *OnboardingTransactionError
	return errors.As(err, &transactionErr) && transactionErr.Committed()
}

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

// ServerRegistryTransaction serializes credential/registry changes and
// supplies the latest registry while the lock is held.
type ServerRegistryTransaction interface {
	Lock(ctx context.Context) (unlock func() error, err error)
	Load(ctx context.Context) (config.ServerRegistry, error)
	Save(ctx context.Context, registry config.ServerRegistry) error
}

type AddServerInput struct {
	URL         string
	Token       string
	DisplayName string
}

// AddServerTransaction validates credentials, then serializes the credential
// and registry update against all other processes using the same transaction.
func AddServerTransaction(ctx context.Context, input AddServerInput, newValidator ValidatorFactory, secrets SecretStore, transaction ServerRegistryTransaction) (server config.MattermostServer, err error) {
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
	committed := false
	defer func() {
		if unlockErr := unlock(); unlockErr != nil {
			unlockFailure := fmt.Errorf("unlock Mattermost onboarding transaction: %w", unlockErr)
			if committed {
				err = errors.Join(err, NewCommittedOnboardingTransactionError(unlockFailure))
				return
			}
			server = config.MattermostServer{}
			err = errors.Join(err, unlockFailure)
		}
	}()

	registry, err := transaction.Load(ctx)
	if err != nil {
		return config.MattermostServer{}, fmt.Errorf("load current Mattermost server registry: %w", err)
	}
	server, previousToken, hadPreviousToken, err := prepareServerCredential(ctx, input, root, user, secrets)
	if err != nil {
		return config.MattermostServer{}, err
	}
	updated := registry
	updated.Servers = append([]config.MattermostServer(nil), registry.Servers...)
	serverIndex := -1
	for i := range updated.Servers {
		if updated.Servers[i].ID == server.ID {
			serverIndex = i
			updated.Servers[i] = server
			break
		}
	}
	if serverIndex < 0 {
		updated.Servers = append(updated.Servers, server)
	}
	if err := transaction.Save(ctx, updated); err != nil {
		if config.RegistrySaveCommitted(err) {
			committed = true
			return server, NewCommittedOnboardingTransactionError(redactError("confirm Mattermost server registry durability", err, input.Token, previousToken))
		}
		return config.MattermostServer{}, rollbackServerCredential(ctx, err, server.ID, input.Token, previousToken, hadPreviousToken, secrets)
	}
	committed = true
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

func prepareServerCredential(ctx context.Context, input AddServerInput, root string, user *User, secrets SecretStore) (config.MattermostServer, string, bool, error) {
	server := config.MattermostServer{ID: ServerID(root), URL: root, DisplayName: strings.TrimSpace(input.DisplayName), UserID: user.ID, Username: user.Username}
	previousToken, err := secrets.Get(ctx, server.ID)
	hadPreviousToken := err == nil
	if err != nil && !errors.Is(err, ErrSecretNotFound) {
		return config.MattermostServer{}, "", false, redactError("read previous Mattermost credential", err, input.Token)
	}
	if err := secrets.Set(ctx, server.ID, input.Token); err != nil {
		return config.MattermostServer{}, "", false, redactError("store Mattermost credential", err, input.Token)
	}
	return server, previousToken, hadPreviousToken, nil
}

func rollbackServerCredential(ctx context.Context, saveErr error, serverID, writtenToken, previousToken string, hadPreviousToken bool, secrets SecretStore) error {
	saveFailure := redactError("save Mattermost server registry", saveErr, writtenToken, previousToken)
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

func NewValidator(canonicalRoot, token string) (ServerValidator, error) {
	client, err := NewClient(canonicalRoot, token)
	if err != nil {
		return nil, fmt.Errorf("create Mattermost client: %w", err)
	}
	return client, nil
}
