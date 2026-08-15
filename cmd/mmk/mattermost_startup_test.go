package main

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/workspace"
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
		NewClient: func(server mattermost.Server, token string) (mattermostStartupClient, error) {
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

func TestMattermostStartupClaimsFirstUsableCachedServerInRegistryOrder(t *testing.T) {
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{
		{ID: "s1", URL: "https://one.example", UserID: "u1"},
		{ID: "s2", URL: "https://two.example", UserID: "u2"},
		{ID: "s3", URL: "https://three.example", UserID: "u3"},
	}
	messages := make(chan tea.Msg, 16)
	blocked := make(chan struct{})
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry: registry,
		Secrets:  blockingMattermostSecrets{release: blocked},
		NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) {
			return nil, errors.New("unused")
		},
		Cache: &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{
			"s2": cachedMattermostSnapshot("s2", "u2", "second"),
			"s3": cachedMattermostSnapshot("s3", "u3", "third"),
		}},
		Send: func(msg tea.Msg) { messages <- msg },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { close(blocked); startup.Cancel(); startup.Wait() }()
	<-messages
	var ready []ui.ServerReadyMsg
	for len(ready) < 2 {
		if msg, ok := (<-messages).(ui.ServerReadyMsg); ok {
			ready = append(ready, msg)
		}
	}
	if !ready[0].Server.InitialActive || ready[0].Server.ServerID != "s2" {
		t.Fatalf("first usable cache = %#v", ready[0].Server)
	}
	if ready[1].Server.InitialActive {
		t.Fatalf("later cache stole activation: %#v", ready[1].Server)
	}
}

func TestMattermostStartupLiveClaimIsExactlyOnceAndLatePreferredServerDoesNotSteal(t *testing.T) {
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{
		{ID: "s1", URL: "https://one.example", UserID: "u1"},
		{ID: "s2", URL: "https://two.example", UserID: "u2"},
	}
	gates := map[string]chan struct{}{"s1": make(chan struct{}), "s2": make(chan struct{})}
	messages := make(chan tea.Msg, 32)
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry: registry, Secrets: fakeMattermostSecrets{tokens: map[string]string{"s1": "one", "s2": "two"}},
		NewClient: func(server mattermost.Server, _ string) (mattermostStartupClient, error) {
			return fixedBlockingBootstrapClient{release: gates[server.ID], userID: server.UserID}, nil
		},
		Cache: &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{}},
		Send:  func(msg tea.Msg) { messages <- msg },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { startup.Cancel(); startup.Wait() }()
	<-messages
	close(gates["s2"])
	first := nextServerReady(t, messages)
	if first.Server.ServerID != "s2" || !first.Server.InitialActive {
		t.Fatalf("first live ready = %#v", first.Server)
	}
	close(gates["s1"])
	second := nextServerReady(t, messages)
	if second.Server.ServerID != "s1" || second.Server.InitialActive {
		t.Fatalf("late preferred server = %#v", second.Server)
	}
}

func TestMattermostStartupRetainsHistoryCapableClientPerServer(t *testing.T) {
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{{ID: "s1", URL: "https://one.example", UserID: "u1"}}
	client := fixedBlockingBootstrapClient{release: closedChannel(), userID: "u1"}
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry:  registry,
		Secrets:   fakeMattermostSecrets{tokens: map[string]string{"s1": "secret"}},
		NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) { return client, nil },
		Cache:     &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{}},
		Send:      func(tea.Msg) {},
	})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(time.Second)
	for {
		startup.mu.RLock()
		retained := startup.contexts["s1"].client
		startup.mu.RUnlock()
		if retained != nil {
			break
		}
		select {
		case <-deadline:
			t.Fatal("timed out waiting for retained client")
		default:
		}
	}
	startup.Cancel()
	startup.Wait()
	startup.mu.RLock()
	retained := startup.contexts["s1"].client
	startup.mu.RUnlock()
	if retained == nil {
		t.Fatal("history-capable client was not retained")
	}
}

func TestApplyPendingMattermostMessagesQueuesCachedHistoryLifecycleForInit(t *testing.T) {
	db := setupMattermostHistoryStartupDB(t)
	app := ui.NewApp()
	startup := &mattermostStartup{contexts: map[ids.ServerID]mattermostServerContext{"s1": {server: mattermost.Server{ID: "s1"}}}}
	app.SetMattermostHistoryService(mattermostUIHistoryService{ctx: context.Background(), startup: startup, cache: db})
	state := mattermostServerViewState(service.ServerSnapshot{
		Server: mattermost.Server{ID: "s1", Name: "One"}, CurrentUser: mattermost.User{ID: "u1"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{Channel: mattermost.Channel{ID: "c1", Kind: mattermost.ChannelKindPublic}, DisplayName: "Town Square"}}}},
	}, true, 1)
	applyPendingMattermostMessages(app, []tea.Msg{mattermostRailMsg{Items: []workspace.WorkspaceItem{{ID: "s1", Name: "One"}}}, ui.ServerReadyMsg{Server: state}})
	if got := app.ActiveChannelID(); got != "c1" {
		t.Fatalf("active channel=%q", got)
	}
	_, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := app.View().Content; !strings.Contains(view, "cached") {
		t.Fatalf("cached message absent from view: %q", view)
	}
	if cmd := app.Init(); cmd == nil {
		t.Fatal("live verification command was not queued for program Init")
	}
}

