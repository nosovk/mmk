package main

import (
	"context"
	"database/sql"
	"errors"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/sidebar"
)

func TestMattermostPostedEventPersistsForInactiveServer(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	notified := false
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db,
		Send:  func(context.Context, tea.Msg) error { notified = true; return nil },
		ActiveSelection: func() (ids.ServerID, string) {
			return "s2", "c1"
		},
	})
	want := cache.MattermostPost{
		ID: "opaque/post:id", ChannelID: "c1", UserID: "u1", RootID: "root-1",
		Text: "hello", CorrelationID: "opaque/correlation:id", CreatedAt: 10,
		UpdatedAt: 11, EditedAt: 12, DeletedAt: 13, ReplyCount: 4,
	}

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{
		ID: want.ID, ServerID: "payload-server", ChannelID: want.ChannelID,
		UserID: want.UserID, RootID: want.RootID, Text: want.Text,
		CorrelationID: want.CorrelationID, CreatedAt: want.CreatedAt,
		UpdatedAt: want.UpdatedAt, EditedAt: want.EditedAt,
		DeletedAt: want.DeletedAt, ReplyCount: want.ReplyCount,
	}})

	got, err := db.GetMattermostPost("s1", want.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("post=%#v want %#v", got, want)
	}
	if notified {
		t.Fatal("inactive event notified UI")
	}
}

func TestMattermostPostedEventRefreshesActiveChannel(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	var sent tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db,
		Send: func(_ context.Context, msg tea.Msg) error {
			if _, err := db.GetMattermostPost("s1", "post-1"); err != nil {
				t.Fatalf("UI notified before cache write: %v", err)
			}
			sent = msg
			return nil
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1"}})

	if got, ok := sent.(ui.MattermostHistoryRefreshMsg); !ok || got.ServerID != "s1" || got.ChannelID != "c1" {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestMattermostPostedEventPersistsWhenChannelIsAbsent(t *testing.T) {
	db := newMattermostEventDB(t, "s1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: db, ActiveSelection: func() (ids.ServerID, string) { return "", "" }})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ServerID: "payload-server", ChannelID: "unknown", Text: "visible after fallback"}})
	if _, err := db.GetMattermostPost("s1", "post-1"); err != nil {
		t.Fatalf("post not persisted: %v", err)
	}
	snapshot, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Channels) != 0 {
		t.Fatalf("placeholder channels=%#v want hidden", snapshot.Channels)
	}
	if _, err := db.GetMattermostPost("payload-server", "post-1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("payload server redirected persistence: %v", err)
	}
}

func TestMattermostPostedEventRemainsVisibleWhenRecentRESTRefreshFails(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	client := &failedRecentStartupClient{}
	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()
	startup := &mattermostStartup{contexts: map[ids.ServerID]mattermostServerContext{"s1": {client: client}}}
	history := mattermostUIHistoryService{ctx: runCtx, startup: startup, cache: db}
	app := ui.NewApp()
	app.SetMattermostHistoryService(history)
	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	_, selectCmd := app.Update(ui.ServerReadyMsg{Server: ui.ServerViewState{
		ServerID: "s1", InitialActive: true,
		Channels: []sidebar.ChannelItem{{ID: "c1", Name: "One", Type: "channel"}},
	}})
	selected, ok := findMattermostChannelSelectedMsg(selectCmd)
	if !ok {
		t.Fatal("server activation did not select its initial channel")
	}
	_, initialFetch := app.Update(selected)
	initialLoaded, ok := findMattermostRecentLoadedMsg(initialFetch)
	if !ok {
		t.Fatal("initial channel selection did not fetch recent history")
	}
	_, _ = app.Update(initialLoaded)

	var refresh tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db,
		Send: func(_ context.Context, msg tea.Msg) error {
			refresh = msg
			return nil
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
	})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{
		ID: "post-1", ChannelID: "c1", UserID: "u2", Text: "cached realtime post", CreatedAt: 10,
	}})
	if refresh == nil {
		t.Fatal("active-channel event did not request history refresh")
	}
	_, fetchCmd := app.Update(refresh)
	loaded, ok := findMattermostRecentLoadedMsg(fetchCmd)
	if !ok {
		t.Fatal("history refresh did not return a recent result")
	}
	if loaded.Err == nil || len(loaded.Messages) != 1 || loaded.Messages[0].ID != "post-1" {
		t.Fatalf("loaded=%#v", loaded)
	}
	_, errorCmd := app.Update(loaded)
	if errorCmd == nil {
		t.Fatal("REST failure was not reported")
	}
	toast, ok := errorCmd().(ui.ToastMsg)
	if !ok || !strings.Contains(toast.Text, "showing cached messages") {
		t.Fatalf("error message=%#v", toast)
	}
	if view := app.View().Content; !strings.Contains(view, "cached realtime post") {
		t.Fatalf("cached realtime post not rendered after REST failure:\n%s", view)
	}
}

