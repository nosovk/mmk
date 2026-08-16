package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
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
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
)

func TestMattermostRealtimeReplyPersistsBeforeAuthoritativeNotification(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	startup := unreadMattermostStartup("s1", "u1", "c1")
	startup.mu.Lock()
	serverContext := startup.contexts["s1"]
	serverContext.snapshot.Users = append(serverContext.snapshot.Users, mattermost.User{ID: "u2", FirstName: "Bob", LastName: "Builder"})
	startup.contexts["s1"] = serverContext
	startup.mu.Unlock()
	request := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	var sent []tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:               db,
		LiveHistoryRequests: func() []ui.HistoryRequest { return []ui.HistoryRequest{request} },
		Send: func(_ context.Context, msg tea.Msg) error {
			if _, err := db.GetMattermostPost("s1", "reply-1"); err != nil {
				t.Fatalf("UI notified before cache write: %v", err)
			}
			sent = append(sent, msg)
			return nil
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{
		ID: "reply-1", ChannelID: "c1", RootID: "root-1", UserID: "u2",
		Text: "authoritative", CorrelationID: "corr-1", CreatedAt: 123,
		EditedAt: 124, ReplyCount: 3,
	}})

	realtimeIndex, historyIndex := -1, -1
	var got ui.MattermostRealtimePostMsg
	for i, msg := range sent {
		if value, ok := msg.(ui.MattermostRealtimePostMsg); ok {
			realtimeIndex, got = i, value
		}
		if _, ok := msg.(ui.MattermostHistoryRefreshMsg); ok {
			historyIndex = i
		}
	}
	if realtimeIndex < 0 || historyIndex < 0 || realtimeIndex >= historyIndex {
		t.Fatalf("notifications=%#v want realtime post before history refresh", sent)
	}
	want := ui.MattermostRealtimePostMsg{Request: request, Message: messages.MessageItem{
		ID: "reply-1", CorrelationID: "corr-1", CreatedAt: 123, RootID: "root-1",
		Format: messages.FormatMattermostPlain, UserID: "u2", UserName: "Bob Builder",
		Text: "authoritative", ReplyCount: 3, IsEdited: true,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("realtime=%#v want %#v", got, want)
	}
}

func TestMattermostRealtimeReplyPersistenceFailureAndCancellationSuppressNotification(t *testing.T) {
	for _, tc := range []struct {
		name  string
		ctx   func() context.Context
		store mattermostEventStore
	}{
		{name: "failure", ctx: context.Background, store: &atomicMattermostEventStore{err: errors.New("disk full")}},
		{name: "cancellation", ctx: func() context.Context { ctx, cancel := context.WithCancel(context.Background()); cancel(); return ctx }, store: &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			notified := false
			adapter := newMattermostEventAdapter(mattermostEventDeps{
				Cache: tc.store,
				LiveHistoryRequests: func() []ui.HistoryRequest {
					return []ui.HistoryRequest{{ServerID: "s1", ChannelID: "c1", Generation: 1}}
				},
				Send: func(context.Context, tea.Msg) error { notified = true; return nil },
			})
			adapter.Handle(tc.ctx(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "reply-1", ChannelID: "c1", RootID: "root-1", CreatedAt: 1}})
			if notified {
				t.Fatal("failed or canceled persistence notified UI")
			}
		})
	}
}

func TestMattermostRealtimeReplyCapturesOriginScopeBeforePersistence(t *testing.T) {
	store := &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}
	selection := newMattermostActiveSelection()
	origin := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	selection.StoreHistoryRequests([]ui.HistoryRequest{origin})
	var sent tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:               store,
		LiveHistoryRequests: selection.LoadHistoryRequests,
		Send: func(_ context.Context, msg tea.Msg) error {
			if _, ok := msg.(ui.MattermostRealtimePostMsg); ok {
				sent = msg
			}
			return nil
		},
	})
	done := make(chan struct{})
	go func() {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "reply-1", ChannelID: "c1", RootID: "root-1", CreatedAt: 1}})
		close(done)
	}()
	waitMattermostEvent(t, store.started, "blocked persistence")
	selection.StoreHistoryRequests([]ui.HistoryRequest{{ServerID: "s1", ChannelID: "c2", Generation: 8}})
	close(store.release)
	waitMattermostEvent(t, done, "posted event completion")

	got, ok := sent.(ui.MattermostRealtimePostMsg)
	if !ok || got.Request != origin {
		t.Fatalf("realtime=%#v want origin request %#v", sent, origin)
	}
}

