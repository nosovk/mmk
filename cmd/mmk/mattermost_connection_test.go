package main

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

func TestStartupRunsIndependentMattermostConnections(t *testing.T) {
	one := newStartupConnectionClient("u1")
	two := newStartupConnectionClient("u2")
	oneRetried := make(chan struct{})
	one.run = func(ctx context.Context, _ func(), _ func(mattermost.Event)) error {
		one.recordSocketAttempt()
		if one.socketAttempts() < 3 {
			return errors.New("server one unavailable")
		}
		close(oneRetried)
		<-ctx.Done()
		return ctx.Err()
	}
	twoReady := make(chan struct{})
	wantEvent := mattermost.PostedEvent{Message: mattermost.Message{ID: "post-2", ChannelID: "c2"}}
	two.run = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
		two.recordSocketAttempt()
		onReady()
		onEvent(wantEvent)
		close(twoReady)
		<-ctx.Done()
		return ctx.Err()
	}

	forwarded := make(chan mattermostServerEvent, 1)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": one, "s2": two}, mattermostStartupDeps{
		OnEvent: func(_ context.Context, serverID ids.ServerID, event mattermost.Event) {
			forwarded <- mattermostServerEvent{ServerID: serverID, Event: event}
		},
	})
	defer stopConnectionTestStartup(t, startup)

	select {
	case <-twoReady:
	case <-time.After(time.Second):
		t.Fatal("second server did not connect independently")
	}
	select {
	case <-oneRetried:
	case <-time.After(time.Second):
		t.Fatal("first server did not retry independently")
	}
	select {
	case got := <-forwarded:
		if got.ServerID != "s2" || got.Event != wantEvent {
			t.Fatalf("forwarded event = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("second server event was not forwarded")
	}
	if one.socketAttempts() != 3 || two.socketAttempts() != 1 {
		t.Fatalf("socket attempts: s1=%d s2=%d", one.socketAttempts(), two.socketAttempts())
	}
}

func TestStartupForwardsEventsWithServerIdentity(t *testing.T) {
	client := newStartupConnectionClient("u1")
	want := mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1"}}
	client.run = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
		onReady()
		onEvent(want)
		<-ctx.Done()
		return ctx.Err()
	}
	forwarded := make(chan mattermostServerEvent, 1)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		OnEvent: func(_ context.Context, serverID ids.ServerID, event mattermost.Event) {
			forwarded <- mattermostServerEvent{ServerID: serverID, Event: event}
		},
	})
	defer stopConnectionTestStartup(t, startup)

	select {
	case got := <-forwarded:
		if got.ServerID != "s1" || got.Event != want {
			t.Fatalf("forwarded event = %#v", got)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for forwarded event")
	}
}

func TestUnreadStartupProductionBindingAppliesImmediatePostedEvent(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	client := newStartupConnectionClient("u1")
	client.teams = []mattermost.Team{{ID: "t1"}}
	client.channels = []mattermost.Channel{{ID: "c1", TeamID: "t1", Kind: mattermost.ChannelKindPublic}}
	client.memberships = map[string][]mattermost.ChannelMembership{"t1": {{ChannelID: "c1", UserID: "u1"}}}
	eventHandled := make(chan struct{})
	client.run = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
		onReady()
		onEvent(mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})
		close(eventHandled)
		<-ctx.Done()
		return ctx.Err()
	}
	messages := make(chan tea.Msg, 16)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		Cache: db,
		Send:  func(msg tea.Msg) { messages <- msg; acknowledgeMattermostRefresh(msg) },
		NewEventHandler: func(startup *mattermostStartup) func(context.Context, ids.ServerID, mattermost.Event) {
			return mattermostProductionEventHandler(db, func(_ context.Context, msg tea.Msg) error {
				messages <- msg
				return nil
			}, func() (ids.ServerID, string) { return "s2", "other" }, func() []ui.HistoryRequest { return nil }, startup, nil)
		},
	})
	defer stopConnectionTestStartup(t, startup)
	waitMattermostEvent(t, eventHandled, "immediate WebSocket event")
	deadline := time.After(time.Second)
	for !startup.viewState("s1").ReadState["c1"].HasUnread {
		select {
		case <-messages:
		case <-deadline:
			t.Fatal("immediate production event did not update retained unread state")
		}
	}
	if _, err := db.GetMattermostPost("s1", "post-1"); err != nil {
		t.Fatalf("immediate event post not persisted: %v", err)
	}
}