func TestMattermostPostedEventDeduplicatesWithReconciliation(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	message := mattermost.Message{ID: "opaque/post:id", ChannelID: "c1", UserID: "u1", Text: "event", CreatedAt: 10}
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: db, ActiveSelection: func() (ids.ServerID, string) { return "", "" }})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: message})
	history := service.NewMattermostHistoryService("s1", mattermostEventHistoryClient{message: message}, db, mattermostHistoryPageSize)
	if _, err := history.FetchRecent(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}
	posts, err := db.ListMattermostChannelTimeline("s1", "c1", mattermostHistoryPageSize, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(posts) != 1 || posts[0].ID != message.ID {
		t.Fatalf("posts=%#v", posts)
	}
}

func TestMattermostEventCannotMutateAnotherServer(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: db, ActiveSelection: func() (ids.ServerID, string) { return "", "" }})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ServerID: "s2", ChannelID: "c1"}})
	if _, err := db.GetMattermostPost("s1", "post-1"); err != nil {
		t.Fatalf("trusted server row missing: %v", err)
	}
	if _, err := db.GetMattermostPost("s2", "post-1"); !reflect.DeepEqual(err, sql.ErrNoRows) {
		t.Fatalf("payload server lookup error=%v want sql.ErrNoRows", err)
	}
}

func TestMattermostEventIgnoresUnsupportedEventsSafely(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	notified := false
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: db, Send: func(context.Context, tea.Msg) error { notified = true; return nil }, ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" }})
	adapter.Handle(context.Background(), "s1", nil)
	if notified {
		t.Fatal("unsupported event notified UI")
	}
}

func TestMattermostProductionEventHandlerUsesCacheAndSelection(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	selection := newMattermostActiveSelection()
	selection.Store("s1", "c1")
	var sent tea.Msg
	handle := mattermostProductionEventHandler(db, func(_ context.Context, msg tea.Msg) error { sent = msg; return nil }, selection.Load, nil)

	handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1"}})

	if _, err := db.GetMattermostPost("s1", "post-1"); err != nil {
		t.Fatal(err)
	}
	if _, ok := sent.(ui.MattermostHistoryRefreshMsg); !ok {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestMattermostEventDispatcherEnqueueReturnsWhilePersistenceBlocked(t *testing.T) {
	store := &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher := newMattermostEventDispatcher("s1", newMattermostEventAdapter(mattermostEventDeps{Cache: store}).Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()

	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "first", ChannelID: "c1"}})
	waitMattermostEvent(t, store.started, "blocked persistence")
	returned := make(chan struct{})
	go func() {
		dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "second", ChannelID: "c1"}})
		close(returned)
	}()
	waitMattermostEvent(t, returned, "prompt enqueue")
	close(store.release)
	cancel()
	waitMattermostEvent(t, done, "dispatcher shutdown")
}

func TestMattermostEventDispatcherPreservesFIFO(t *testing.T) {
	store := &recordingMattermostEventStore{processed: make(chan string, 3)}
	dispatcher := newMattermostEventDispatcher("s1", newMattermostEventAdapter(mattermostEventDeps{Cache: store}).Handle)
	for _, id := range []string{"one", "two", "three"} {
		dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: id, ChannelID: "c1"}})
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()
	for _, want := range []string{"one", "two", "three"} {
		if got := waitMattermostEvent(t, store.processed, "FIFO event"); got != want {
			t.Fatalf("processed=%q want %q", got, want)
		}
	}
	cancel()
	waitMattermostEvent(t, done, "dispatcher shutdown")
}