func TestMattermostRealtimeReplyRoutesAllMatchingLiveRetainedScopes(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1", "c2")
	c1 := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	c1SecondGeneration := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 9}
	live := []ui.HistoryRequest{
		c1,
		c1,
		{ServerID: "s1", ChannelID: "c2", Generation: 8},
		c1SecondGeneration,
		{ServerID: "s2", ChannelID: "c1", Generation: 10},
	}
	var sent []ui.MattermostRealtimePostMsg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db,
		LiveHistoryRequests: func() []ui.HistoryRequest {
			return append([]ui.HistoryRequest(nil), live...)
		},
		Send: func(_ context.Context, msg tea.Msg) error {
			if realtime, ok := msg.(ui.MattermostRealtimePostMsg); ok {
				sent = append(sent, realtime)
			}
			return nil
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c2" },
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "reply-1", ChannelID: "c1", RootID: "root-1", CreatedAt: 1}})

	if len(sent) != 2 || sent[0].Request != c1 || sent[1].Request != c1SecondGeneration {
		t.Fatalf("realtime scopes=%#v want exact live c1 generations %#v/%#v", sent, c1, c1SecondGeneration)
	}
}

func TestMattermostRealtimeReplyCapturesLiveScopeSnapshotBeforePersistence(t *testing.T) {
	store := &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}
	origin := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	live := []ui.HistoryRequest{origin}
	var sent tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: store,
		LiveHistoryRequests: func() []ui.HistoryRequest {
			return append([]ui.HistoryRequest(nil), live...)
		},
		Send: func(_ context.Context, msg tea.Msg) error {
			if _, ok := msg.(ui.MattermostRealtimePostMsg); ok {
				sent = msg
			}
			return nil
		},
	})
	done := make(chan struct{})
	go func() {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "reply-1", ChannelID: "c1", RootID: "root-1", CreatedAt: 1}})
		close(done)
	}()
	waitMattermostEvent(t, store.started, "blocked persistence")
	live = []ui.HistoryRequest{{ServerID: "s1", ChannelID: "c2", Generation: 8}}
	close(store.release)
	waitMattermostEvent(t, done, "posted event completion")

	got, ok := sent.(ui.MattermostRealtimePostMsg)
	if !ok || got.Request != origin {
		t.Fatalf("realtime=%#v want captured origin %#v", sent, origin)
	}
}

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