func TestApplyPendingMattermostMessagesAppliesConnectionStatesThroughAppUpdate(t *testing.T) {
	app := ui.NewApp()
	items := []workspace.WorkspaceItem{
		{ID: "s1", Name: "One", Initials: "ON", State: workspace.ItemStateReady},
		{ID: "s2", Name: "Two", Initials: "TW", State: workspace.ItemStateReady},
	}
	applyPendingMattermostMessages(app, []tea.Msg{
		mattermostRailMsg{Items: items},
		ui.ServerReadyMsg{Server: ui.ServerViewState{ServerID: "s1"}},
		ui.ServerReadyMsg{Server: ui.ServerViewState{ServerID: "s2"}},
		ui.ServerConnectionStateMsg{ServerID: "s1", State: workspace.ItemStateOffline},
		ui.ServerConnectionStateMsg{ServerID: "s1", State: workspace.ItemStateReconnecting},
		ui.ServerConnectionStateMsg{ServerID: "s1", State: workspace.ItemStateReady},
		ui.ServerConnectionStateMsg{ServerID: "s2", State: workspace.ItemStateOffline},
	})
	_, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := ansi.Strip(app.View().Content)
	if !strings.Contains(view, "ON+") || !strings.Contains(view, "TW-") {
		t.Fatalf("pending connection states not scoped/finalized in rail: %q", view)
	}
}

func TestPendingConnectionStatesDoNotMakeCachedServerUnusable(t *testing.T) {
	startup := reconciliationStartup(reconciliationBootstrapClient(), service.ServerSnapshot{
		Server: mattermost.Server{ID: "s1", Name: "One"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{
			Channel: mattermost.Channel{ID: "c1", Kind: mattermost.ChannelKindPublic},
		}}}},
	})
	app := ui.NewApp()
	applyPendingMattermostMessages(app, []tea.Msg{
		mattermostRailMsg{Items: []workspace.WorkspaceItem{{ID: "s1", Name: "One", Initials: "ON"}}},
		ui.ServerReadyMsg{Server: startup.viewState("s1")},
		ui.ServerConnectionStateMsg{ServerID: "s1", State: workspace.ItemStateOffline},
	})
	if msg := startup.switchMsg("s1"); msg == nil {
		t.Fatal("offline cached server became unswitchable")
	}
}

func setupMattermostHistoryStartupDB(t *testing.T) *cache.DB {
	t.Helper()
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertMattermostServer(cache.MattermostServer{ID: "s1", URL: "https://one.example", CurrentUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostTeam("s1", cache.MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", cache.MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", cache.MattermostPost{ID: "cached", ChannelID: "c1", Text: "cached", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}
	return db
}

func closedChannel() chan struct{} { ch := make(chan struct{}); close(ch); return ch }

func TestMattermostStartupSwitchAndShutdownAreBoundedForUnusableServer(t *testing.T) {
	release := make(chan struct{})
	messages := make(chan tea.Msg, 8)
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{{ID: "s1", URL: "https://one.example", UserID: "u1"}}
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry: registry, Secrets: blockingMattermostSecrets{release: release},
		NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) {
			return nil, errors.New("unused")
		},
		Cache: &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{}},
		Send:  func(msg tea.Msg) { messages <- msg },
	})
	if err != nil {
		t.Fatal(err)
	}
	if msg := startup.switchMsg("s1"); msg != nil {
		t.Fatalf("unusable switch emitted %#v", msg)
	}
	startup.Cancel()
	waitCtx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := startup.WaitContext(waitCtx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("WaitContext error = %v", err)
	}
	close(release)
	if err := startup.WaitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
}

func TestMattermostStartupDoesNotSendAfterCancellationReleasesBlockedSecretStore(t *testing.T) {
	release := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())
	messages := make(chan tea.Msg, 8)
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{{ID: "s1", URL: "https://one.example", UserID: "u1"}}
	startup, err := startMattermost(ctx, mattermostStartupDeps{
		Registry: registry, Secrets: blockingMattermostSecrets{release: release},
		NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) {
			return nil, errors.New("must not initialize after cancellation")
		},
		Cache: &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{}},
		Send: func(msg tea.Msg) {
			if ctx.Err() == nil {
				messages <- msg
			}
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	<-messages
	cancel()
	startup.Cancel()
	close(release)
	if err := startup.WaitContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	select {
	case msg := <-messages:
		t.Fatalf("late UI message after cancellation: %#v", msg)
	default:
	}
}