func TestStartupWaitIncludesConnectionManagers(t *testing.T) {
	client := newStartupConnectionClient("u1")
	backoff := make(chan struct{})
	client.run = func(context.Context, func(), func(mattermost.Event)) error {
		return errors.New("socket unavailable")
	}
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		ConnectionWait: func(ctx context.Context, _ time.Duration) error {
			close(backoff)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	select {
	case <-backoff:
	case <-time.After(time.Second):
		t.Fatal("connection manager did not enter backoff")
	}
	startup.Cancel()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startup.WaitContext(waitCtx); err != nil {
		t.Fatalf("WaitContext error = %v", err)
	}
}

func TestStartupCancellationInterruptsInitialSnapshotPersistence(t *testing.T) {
	client := newStartupConnectionClient("u1")
	store := &cancellationAwareSnapshotStore{started: make(chan struct{})}
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{Cache: store})

	select {
	case <-store.started:
	case <-time.After(time.Second):
		startup.Cancel()
		t.Fatal("initial snapshot persistence did not use context-aware replacement")
	}
	startup.Cancel()
	waitCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startup.WaitContext(waitCtx); err != nil {
		t.Fatalf("WaitContext error = %v", err)
	}
}

func TestStartupEventCallbackReturnsPromptlyAndWaitTracksWorker(t *testing.T) {
	client := newStartupConnectionClient("u1")
	callbackReturned := make(chan struct{})
	client.run = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
		onReady()
		onEvent(mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1"}})
		close(callbackReturned)
		<-ctx.Done()
		return ctx.Err()
	}
	handleStarted := make(chan struct{})
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		OnEvent: func(ctx context.Context, _ ids.ServerID, _ mattermost.Event) {
			close(handleStarted)
			<-ctx.Done()
		},
	})
	waitMattermostEvent(t, callbackReturned, "WebSocket callback return")
	waitMattermostEvent(t, handleStarted, "event worker start")
	startup.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startup.WaitContext(ctx); err != nil {
		t.Fatalf("WaitContext error=%v", err)
	}
}

func TestRuntimeDisconnectPreservesUsableCachedServer(t *testing.T) {
	client := newStartupConnectionClient("u1")
	disconnected := make(chan struct{})
	client.run = func(ctx context.Context, onReady func(), _ func(mattermost.Event)) error {
		onReady()
		close(disconnected)
		return errors.New("socket lost")
	}
	messages := make(chan tea.Msg, 32)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		Send: func(msg tea.Msg) { messages <- msg },
		ConnectionWait: func(ctx context.Context, _ time.Duration) error {
			<-ctx.Done()
			return ctx.Err()
		},
	})
	defer stopConnectionTestStartup(t, startup)
	select {
	case <-disconnected:
	case <-time.After(time.Second):
		t.Fatal("socket did not disconnect")
	}
	if !startup.usable("s1") || startup.switchMsg("s1") == nil {
		t.Fatal("runtime disconnect made cached server unusable")
	}
	for {
		select {
		case msg := <-messages:
			if state, ok := msg.(ui.ServerStateMsg); ok && state.ServerID == "s1" && state.State == workspace.ItemStateError {
				t.Fatalf("runtime disconnect emitted bootstrap error: %#v", state)
			}
		default:
			return
		}
	}
}