func TestUnreadInactiveChannelPostUpdatesRuntimeBeforeNotification(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1", "c2")
	startup := unreadMattermostStartup("s1", "u1", "c1", "c2")
	var sent []tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db,
		Send: func(_ context.Context, msg tea.Msg) error {
			if !startup.viewState("s1").ReadState["c2"].HasUnread {
				t.Fatal("UI notified before retained runtime snapshot update")
			}
			sent = append(sent, msg)
			return nil
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c2", CreatedAt: 10}})

	if len(sent) != 1 {
		t.Fatalf("notifications=%#v want one server refresh", sent)
	}
	refresh, ok := sent[0].(ui.ServerRefreshedMsg)
	if !ok || !refresh.Server.ReadState["c2"].HasUnread || !refresh.Server.HasUnread {
		t.Fatalf("refresh=%#v", sent[0])
	}
}

func TestUnreadInactiveServerPostUpdatesServerScopedRuntime(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	startup := unreadMattermostStartup("s1", "u1", "c1")
	var sent tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           db,
		Send:            func(_ context.Context, msg tea.Msg) error { sent = msg; return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

	refresh, ok := sent.(ui.ServerRefreshedMsg)
	if !ok || refresh.Server.ServerID != "s1" || !refresh.Server.ReadState["c1"].HasUnread {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestUnreadActiveChannelPostSuppressesUnreadAndKeepsHistoryRefresh(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	startup := unreadMattermostStartup("s1", "u1", "c1")
	var sent []tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           db,
		Send:            func(_ context.Context, msg tea.Msg) error { sent = append(sent, msg); return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

	state := startup.viewState("s1")
	if state.ReadState["c1"].HasUnread || state.HasUnread {
		t.Fatalf("active channel became unread: %#v", state)
	}
	if len(sent) != 2 {
		t.Fatalf("notifications=%#v want server and history refresh", sent)
	}
	if _, ok := sent[0].(ui.ServerRefreshedMsg); !ok {
		t.Fatalf("first notification=%#v want server refresh", sent[0])
	}
	if _, ok := sent[1].(ui.MattermostHistoryRefreshMsg); !ok {
		t.Fatalf("second notification=%#v want history refresh", sent[1])
	}
}

func TestUnreadPostedEventUsesSelectionAtRuntimeTransition(t *testing.T) {
	for _, tt := range []struct {
		name               string
		initialChannel     string
		transitionChannel  string
		wantUnread         bool
		wantHistoryRefresh bool
	}{
		{name: "becomes active", initialChannel: "c1", transitionChannel: "c2", wantUnread: false, wantHistoryRefresh: true},
		{name: "becomes inactive", initialChannel: "c2", transitionChannel: "c1", wantUnread: true, wantHistoryRefresh: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &blockingMattermostEventStore{started: make(chan struct{}), release: make(chan struct{})}
			startup := unreadMattermostStartup("s1", "u1", "c1", "c2")
			selection := newMattermostActiveSelection()
			selection.Store("s1", tt.initialChannel)
			var mu sync.Mutex
			var sent []tea.Msg
			adapter := newMattermostEventAdapter(mattermostEventDeps{
				Cache: store,
				Send: func(_ context.Context, msg tea.Msg) error {
					mu.Lock()
					sent = append(sent, msg)
					mu.Unlock()
					return nil
				},
				ActiveSelection: selection.Load,
				Startup:         startup,
			})
			done := make(chan struct{})
			go func() {
				adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c2", CreatedAt: 10}})
				close(done)
			}()
			waitMattermostEvent(t, store.started, "blocked event persistence")
			selection.Store("s1", tt.transitionChannel)
			close(store.release)
			waitMattermostEvent(t, done, "posted event completion")

			state := startup.viewState("s1")
			if got := state.ReadState["c2"].HasUnread; got != tt.wantUnread {
				t.Fatalf("unread=%v want %v state=%#v", got, tt.wantUnread, state)
			}
			mu.Lock()
			defer mu.Unlock()
			historyRefreshes := 0
			for _, msg := range sent {
				if refresh, ok := msg.(ui.MattermostHistoryRefreshMsg); ok && refresh.ChannelID == "c2" {
					historyRefreshes++
				}
			}
			if got := historyRefreshes == 1; got != tt.wantHistoryRefresh {
				t.Fatalf("history refreshes=%d messages=%#v", historyRefreshes, sent)
			}
		})
	}
}

func TestUnreadDuplicatePostedDeliveryDoesNotDoubleIncrementRuntime(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           db,
		Send:            func(context.Context, tea.Msg) error { return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		Startup:         startup,
	})
	event := mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}}

	adapter.Handle(context.Background(), "s1", event)
	adapter.Handle(context.Background(), "s1", event)

	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 1 || entry.Membership.MsgCount != 0 {
		t.Fatalf("counts total=%d viewed=%d want 1/0", entry.Channel.TotalMsgCount, entry.Membership.MsgCount)
	}
}

func TestRuntimePostHistoryPersistenceBeforeWebSocketStillAdvancesOnce(t *testing.T) {
	store := &atomicMattermostEventStore{inserted: false}
	startup := unreadMattermostStartup("s1", "u1", "c1")
	var sent []tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           store,
		Send:            func(_ context.Context, msg tea.Msg) error { sent = append(sent, msg); return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 1 || entry.Membership.MsgCount != 1 {
		t.Fatalf("persisted history suppressed runtime transition: %#v", entry)
	}
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})
	entry = unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 1 || entry.Membership.MsgCount != 1 {
		t.Fatalf("duplicate advanced runtime counts: %#v", entry)
	}
	if len(sent) != 3 {
		t.Fatalf("notifications=%#v want one runtime refresh and two history refreshes", sent)
	}
	if _, ok := sent[0].(ui.ServerRefreshedMsg); !ok {
		t.Fatalf("first notification=%#v want runtime refresh", sent[0])
	}
}