func TestMattermostServerViewStateMarksFinderChannelsJoined(t *testing.T) {
	state := mattermostServerViewState(service.ServerSnapshot{
		Server: mattermost.Server{ID: "s1"}, CurrentUser: mattermost.User{ID: "u1"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{Channel: mattermost.Channel{ID: "c1", Kind: mattermost.ChannelKindPublic}, DisplayName: "Town Square"}}}},
	}, false, 1)
	if len(state.FinderItems) != 1 || !state.FinderItems[0].Joined {
		t.Fatalf("finder items = %#v", state.FinderItems)
	}
}

func TestMattermostCacheHydrationWithoutMembershipClearsUnread(t *testing.T) {
	raw := cachedMattermostSnapshot("s1", "u1", "town-square")
	raw.Channels[0].TotalMsgCount = 5
	raw.Memberships = nil
	snapshot, err := mattermostServiceSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	state := mattermostServerViewState(snapshot, false, 1)
	if state.HasUnread || len(state.ReadState) != 0 {
		t.Fatalf("unread state=%#v hasUnread=%v", state.ReadState, state.HasUnread)
	}
}

func TestUnreadRuntimeUpdateRetainsFreshServerSnapshot(t *testing.T) {
	startup := unreadMattermostStartup("s1", "u1", "c1")

	state, changed := startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s2", "other", true)

	if !changed || !state.ReadState["c1"].HasUnread {
		t.Fatalf("state=%#v changed=%v", state, changed)
	}
	if switched, ok := startup.switchMsg("s1").(ui.ServerSwitchedMsg); !ok || !switched.Server.ReadState["c1"].HasUnread {
		t.Fatalf("switch state=%#v", startup.switchMsg("s1"))
	}
}

func nextServerReady(t *testing.T, messages <-chan tea.Msg) ui.ServerReadyMsg {
	t.Helper()
	deadline := time.After(time.Second)
	for {
		select {
		case msg := <-messages:
			if ready, ok := msg.(ui.ServerReadyMsg); ok {
				return ready
			}
		case <-deadline:
			t.Fatal("timed out waiting for ServerReadyMsg")
		}
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

func (f *fakeMattermostSnapshotStore) ReplaceMattermostBootstrapSnapshot(snapshot cache.MattermostBootstrapSnapshot) error {
	return f.ApplyMattermostBootstrapSnapshot(snapshot)
}

func (f *fakeMattermostSnapshotStore) ReplaceMattermostBootstrapSnapshotContext(_ context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	return f.ApplyMattermostBootstrapSnapshot(snapshot)
}

type cancellationAwareSnapshotStore struct {
	started chan struct{}
}

func (s *cancellationAwareSnapshotStore) LoadMattermostBootstrapSnapshot(string) (cache.MattermostBootstrapSnapshot, error) {
	return cache.MattermostBootstrapSnapshot{}, sql.ErrNoRows
}

func (s *cancellationAwareSnapshotStore) ApplyMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error {
	return nil
}

func (s *cancellationAwareSnapshotStore) ReplaceMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error {
	return errors.New("non-context snapshot replacement used")
}

func (s *cancellationAwareSnapshotStore) ReplaceMattermostBootstrapSnapshotContext(ctx context.Context, _ cache.MattermostBootstrapSnapshot) error {
	close(s.started)
	<-ctx.Done()
	return ctx.Err()
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

type fixedBlockingBootstrapClient struct {
	release <-chan struct{}
	userID  string
}

func (blockingBootstrapClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{}, errors.New("unused history")
}

func (blockingBootstrapClient) RunWebSocket(ctx context.Context, _ func(), _ func(mattermost.Event), _ func(error)) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingBootstrapClient) CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error) {
	return mattermost.Message{}, errors.New("unused send")
}

func (blockingBootstrapClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, errors.New("unused view")
}

func (fixedBlockingBootstrapClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{}, errors.New("unused history")
}

func (fixedBlockingBootstrapClient) RunWebSocket(ctx context.Context, _ func(), _ func(mattermost.Event), _ func(error)) error {
	<-ctx.Done()
	return ctx.Err()
}

func (fixedBlockingBootstrapClient) CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error) {
	return mattermost.Message{}, errors.New("unused send")
}

func (fixedBlockingBootstrapClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, errors.New("unused view")
}

func (b fixedBlockingBootstrapClient) CurrentUser(ctx context.Context) (*mattermost.User, error) {
	select {
	case <-b.release:
		return &mattermost.User{ID: b.userID}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (fixedBlockingBootstrapClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return nil, nil
}
func (fixedBlockingBootstrapClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return nil, nil
}
func (fixedBlockingBootstrapClient) ChannelMembershipsForUser(context.Context, string, string) ([]mattermost.ChannelMembership, error) {
	return nil, nil
}
func (fixedBlockingBootstrapClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}
func (fixedBlockingBootstrapClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}

type blockingMattermostSecrets struct{ release <-chan struct{} }

func (b blockingMattermostSecrets) Get(context.Context, string) (string, error) {
	<-b.release
	return "token", nil
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