func TestMattermostEventDispatcherContinuesAfterPersistenceFailure(t *testing.T) {
	diagnostic := make(chan error, 1)
	store := &recordingMattermostEventStore{failFirst: true, processed: make(chan string, 1)}
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Diagnostic: func(err error) { diagnostic <- err }})
	dispatcher := newMattermostEventDispatcher("s1", adapter.Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "sentinel-event-text", ChannelID: "c1"}})
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "later", ChannelID: "c1"}})

	if err := waitMattermostEvent(t, diagnostic, "persistence diagnostic"); err == nil || err.Error() != "Mattermost posted event persistence failed" {
		t.Fatalf("diagnostic=%v", err)
	}
	if got := waitMattermostEvent(t, store.processed, "later event"); got != "later" {
		t.Fatalf("processed=%q", got)
	}
	cancel()
	waitMattermostEvent(t, done, "dispatcher shutdown")
}

func TestMattermostEventDispatcherCancellationDiscardsQueuedEvents(t *testing.T) {
	store := &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}
	dispatcher := newMattermostEventDispatcher("s1", newMattermostEventAdapter(mattermostEventDeps{Cache: store}).Handle)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "first", ChannelID: "c1"}})
	waitMattermostEvent(t, store.started, "blocked persistence")
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "discarded", ChannelID: "c1"}})
	cancel()
	waitMattermostEvent(t, done, "canceled dispatcher")
	if got := store.ids(); !reflect.DeepEqual(got, []string{"first"}) {
		t.Fatalf("processed=%v want first only", got)
	}
}

type blockingMattermostEventStore struct {
	mu      sync.Mutex
	seen    []string
	started chan struct{}
	release chan struct{}
}

func (s *blockingMattermostEventStore) UpsertMattermostRealtimePostContext(ctx context.Context, _ string, post cache.MattermostPost) error {
	s.mu.Lock()
	s.seen = append(s.seen, post.ID)
	s.mu.Unlock()
	select {
	case <-s.started:
	default:
		close(s.started)
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (s *blockingMattermostEventStore) ids() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.seen...)
}

type recordingMattermostEventStore struct {
	mu        sync.Mutex
	failFirst bool
	processed chan string
}

func (s *recordingMattermostEventStore) UpsertMattermostRealtimePostContext(_ context.Context, _ string, post cache.MattermostPost) error {
	s.mu.Lock()
	if s.failFirst {
		s.failFirst = false
		s.mu.Unlock()
		return errors.New("database failure containing sentinel-event-text")
	}
	s.mu.Unlock()
	s.processed <- post.ID
	return nil
}

func waitMattermostEvent[T any](t *testing.T, ch <-chan T, label string) T {
	t.Helper()
	select {
	case value := <-ch:
		return value
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
		var zero T
		return zero
	}
}

type mattermostEventHistoryClient struct {
	message mattermost.Message
}

type failedRecentStartupClient struct {
	mattermostStartupClient
}

func (*failedRecentStartupClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{}, errors.New("offline")
}

func (*failedRecentStartupClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}

func findMattermostRecentLoadedMsg(cmd tea.Cmd) (ui.MattermostMessagesLoadedMsg, bool) {
	if cmd == nil {
		return ui.MattermostMessagesLoadedMsg{}, false
	}
	msg := cmd()
	if loaded, ok := msg.(ui.MattermostMessagesLoadedMsg); ok {
		return loaded, true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			if loaded, ok := findMattermostRecentLoadedMsg(next); ok {
				return loaded, true
			}
		}
	}
	return ui.MattermostMessagesLoadedMsg{}, false
}

func findMattermostChannelSelectedMsg(cmd tea.Cmd) (ui.ChannelSelectedMsg, bool) {
	if cmd == nil {
		return ui.ChannelSelectedMsg{}, false
	}
	msg := cmd()
	if selected, ok := msg.(ui.ChannelSelectedMsg); ok {
		return selected, true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			if selected, ok := findMattermostChannelSelectedMsg(next); ok {
				return selected, true
			}
		}
	}
	return ui.ChannelSelectedMsg{}, false
}

func (c mattermostEventHistoryClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{Messages: []mattermost.Message{c.message}}, nil
}

func (mattermostEventHistoryClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}

func newMattermostEventDB(t *testing.T, serverID string, channelIDs ...string) *cache.DB {
	t.Helper()
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	snapshot := cache.MattermostBootstrapSnapshot{
		Server:      cache.MattermostServer{ID: serverID, URL: "https://" + serverID + ".example", CurrentUserID: "u1"},
		CurrentUser: cache.MattermostUser{ID: "u1"},
		Teams:       []cache.MattermostTeam{{ID: "t1"}},
	}
	for _, channelID := range channelIDs {
		snapshot.Channels = append(snapshot.Channels, cache.MattermostChannel{ID: channelID, TeamID: "t1", Kind: "public"})
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	return db
}