func TestRuntimePostPersistenceFailureDoesNotMarkApplied(t *testing.T) {
	store := &atomicMattermostEventStore{err: errors.New("disk full")}
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup})
	event := mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}}

	adapter.Handle(context.Background(), "s1", event)
	store.err = nil
	adapter.Handle(context.Background(), "s1", event)

	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 1 {
		t.Fatalf("retry after persistence failure did not apply exactly once: %#v", entry)
	}
}

func TestRuntimePostRejectsNonpositiveCreateAt(t *testing.T) {
	store := &atomicMattermostEventStore{inserted: true}
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup})
	for _, createdAt := range []int64{0, -1} {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: fmt.Sprintf("post-%d", createdAt), ChannelID: "c1", CreatedAt: createdAt}})
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 0 {
		t.Fatalf("nonpositive posts advanced runtime: %#v", entry)
	}
}

func TestRuntimePostEqualTimestampDistinctIDsAdvanceIndependently(t *testing.T) {
	store := newClaimingMattermostEventStore()
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup, ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" }})
	for _, id := range []string{"post-1", "post-2", "post-1"} {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: id, ChannelID: "c1", CreatedAt: 10}})
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 2 {
		t.Fatalf("equal timestamp count=%d want 2", entry.Channel.TotalMsgCount)
	}
}

func TestRuntimePostArbitraryOutOfOrderDistinctIDsAdvanceIndependently(t *testing.T) {
	store := newClaimingMattermostEventStore()
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup})
	for _, post := range []mattermost.Message{
		{ID: "post-20", ChannelID: "c1", CreatedAt: 20},
		{ID: "post-10", ChannelID: "c1", CreatedAt: 10},
		{ID: "post-20", ChannelID: "c1", CreatedAt: 20},
		{ID: "post-10", ChannelID: "c1", CreatedAt: 10},
	} {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: post})
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != 2 {
		t.Fatalf("out-of-order distinct count=%d want 2", entry.Channel.TotalMsgCount)
	}
}

func TestRuntimePostStateHasNoUnboundedPostIDLedger(t *testing.T) {
	store := newClaimingMattermostEventStore()
	startup := unreadMattermostStartup("s1", "u1", "c1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup})
	for i := int64(1); i <= 1000; i++ {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: fmt.Sprintf("post-%d", i), ChannelID: "c1", CreatedAt: i}})
	}
	stateType := reflect.TypeOf(*startup.reconcileState("s1"))
	for i := 0; i < stateType.NumField(); i++ {
		if strings.Contains(strings.ToLower(stateType.Field(i).Name), "frontier") {
			t.Fatalf("runtime state retains frontier field %q", stateType.Field(i).Name)
		}
	}
}

func TestChannelViewedEqualModernNoOpDoesNotRecordOverlayAndLegacyDoes(t *testing.T) {
	startup := unreadMattermostStartup("s1", "u1", "c1")
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	entry.Channel.TotalMsgCount = 5
	entry.Membership.MsgCount = 5
	entry.Membership.LastViewedAt = 100
	startup.mu.Lock()
	serverContext := startup.contexts["s1"]
	serverContext.snapshot.Sections[0].Channels[0] = entry
	startup.contexts["s1"] = serverContext
	startup.mu.Unlock()
	adapter := newMattermostEventAdapter(mattermostEventDeps{Startup: startup})
	adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u1", Updates: []mattermost.ChannelViewUpdate{{ChannelID: "c1", ViewedAt: 100, HasViewedAt: true}}})
	startup.mu.RLock()
	_, modernTracked := startup.contexts["s1"].localReadOverlays["c1"]
	startup.mu.RUnlock()
	if modernTracked {
		t.Fatal("equal modern viewed event recorded overlay")
	}
	adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u1", Updates: []mattermost.ChannelViewUpdate{{ChannelID: "c1"}}})
	startup.mu.RLock()
	overlay := startup.contexts["s1"].localReadOverlays["c1"]
	startup.mu.RUnlock()
	if overlay.ViewedMsgCount != 5 || overlay.HasLastViewedAt {
		t.Fatalf("overlay=%#v", overlay)
	}
}