func TestMattermostProductionConnectionStateMessageIsServerScoped(t *testing.T) {
	var sent tea.Msg
	callback := mattermostConnectionStateSender(func(msg tea.Msg) { sent = msg })
	callback("s2", mattermost.ConnectionStateReconnecting)
	msg, ok := sent.(ui.ServerConnectionStateMsg)
	if !ok || msg.ServerID != "s2" || msg.State != workspace.ItemStateReconnecting {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestStartupReconcilesOnlyAfterReconnect(t *testing.T) {
	client := newStartupConnectionClient("u1")
	firstReady := make(chan struct{})
	secondReady := make(chan struct{})
	reconcileStarted := make(chan struct{})
	client.currentUserHook = func(_ context.Context, call int) error {
		if call == 2 {
			close(reconcileStarted)
		}
		return nil
	}
	var attempt int
	client.run = func(ctx context.Context, onReady func(), _ func(mattermost.Event)) error {
		attempt++
		onReady()
		if attempt == 1 {
			close(firstReady)
			return errors.New("socket lost")
		}
		close(secondReady)
		<-ctx.Done()
		return ctx.Err()
	}
	waits := make(chan struct{}, 1)
	releaseWait := make(chan struct{})
	var releaseWaitOnce sync.Once
	releaseReconnectWait := func() { releaseWaitOnce.Do(func() { close(releaseWait) }) }
	t.Cleanup(releaseReconnectWait)
	db := newMattermostReconcileDB(t)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		Cache: db,
		ConnectionWait: func(ctx context.Context, _ time.Duration) error {
			select {
			case waits <- struct{}{}:
			case <-ctx.Done():
				return ctx.Err()
			}
			select {
			case <-releaseWait:
				return nil
			case <-ctx.Done():
				return ctx.Err()
			}
		},
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	})
	defer stopConnectionTestStartup(t, startup)
	select {
	case <-firstReady:
	case <-time.After(time.Second):
		t.Fatal("first socket did not become ready")
	}
	select {
	case <-waits:
	case <-time.After(time.Second):
		t.Fatal("first disconnect did not enter reconnect wait")
	}
	if got := client.bootstrapCalls(); got != 1 {
		t.Fatalf("bootstrap calls before reconnect = %d, want 1", got)
	}
	releaseReconnectWait()
	select {
	case <-secondReady:
	case <-time.After(time.Second):
		t.Fatal("second socket did not become ready")
	}
	waitMattermostEvent(t, reconcileStarted, "reconciliation after reconnect")
	if got := client.bootstrapCalls(); got != 2 {
		t.Fatalf("bootstrap calls after reconnect = %d, want 2", got)
	}
}

func TestStartupReconnectReadsEventsWhileReconciliationBlocked(t *testing.T) {
	client := newStartupConnectionClient("u1")
	reconcileStarted := make(chan struct{})
	client.currentUserHook = func(ctx context.Context, call int) error {
		if call != 2 {
			return nil
		}
		close(reconcileStarted)
		<-ctx.Done()
		return ctx.Err()
	}
	eventDelivered := make(chan struct{})
	connectedAfterReconnect := make(chan struct{})
	var attempt int
	client.run = func(ctx context.Context, onReady func(), onEvent func(mattermost.Event)) error {
		attempt++
		onReady()
		if attempt == 1 {
			return errors.New("socket lost")
		}
		onEvent(mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1"}})
		close(eventDelivered)
		<-ctx.Done()
		return ctx.Err()
	}
	connectedCount := 0
	db := newMattermostReconcileDB(t)
	startup := startConnectionTestStartup(t, map[string]*startupConnectionClient{"s1": client}, mattermostStartupDeps{
		Cache:   db,
		OnEvent: func(context.Context, ids.ServerID, mattermost.Event) {},
		OnConnectionState: func(_ ids.ServerID, state mattermost.ConnectionState) {
			if state == mattermost.ConnectionStateConnected {
				connectedCount++
				if connectedCount == 2 {
					close(connectedAfterReconnect)
				}
			}
		},
	})
	waitMattermostEvent(t, reconcileStarted, "blocked reconciliation")
	waitMattermostEvent(t, connectedAfterReconnect, "reconnect connected state")
	waitMattermostEvent(t, eventDelivered, "event after reconnect")
	startup.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startup.WaitContext(ctx); err != nil {
		t.Fatalf("WaitContext error=%v", err)
	}
}

func TestMattermostActiveSelectionStoresAtomicPair(t *testing.T) {
	selection := newMattermostActiveSelection()
	selection.Store("s1", "c1")
	serverID, channelID := selection.Load()
	if serverID != "s1" || channelID != "c1" {
		t.Fatalf("selection = %q/%q", serverID, channelID)
	}
}

func TestMattermostActiveSelectionKeepsHistoryRequestGenerationIndependent(t *testing.T) {
	selection := newMattermostActiveSelection()
	request := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 17}
	selection.StoreHistoryRequest(request)
	selection.Store("s2", "c2")

	if got := selection.LoadHistoryRequest(); got != request {
		t.Fatalf("history request=%#v want %#v", got, request)
	}
	if serverID, channelID := selection.Load(); serverID != "s2" || channelID != "c2" {
		t.Fatalf("selection=%q/%q want s2/c2", serverID, channelID)
	}
}

type mattermostServerEvent struct {
	ServerID ids.ServerID
	Event    mattermost.Event
}

