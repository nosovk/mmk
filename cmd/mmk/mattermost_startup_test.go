package main

import (
	"context"
	"database/sql"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
)

func TestStartupModeSelectsMattermostForAnyRegisteredServer(t *testing.T) {
	if startupMode(config.NewServerRegistry()) != startupSlack {
		t.Fatal("empty registry must preserve Slack fallback")
	}
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{{ID: "s1"}}
	if startupMode(registry) != startupMattermost {
		t.Fatal("non-empty registry must select Mattermost")
	}
}

func TestMattermostStartupHydratesCacheBeforeBlockedLiveAndIsolatesFailure(t *testing.T) {
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{
		{ID: "s1", URL: "https://one.example", DisplayName: "Same", UserID: "u1"},
		{ID: "s2", URL: "https://two.example", DisplayName: "Same", UserID: "u2"},
	}
	cacheStore := &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{
		"s1": cachedMattermostSnapshot("s1", "u1", "cached"),
	}}
	barrier := make(chan struct{})
	messages := make(chan tea.Msg, 16)
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry: registry,
		Secrets:  fakeMattermostSecrets{tokens: map[string]string{"s1": "pat-one"}, errs: map[string]error{"s2": errors.New("secret pat-two denied")}},
		NewClient: func(server mattermost.Server, token string) (service.ServerBootstrapClient, error) {
			if token != "pat-one" {
				t.Fatalf("unexpected token %q", token)
			}
			return blockingBootstrapClient{barrier: barrier, serverID: server.ID}, nil
		},
		Cache: cacheStore,
		Send:  func(msg tea.Msg) { messages <- msg },
		Clock: func() time.Time { return time.UnixMilli(99) },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(barrier); startup.Cancel(); startup.Wait() }()

	first := <-messages
	placeholders, ok := first.(mattermostRailMsg)
	if !ok || len(placeholders.Items) != 2 || placeholders.Items[0].ID != "s1" || placeholders.Items[1].ID != "s2" {
		t.Fatalf("first message = %#v", first)
	}
	second := <-messages
	ready, ok := second.(ui.ServerReadyMsg)
	if !ok || ready.Server.ServerID != "s1" || ready.Server.Channels[0].Name != "cached" {
		t.Fatalf("cache message before live = %#v", second)
	}

	var errorMsg ui.ServerStateMsg
	deadline := time.After(time.Second)
	for errorMsg.ServerID == "" {
		select {
		case msg := <-messages:
			if state, ok := msg.(ui.ServerStateMsg); ok && state.ServerID == "s2" {
				errorMsg = state
			}
		case <-deadline:
			t.Fatal("timed out waiting for independent s2 failure")
		}
	}
	if errorMsg.Err == nil || contains(errorMsg.Err.Error(), "pat-two") {
		t.Fatalf("error leaked secret: %v", errorMsg.Err)
	}
}

type fakeMattermostSnapshotStore struct {
	mu    sync.Mutex
	loads map[string]cache.MattermostBootstrapSnapshot
	saves []cache.MattermostBootstrapSnapshot
}

func (f *fakeMattermostSnapshotStore) LoadMattermostBootstrapSnapshot(id string) (cache.MattermostBootstrapSnapshot, error) {
	if snapshot, ok := f.loads[id]; ok {
		return snapshot, nil
	}
	return cache.MattermostBootstrapSnapshot{}, sql.ErrNoRows
}

func (f *fakeMattermostSnapshotStore) ApplyMattermostBootstrapSnapshot(snapshot cache.MattermostBootstrapSnapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.saves = append(f.saves, snapshot)
	return nil
}

type fakeMattermostSecrets struct {
	tokens map[string]string
	errs   map[string]error
}

func (f fakeMattermostSecrets) Get(_ context.Context, id string) (string, error) {
	return f.tokens[id], f.errs[id]
}

type blockingBootstrapClient struct {
	barrier  <-chan struct{}
	serverID string
}

func (b blockingBootstrapClient) CurrentUser(ctx context.Context) (*mattermost.User, error) {
	select {
	case <-b.barrier:
		return &mattermost.User{ID: "u1"}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (blockingBootstrapClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return nil, nil
}
func (blockingBootstrapClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return nil, nil
}
func (blockingBootstrapClient) ChannelMembershipsForUser(context.Context, string, string) ([]mattermost.ChannelMembership, error) {
	return nil, nil
}
func (blockingBootstrapClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}
func (blockingBootstrapClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}

func cachedMattermostSnapshot(serverID, userID, channelName string) cache.MattermostBootstrapSnapshot {
	return cache.MattermostBootstrapSnapshot{
		Server:      cache.MattermostServer{ID: serverID, Name: serverID, URL: "https://example", CurrentUserID: userID},
		CurrentUser: cache.MattermostUser{ID: userID},
		Teams:       []cache.MattermostTeam{{ID: "t1", DisplayName: "Team"}},
		Channels:    []cache.MattermostChannel{{ID: "c1", TeamID: "t1", Name: channelName, Kind: "public"}},
	}
}

func contains(value, substring string) bool {
	for i := 0; i+len(substring) <= len(value); i++ {
		if value[i:i+len(substring)] == substring {
			return true
		}
	}
	return false
}