func TestChannelViewedCurrentUserCorrectsRuntimeAndLegacyKeepsTimestamp(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1", "c2")
	startup := unreadMattermostStartup("s1", "u1", "c1", "c2")
	startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s2", "", true)
	startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c2"}}, "s2", "", true)
	var sent tea.Msg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           db,
		Send:            func(_ context.Context, msg tea.Msg) error { sent = msg; return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u1", Updates: []mattermost.ChannelViewUpdate{
		{ChannelID: "c1", ViewedAt: 50, HasViewedAt: true},
		{ChannelID: "c2"},
	}})

	refresh, ok := sent.(ui.ServerRefreshedMsg)
	if !ok || refresh.Server.HasUnread {
		t.Fatalf("sent=%#v", sent)
	}
	c1 := unreadMattermostEntry(t, startup, "s1", "c1")
	c2 := unreadMattermostEntry(t, startup, "s1", "c2")
	if c1.Membership.LastViewedAt != 50 || c2.Membership.LastViewedAt != 10 {
		t.Fatalf("last viewed c1=%d c2=%d want 50/10", c1.Membership.LastViewedAt, c2.Membership.LastViewedAt)
	}
}

func TestChannelViewedOtherUserIsIgnored(t *testing.T) {
	db := newMattermostEventDB(t, "s1", "c1")
	startup := unreadMattermostStartup("s1", "u1", "c1")
	startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s2", "", true)
	notified := false
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           db,
		Send:            func(context.Context, tea.Msg) error { notified = true; return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		Startup:         startup,
	})

	adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u2", Updates: []mattermost.ChannelViewUpdate{{ChannelID: "c1", ViewedAt: 50, HasViewedAt: true}}})

	if !startup.viewState("s1").ReadState["c1"].HasUnread || notified {
		t.Fatal("other user's viewed event changed runtime state or notified UI")
	}
}

func TestChannelViewedStaleTimestampDoesNotClearNewerUnread(t *testing.T) {
	for _, viewedAt := range []int64{90, 100} {
		t.Run(fmt.Sprintf("viewed_at_%d", viewedAt), func(t *testing.T) {
			startup := unreadMattermostStartup("s1", "u1", "c1")
			startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s2", "", true)
			startup.mu.Lock()
			serverContext := startup.contexts["s1"]
			entry := serverContext.snapshot.Sections[0].Channels[0]
			membership := *entry.Membership
			membership.LastViewedAt = 100
			membership.MentionCount = 2
			entry.Membership = &membership
			serverContext.snapshot.Sections[0].Channels[0] = entry
			startup.contexts["s1"] = serverContext
			startup.mu.Unlock()
			notified := false
			adapter := newMattermostEventAdapter(mattermostEventDeps{
				Send:            func(context.Context, tea.Msg) error { notified = true; return nil },
				ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
				Startup:         startup,
			})

			adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u1", Updates: []mattermost.ChannelViewUpdate{{ChannelID: "c1", ViewedAt: viewedAt, HasViewedAt: true}}})

			got := unreadMattermostEntry(t, startup, "s1", "c1")
			if got.Membership.MsgCount != 0 || got.Membership.MentionCount != 2 || got.Membership.LastViewedAt != 100 || !service.ChannelHasUnread(got) {
				t.Fatalf("stale viewed update changed entry: %#v", got)
			}
			if notified {
				t.Fatal("stale viewed update notified UI")
			}
		})
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

	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

	if got, ok := sent.(ui.MattermostHistoryRefreshMsg); !ok || got.ServerID != "s1" || got.ChannelID != "c1" {
		t.Fatalf("sent=%#v", sent)
	}
}

func TestMattermostPostedEventPersistsWhenChannelIsAbsent(t *testing.T) {
	db := newMattermostEventDB(t, "s1")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: db, ActiveSelection: func() (ids.ServerID, string) { return "", "" }})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ServerID: "payload-server", ChannelID: "unknown", Text: "visible after fallback", CreatedAt: 10}})
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
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ServerID: "s2", ChannelID: "c1", CreatedAt: 10}})
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
	handle := mattermostProductionEventHandler(db, func(_ context.Context, msg tea.Msg) error { sent = msg; return nil }, selection.Load, func() []ui.HistoryRequest {
		return []ui.HistoryRequest{{ServerID: "s1", ChannelID: "c1", Generation: 1}}
	}, nil, nil)

	handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

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

	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "first", ChannelID: "c1", CreatedAt: 1}})
	waitMattermostEvent(t, store.started, "blocked persistence")
	returned := make(chan struct{})
	go func() {
		dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "second", ChannelID: "c1", CreatedAt: 2}})
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
	for i, id := range []string{"one", "two", "three"} {
		dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: id, ChannelID: "c1", CreatedAt: int64(i + 1)}})
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
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "sentinel-event-text", ChannelID: "c1", CreatedAt: 1}})
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "later", ChannelID: "c1", CreatedAt: 2}})

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
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "first", ChannelID: "c1", CreatedAt: 1}})
	waitMattermostEvent(t, store.started, "blocked persistence")
	dispatcher.Enqueue(mattermost.PostedEvent{Message: mattermost.Message{ID: "discarded", ChannelID: "c1", CreatedAt: 2}})
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