type startupConnectionClient struct {
	mu              sync.Mutex
	userID          string
	run             func(context.Context, func(), func(mattermost.Event)) error
	bootstrapCount  int
	socketCount     int
	currentUserHook func(context.Context, int) error
	teams           []mattermost.Team
	channels        []mattermost.Channel
	memberships     map[string][]mattermost.ChannelMembership
}

func newStartupConnectionClient(userID string) *startupConnectionClient {
	return &startupConnectionClient{userID: userID}
}

func (c *startupConnectionClient) RunWebSocket(ctx context.Context, onReady func(), onEvent func(mattermost.Event), _ func(error)) error {
	return c.run(ctx, onReady, onEvent)
}

func (c *startupConnectionClient) CurrentUser(ctx context.Context) (*mattermost.User, error) {
	c.mu.Lock()
	c.bootstrapCount++
	call := c.bootstrapCount
	hook := c.currentUserHook
	c.mu.Unlock()
	if hook != nil {
		if err := hook(ctx, call); err != nil {
			return nil, err
		}
	}
	return &mattermost.User{ID: c.userID}, nil
}

func (c *startupConnectionClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return append([]mattermost.Team(nil), c.teams...), nil
}

func (c *startupConnectionClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return append([]mattermost.Channel(nil), c.channels...), nil
}

func (c *startupConnectionClient) ChannelMembershipsForUser(_ context.Context, _ string, teamID string) ([]mattermost.ChannelMembership, error) {
	return append([]mattermost.ChannelMembership(nil), c.memberships[teamID]...), nil
}

func (*startupConnectionClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}

func (*startupConnectionClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}

func (*startupConnectionClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{}, nil
}

func (*startupConnectionClient) CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error) {
	return mattermost.Message{}, nil
}

func (*startupConnectionClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, nil
}

func (c *startupConnectionClient) recordSocketAttempt() {
	c.mu.Lock()
	c.socketCount++
	c.mu.Unlock()
}

func (c *startupConnectionClient) socketAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.socketCount
}

func (c *startupConnectionClient) bootstrapCalls() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bootstrapCount
}

func startConnectionTestStartup(t *testing.T, clients map[string]*startupConnectionClient, overrides mattermostStartupDeps) *mattermostStartup {
	t.Helper()
	registry := config.NewServerRegistry()
	for id, client := range clients {
		registry.Servers = append(registry.Servers, config.MattermostServer{ID: id, URL: "https://" + id + ".example", UserID: client.userID})
	}
	deps := mattermostStartupDeps{
		Registry: registry,
		Secrets:  fakeMattermostSecrets{tokens: map[string]string{"s1": "one", "s2": "two"}},
		NewClient: func(server mattermost.Server, _ string) (mattermostStartupClient, error) {
			return clients[server.ID], nil
		},
		Cache:           &fakeMattermostSnapshotStore{loads: map[string]cache.MattermostBootstrapSnapshot{}},
		Send:            acknowledgeMattermostRefresh,
		Clock:           time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
		OnEvent:         func(context.Context, ids.ServerID, mattermost.Event) {},
		ConnectionWait: func(ctx context.Context, _ time.Duration) error {
			select {
			case <-ctx.Done():
				return ctx.Err()
			default:
				return nil
			}
		},
	}
	if overrides.Cache != nil {
		deps.Cache = overrides.Cache
	}
	if overrides.Send != nil {
		deps.Send = overrides.Send
	}
	if overrides.ActiveSelection != nil {
		deps.ActiveSelection = overrides.ActiveSelection
	}
	if overrides.OnEvent != nil {
		deps.OnEvent = overrides.OnEvent
	}
	if overrides.NewEventHandler != nil {
		deps.NewEventHandler = overrides.NewEventHandler
	}
	if overrides.OnConnectionState != nil {
		deps.OnConnectionState = overrides.OnConnectionState
	}
	if overrides.ConnectionWait != nil {
		deps.ConnectionWait = overrides.ConnectionWait
	}
	startup, err := startMattermost(context.Background(), deps)
	if err != nil {
		t.Fatal(err)
	}
	return startup
}

func stopConnectionTestStartup(t *testing.T, startup *mattermostStartup) {
	t.Helper()
	startup.Cancel()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if err := startup.WaitContext(ctx); err != nil {
		t.Fatal(err)
	}
}