func (s *blockingMattermostEventStore) UpsertMattermostRealtimePostContext(ctx context.Context, _ string, post cache.MattermostPost) (bool, error) {
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
		return true, nil
	case <-ctx.Done():
		return false, ctx.Err()
	}
}

func (s *blockingMattermostEventStore) PersistMattermostRealtimePostContext(ctx context.Context, serverID string, post cache.MattermostPost) error {
	_, err := s.UpsertMattermostRealtimePostContext(ctx, serverID, post)
	return err
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

func (s *recordingMattermostEventStore) UpsertMattermostRealtimePostContext(_ context.Context, _ string, post cache.MattermostPost) (bool, error) {
	s.mu.Lock()
	if s.failFirst {
		s.failFirst = false
		s.mu.Unlock()
		return false, errors.New("database failure containing sentinel-event-text")
	}
	s.mu.Unlock()
	s.processed <- post.ID
	return true, nil
}

func (s *recordingMattermostEventStore) PersistMattermostRealtimePostContext(ctx context.Context, serverID string, post cache.MattermostPost) error {
	_, err := s.UpsertMattermostRealtimePostContext(ctx, serverID, post)
	return err
}

type atomicMattermostEventStore struct {
	inserted bool
	err      error
	claimed  bool
}

type claimingMattermostEventStore struct {
	mu      sync.Mutex
	claimed map[string]bool
}

func newClaimingMattermostEventStore() *claimingMattermostEventStore {
	return &claimingMattermostEventStore{claimed: make(map[string]bool)}
}

func (s *claimingMattermostEventStore) UpsertMattermostRealtimePostContext(_ context.Context, _ string, post cache.MattermostPost) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.claimed[post.ID] {
		return false, nil
	}
	s.claimed[post.ID] = true
	return true, nil
}

func (*claimingMattermostEventStore) PersistMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) error {
	return nil
}

func (s *atomicMattermostEventStore) UpsertMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if s.claimed {
		return false, nil
	}
	s.claimed = true
	return true, nil
}

func (s *atomicMattermostEventStore) PersistMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) error {
	return s.err
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

func unreadMattermostStartup(serverID, userID string, channelIDs ...string) *mattermostStartup {
	entries := make([]service.ChannelEntry, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		entries = append(entries, service.ChannelEntry{
			Channel: mattermost.Channel{ID: channelID, Kind: mattermost.ChannelKindPublic},
			Membership: &mattermost.ChannelMembership{
				ChannelID: channelID, UserID: userID, LastViewedAt: 10,
			},
		})
	}
	snapshot := service.ServerSnapshot{
		Server:      mattermost.Server{ID: serverID, Name: serverID},
		CurrentUser: mattermost.User{ID: userID},
		Sections:    []service.ChannelSection{{ID: "t1", Channels: entries}},
	}
	return &mattermostStartup{contexts: map[ids.ServerID]mattermostServerContext{
		ids.ServerID(serverID): {server: snapshot.Server, snapshot: snapshot, usable: true},
	}}
}

func unreadMattermostEntry(t *testing.T, startup *mattermostStartup, serverID ids.ServerID, channelID string) service.ChannelEntry {
	t.Helper()
	startup.mu.RLock()
	defer startup.mu.RUnlock()
	for _, section := range startup.contexts[serverID].snapshot.Sections {
		for _, entry := range section.Channels {
			if entry.Channel.ID == channelID {
				return entry
			}
		}
	}
	t.Fatalf("channel %q not found", channelID)
	return service.ChannelEntry{}
}
