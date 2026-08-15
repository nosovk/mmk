package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
)

func TestReconciliationReplacesAuthoritativeSnapshot(t *testing.T) {
	db := newMattermostReconcileDB(t)
	initial := cache.MattermostBootstrapSnapshot{
		Server:      cache.MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1"},
		CurrentUser: cache.MattermostUser{ID: "u1"},
		Teams:       []cache.MattermostTeam{{ID: "t1"}, {ID: "retired-team"}},
		Channels: []cache.MattermostChannel{
			{ID: "c1", TeamID: "t1", Name: "old-name", Kind: "public"},
			{ID: "retired-channel", TeamID: "retired-team", Kind: "public"},
		},
		Memberships: []cache.MattermostChannelMembership{
			{ChannelID: "c1", UserID: "u1"},
			{ChannelID: "retired-channel", UserID: "u1"},
		},
	}
	if err := db.ReplaceMattermostBootstrapSnapshot(initial); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", cache.MattermostPost{ID: "retained-post", ChannelID: "retired-channel", Text: "retained", CreatedAt: 1}); err != nil {
		t.Fatal(err)
	}

	client := &reconciliationClient{
		currentUser: mattermost.User{ID: "u1"},
		teams:       []mattermost.Team{{ID: "t1", DisplayName: "Team"}},
		channels:    []mattermost.Channel{{ID: "c1", TeamID: "t1", DisplayName: "new-name", Kind: mattermost.ChannelKindPublic}},
		memberships: map[string][]mattermost.ChannelMembership{"t1": {{ChannelID: "c1", UserID: "u1"}}},
	}
	startup := reconciliationStartup(client, service.ServerSnapshot{
		Server:      mattermost.Server{ID: "s1", Name: "One", URL: "https://one.example", UserID: "u1"},
		CurrentUser: mattermost.User{ID: "u1"},
	})
	var refreshed ui.ServerRefreshedMsg
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db,
		Send: func(msg tea.Msg) {
			refreshed, _ = msg.(ui.ServerRefreshedMsg)
			acknowledgeMattermostRefresh(msg)
		},
		Clock:           func() time.Time { return time.UnixMilli(123) },
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	})
	if err != nil {
		t.Fatal(err)
	}

	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := reconcileCacheTeamIDs(loaded.Teams), []string{"t1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("teams=%v want %v", got, want)
	}
	if got, want := reconcileCacheChannelIDs(loaded.Channels), []string{"c1"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("channels=%v want %v", got, want)
	}
	if len(loaded.Memberships) != 1 || loaded.Memberships[0].ChannelID != "c1" {
		t.Fatalf("memberships=%#v", loaded.Memberships)
	}
	if post, err := db.GetMattermostPost("s1", "retained-post"); err != nil || post.Text != "retained" {
		t.Fatalf("retained post=%#v err=%v", post, err)
	}
	if refreshed.Server.ServerID != "s1" || len(refreshed.Server.Channels) != 1 || refreshed.Server.Channels[0].Name != "new-name" {
		t.Fatalf("refresh=%#v", refreshed)
	}
	if got := startup.viewState("s1"); len(got.Channels) != 1 || got.Channels[0].Name != "new-name" {
		t.Fatalf("memory snapshot=%#v", got)
	}
}

func TestUnreadReconnectAuthoritativeCorrectionReplacesRuntimeOverlay(t *testing.T) {
	db := newMattermostReconcileDB(t)
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 4
	client.memberships["t1"][0].MsgCount = 4
	startup := reconciliationStartup(client, service.ServerSnapshot{
		Server:      mattermost.Server{ID: "s1", Name: "One"},
		CurrentUser: mattermost.User{ID: "u1"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{
			Channel:    mattermost.Channel{ID: "c1", Kind: mattermost.ChannelKindPublic, TotalMsgCount: 4},
			Membership: &mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: 3},
		}}}},
	})
	if !startup.viewState("s1").ReadState["c1"].HasUnread {
		t.Fatal("test setup did not create runtime unread overlay")
	}
	beforeRevision := startup.viewState("s1").Revision

	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db, Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if state := startup.viewState("s1"); state.ReadState["c1"].HasUnread || state.HasUnread || state.Revision <= beforeRevision {
		t.Fatalf("authoritative reconciliation retained runtime unread: %#v", state)
	}
}

func TestReconciliationPostAppliedDuringFetchIsRebasedExactlyOnce(t *testing.T) {
	store := newBlockingReconciliationStore(t)
	if err := store.DB.ReplaceMattermostBootstrapSnapshot(mattermostCacheSnapshot(localReadSnapshot(true), time.Now())); err != nil {
		t.Fatal(err)
	}
	client := reconciliationBootstrapClient()
	fetchCaptured := make(chan struct{})
	releaseFetch := make(chan struct{})
	client.currentUserHook = func(ctx context.Context) error {
		close(fetchCaptured)
		select {
		case <-releaseFetch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	startup := reconciliationStartup(client, service.ServerSnapshot{
		Server:      mattermost.Server{ID: "s1", Name: "One"},
		CurrentUser: mattermost.User{ID: "u1"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{
			Channel:    mattermost.Channel{ID: "c1", TeamID: "t1", Kind: mattermost.ChannelKindPublic},
			Membership: &mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1"},
		}}}},
	})
	var mu sync.Mutex
	var unreadOrder []bool
	send := func(msg tea.Msg) {
		if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
			mu.Lock()
			unreadOrder = append(unreadOrder, refresh.Server.HasUnread)
			mu.Unlock()
			acknowledgeMattermostRefresh(msg)
		}
	}
	reconcileDone := make(chan error, 1)
	go func() {
		reconcileDone <- startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
			Cache: store, Send: send, Clock: time.Now,
			ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		})
	}()
	waitMattermostEvent(t, fetchCaptured, "authoritative snapshot fetch")
	eventDone := make(chan struct{})
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           store,
		Send:            func(_ context.Context, msg tea.Msg) error { send(msg); return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		Startup:         startup,
	})
	go func() {
		adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})
		close(eventDone)
	}()
	waitMattermostEvent(t, eventDone, "event applied while reconciliation fetch was blocked")
	state := startup.reconcileState("s1")
	state.runtime.Lock()
	_, recorded := state.inFlight.posts["post-1"]
	state.runtime.Unlock()
	if !recorded {
		t.Fatal("event transition was not recorded for reconciliation rebase")
	}
	close(releaseFetch)
	waitMattermostEvent(t, store.firstEntered, "authoritative snapshot persistence")
	close(store.releaseFirst)
	if err := <-reconcileDone; err != nil {
		t.Fatal(err)
	}
	if state := startup.viewState("s1"); !state.ReadState["c1"].HasUnread {
		t.Fatalf("final state lost post-reconciliation event: %#v", state)
	}
	mu.Lock()
	defer mu.Unlock()
	if !reflect.DeepEqual(unreadOrder, []bool{true, true}) {
		t.Fatalf("refresh unread order=%v want runtime transition then rebased commit", unreadOrder)
	}
}

func TestReconciliationFetchesOnlyActiveServerChannel(t *testing.T) {
	db := newMattermostReconcileDB(t)
	client := &reconciliationClient{
		currentUser: mattermost.User{ID: "u1"},
		teams:       []mattermost.Team{{ID: "t1"}},
		channels: []mattermost.Channel{
			{ID: "active-channel", TeamID: "t1", Kind: mattermost.ChannelKindPublic},
			{ID: "other-channel", TeamID: "t1", Kind: mattermost.ChannelKindPublic},
		},
		memberships: map[string][]mattermost.ChannelMembership{"t1": {
			{ChannelID: "active-channel", UserID: "u1"},
			{ChannelID: "other-channel", UserID: "u1"},
		}},
		postPage: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "recent", ChannelID: "active-channel", UserID: "u1", Text: "recent", CreatedAt: 1}}},
	}
	startup := reconciliationStartup(client, service.ServerSnapshot{})
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db, Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "active-channel" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := client.postCalls, []reconciliationPostCall{{channelID: "active-channel", options: mattermost.ChannelPostsOptions{Page: 0, PerPage: mattermostHistoryPageSize}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post calls=%#v want %#v", got, want)
	}
	if post, err := db.GetMattermostPost("s1", "recent"); err != nil || post.ChannelID != "active-channel" {
		t.Fatalf("recent post=%#v err=%v", post, err)
	}
}

func TestReconciliationUpdatesActivePaneFromFetchedPageWithoutExtraRESTCall(t *testing.T) {
	db := newMattermostReconcileDB(t)
	client := reconciliationBootstrapClient()
	client.postPage = mattermost.MessagePage{
		OrderCount: 2,
		Messages: []mattermost.Message{
			{ID: "missed", ChannelID: "c1", UserID: "u1", Text: "missed post", CreatedAt: 2},
			{ID: "deleted", ChannelID: "c1", UserID: "u1", DeletedAt: 3, CreatedAt: 1},
		},
	}
	startup := reconciliationStartup(client, service.ServerSnapshot{})
	app := ui.NewApp()
	selection := newMattermostActiveSelection()
	app.SetSelectionObserver(selection.Store)
	app.SetMattermostHistoryService(&fakeReconcileUIHistory{})
	_, readyCmd := app.Update(ui.ServerReadyMsg{Server: ui.ServerViewState{
		ServerID:      "s1",
		InitialActive: true,
		Channels:      []sidebar.ChannelItem{{ID: "c1", Name: "One"}},
	}})
	if readyCmd == nil {
		t.Fatal("server activation did not select a channel")
	}
	_, _ = app.Update(readyCmd())

	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db,
		Send: func(msg tea.Msg) {
			_, _ = app.Update(msg)
		},
		Clock:                   time.Now,
		ActiveSelection:         selection.Load,
		ActiveSelectionSnapshot: selection.LoadSnapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := client.postCalls; len(got) != 1 || got[0].channelID != "c1" {
		t.Fatalf("post calls=%#v want one c1 fetch", got)
	}
	_, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	view := ansi.Strip(app.View().Content)
	if !strings.Contains(view, "missed post") || strings.Contains(view, "deleted") {
		t.Fatalf("active pane not reconciled: %q", view)
	}
}

func TestReconciliationDropsFetchedPageAfterSameChannelReselection(t *testing.T) {
	db := newMattermostReconcileDB(t)
	entered := make(chan struct{})
	release := make(chan struct{})
	client := reconciliationBootstrapClient()
	client.postPage = mattermost.MessagePage{Messages: []mattermost.Message{{ID: "stale", ChannelID: "c1", Text: "stale result", CreatedAt: 1}}}
	client.postHook = func(context.Context) error {
		close(entered)
		<-release
		return nil
	}
	startup := reconciliationStartup(client, service.ServerSnapshot{})
	app := ui.NewApp()
	selection := newMattermostActiveSelection()
	app.SetSelectionObserver(selection.Store)
	app.SetMattermostHistoryService(&fakeReconcileUIHistory{})
	_, readyCmd := app.Update(ui.ServerReadyMsg{Server: ui.ServerViewState{ServerID: "s1", InitialActive: true, Channels: []sidebar.ChannelItem{{ID: "c1", Name: "One"}, {ID: "c2", Name: "Two"}}}})
	_, _ = app.Update(readyCmd())

	done := make(chan error, 1)
	go func() {
		done <- startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
			Cache: db, Send: func(msg tea.Msg) { _, _ = app.Update(msg) }, Clock: time.Now,
			ActiveSelection: selection.Load, ActiveSelectionSnapshot: selection.LoadSnapshot,
		})
	}()
	<-entered
	_, _ = app.Update(ui.ChannelSelectedMsg{ID: "c2", Name: "Two"})
	_, _ = app.Update(ui.ChannelSelectedMsg{ID: "c1", Name: "One"})
	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	_, _ = app.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	if view := ansi.Strip(app.View().Content); strings.Contains(view, "stale result") {
		t.Fatalf("stale same-channel result applied: %q", view)
	}
}

func TestReconciliationUsesSelectionPublishedAfterRefreshApply(t *testing.T) {
	db := newMattermostReconcileDB(t)
	client := reconciliationBootstrapClient()
	client.channels = []mattermost.Channel{{ID: "new-channel", TeamID: "t1", Kind: mattermost.ChannelKindPublic}}
	client.memberships = map[string][]mattermost.ChannelMembership{"t1": {{ChannelID: "new-channel", UserID: "u1"}}}
	selection := newMattermostActiveSelection()
	selection.Store("s1", "removed-channel")
	app := ui.NewApp()
	app.SetSelectionObserver(selection.Store)
	_, readyCmd := app.Update(ui.ServerReadyMsg{Server: ui.ServerViewState{
		ServerID:      "s1",
		InitialActive: true,
		Channels:      []sidebar.ChannelItem{{ID: "removed-channel", Name: "Removed"}},
	}})
	if readyCmd == nil {
		t.Fatal("initial server did not select a channel")
	}
	_, _ = app.Update(readyCmd())

	startup := reconciliationStartup(client, service.ServerSnapshot{})
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db,
		Send: func(msg tea.Msg) {
			_, _ = app.Update(msg)
		},
		Clock:           time.Now,
		ActiveSelection: selection.Load,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := client.postCalls, []reconciliationPostCall{{channelID: "new-channel", options: mattermost.ChannelPostsOptions{Page: 0, PerPage: mattermostHistoryPageSize}}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("post calls=%#v want %#v", got, want)
	}
}

func TestReconciliationCancellationWhileWaitingForRefreshApply(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	refreshSent := make(chan struct{})
	startup := reconciliationStartup(reconciliationBootstrapClient(), service.ServerSnapshot{})
	db := newMattermostReconcileDB(t)
	done := make(chan error, 1)
	go func() {
		done <- startup.reconcile(ctx, "s1", mattermostReconcileDeps{
			Cache: db,
			Send: func(tea.Msg) {
				close(refreshSent)
			},
			Clock:           time.Now,
			ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		})
	}()
	select {
	case <-refreshSent:
	case <-time.After(time.Second):
		t.Fatal("refresh was not sent")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("reconciliation error=%v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("reconciliation did not stop after cancellation")
	}
}

func TestReconciliationSkipsHistoryForInactiveServer(t *testing.T) {
	for _, tt := range []struct {
		name          string
		activeServer  ids.ServerID
		activeChannel string
	}{
		{name: "inactive server", activeServer: "s2", activeChannel: "c1"},
		{name: "empty active channel", activeServer: "s1"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			db := newMattermostReconcileDB(t)
			client := reconciliationBootstrapClient()
			startup := reconciliationStartup(client, service.ServerSnapshot{})
			err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
				Cache: db, Send: acknowledgeMattermostRefresh, Clock: time.Now,
				ActiveSelection: func() (ids.ServerID, string) { return tt.activeServer, tt.activeChannel },
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(client.postCalls) != 0 {
				t.Fatalf("unexpected history fetch: %#v", client.postCalls)
			}
		})
	}
}

func TestReconciliationPersistsBeforeServerRefresh(t *testing.T) {
	db := newMattermostReconcileDB(t)
	store := &orderedReconciliationStore{DB: db}
	client := reconciliationBootstrapClient()
	startup := reconciliationStartup(client, service.ServerSnapshot{
		Server:      mattermost.Server{ID: "s1", Name: "One", URL: "https://one.example", UserID: "u1"},
		CurrentUser: mattermost.User{ID: "u1"},
	})
	var sendErr error
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: store,
		Send: func(msg tea.Msg) {
			defer acknowledgeMattermostRefresh(msg)
			if _, ok := msg.(ui.ServerRefreshedMsg); !ok {
				sendErr = errors.New("unexpected message type")
				return
			}
			if !store.persisted {
				sendErr = errors.New("refresh sent before persistence")
				return
			}
			if got := startup.viewState("s1"); len(got.Channels) != 1 || got.Channels[0].ID != "c1" {
				sendErr = errors.New("refresh sent before memory update")
			}
		},
		Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	})
	if err != nil {
		t.Fatal(err)
	}
	if sendErr != nil {
		t.Fatal(sendErr)
	}
}

func TestReconciliationFailureKeepsPreviousUsableSnapshot(t *testing.T) {
	previous := service.ServerSnapshot{
		Server:      mattermost.Server{ID: "s1", Name: "One", URL: "https://one.example", UserID: "u1"},
		CurrentUser: mattermost.User{ID: "u1"},
		Sections: []service.ChannelSection{{Channels: []service.ChannelEntry{{
			Channel: mattermost.Channel{ID: "previous", Kind: mattermost.ChannelKindPublic}, DisplayName: "Previous",
		}}}},
	}
	for _, tt := range []struct {
		name   string
		client *reconciliationClient
		store  mattermostReconcileStore
	}{
		{name: "bootstrap", client: &reconciliationClient{bootstrapErr: errors.New("offline")}, store: newMattermostReconcileDB(t)},
		{name: "persistence", client: reconciliationBootstrapClient(), store: &failingReconciliationStore{DB: newMattermostReconcileDB(t)}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startup := reconciliationStartup(tt.client, previous)
			sent := false
			err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
				Cache: tt.store, Send: func(msg tea.Msg) { sent = true; acknowledgeMattermostRefresh(msg) }, Clock: time.Now,
				ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
			})
			if err == nil {
				t.Fatal("expected reconciliation failure")
			}
			if sent {
				t.Fatal("failure emitted server refresh")
			}
			if got := startup.viewState("s1"); len(got.Channels) != 1 || got.Channels[0].ID != "previous" {
				t.Fatalf("previous snapshot replaced: %#v", got)
			}
			if len(tt.client.postCalls) != 0 {
				t.Fatalf("history fetched after failure: %#v", tt.client.postCalls)
			}
		})
	}
}

func TestReconciliationSupersededRequestCannotOverwriteNewerSnapshot(t *testing.T) {
	db := newMattermostReconcileDB(t)
	oldEntered := make(chan struct{})
	releaseOld := make(chan struct{})
	oldClient := reconciliationBootstrapClient()
	oldClient.channels[0].DisplayName = "old"
	oldClient.currentUserHook = func(ctx context.Context) error {
		close(oldEntered)
		select {
		case <-releaseOld:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	newClient := reconciliationBootstrapClient()
	newClient.channels[0].DisplayName = "new"
	startup := reconciliationStartup(oldClient, service.ServerSnapshot{})
	var refreshedNames []string
	deps := mattermostReconcileDeps{
		Cache: db,
		Send: func(msg tea.Msg) {
			defer acknowledgeMattermostRefresh(msg)
			if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
				refreshedNames = append(refreshedNames, refresh.Server.Channels[0].Name)
			}
		},
		Clock:           time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
	}

	oldDone := make(chan error, 1)
	go func() { oldDone <- startup.reconcile(context.Background(), "s1", deps) }()
	<-oldEntered
	startup.setClient("s1", newClient)
	if err := startup.reconcile(context.Background(), "s1", deps); err != nil {
		t.Fatalf("new reconciliation: %v", err)
	}
	close(releaseOld)
	if err := <-oldDone; !errors.Is(err, errMattermostReconciliationSuperseded) {
		t.Fatalf("old reconciliation error=%v", err)
	}

	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Channels) != 1 || loaded.Channels[0].DisplayName != "new" {
		t.Fatalf("cache snapshot=%#v", loaded.Channels)
	}
	if got := startup.viewState("s1"); len(got.Channels) != 1 || got.Channels[0].Name != "new" {
		t.Fatalf("memory snapshot=%#v", got)
	}
	if !reflect.DeepEqual(refreshedNames, []string{"new"}) {
		t.Fatalf("refreshes=%v", refreshedNames)
	}
	if len(oldClient.postCalls) != 0 || len(newClient.postCalls) != 1 {
		t.Fatalf("old history=%#v new history=%#v", oldClient.postCalls, newClient.postCalls)
	}
}

func TestReconciliationGenerationIssuanceWaitsForInProgressApply(t *testing.T) {
	store := newBlockingReconciliationStore(t)
	oldClient := reconciliationBootstrapClient()
	oldClient.channels[0].DisplayName = "old"
	oldClient.channels[0].UpdatedAt = 1
	newRESTEntered := make(chan struct{})
	newClient := reconciliationBootstrapClient()
	newClient.channels[0].DisplayName = "new"
	newClient.channels[0].UpdatedAt = 2
	newClient.currentUserHook = func(context.Context) error {
		close(newRESTEntered)
		return nil
	}
	startup := reconciliationStartup(oldClient, service.ServerSnapshot{})
	deps := mattermostReconcileDeps{
		Cache: store, Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	}

	oldDone := make(chan error, 1)
	go func() { oldDone <- startup.reconcile(context.Background(), "s1", deps) }()
	<-store.firstEntered
	t.Cleanup(func() {
		select {
		case <-store.releaseFirst:
		default:
			close(store.releaseFirst)
		}
	})

	clientSet := make(chan struct{})
	go func() {
		startup.setClient("s1", newClient)
		close(clientSet)
	}()
	select {
	case <-clientSet:
		t.Fatal("client replacement completed while prior cache commit was in progress")
	default:
	}
	close(store.releaseFirst)
	waitMattermostEvent(t, clientSet, "client replacement after prior cache commit")
	newInvoked := make(chan struct{})
	newDone := make(chan error, 1)
	go func() {
		close(newInvoked)
		newDone <- startup.reconcile(context.Background(), "s1", deps)
	}()
	<-newInvoked
	waitMattermostEvent(t, newRESTEntered, "new reconciliation after prior apply")

	if err := <-oldDone; err != nil {
		t.Fatalf("old reconciliation: %v", err)
	}
	if err := <-newDone; err != nil {
		t.Fatalf("new reconciliation: %v", err)
	}
	loaded, err := store.LoadMattermostBootstrapSnapshot("s1")
	if err != nil || len(loaded.Channels) != 1 || loaded.Channels[0].DisplayName != "new" {
		t.Fatalf("final snapshot=%#v err=%v", loaded.Channels, err)
	}
}

func TestReconciliationDifferentServersSerializeStartupTransaction(t *testing.T) {
	store := newBlockingReconciliationStore(t)
	client1 := reconciliationBootstrapClient()
	client2 := reconciliationBootstrapClient()
	client2.currentUser.ID = "u2"
	client2.teams[0].ID = "t2"
	client2.channels[0] = mattermost.Channel{ID: "c2", TeamID: "t2", Kind: mattermost.ChannelKindPublic}
	client2.memberships = map[string][]mattermost.ChannelMembership{"t2": {{ChannelID: "c2", UserID: "u2"}}}
	startup := reconciliationStartup(client1, service.ServerSnapshot{})
	startup.contexts["s2"] = mattermostServerContext{server: mattermost.Server{ID: "s2", Name: "Two", URL: "https://two.example", UserID: "u2"}, client: client2}
	deps := mattermostReconcileDeps{
		Cache: store, Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	}

	firstDone := make(chan error, 1)
	go func() { firstDone <- startup.reconcile(context.Background(), "s1", deps) }()
	<-store.firstEntered
	secondDone := make(chan error, 1)
	secondStarted := make(chan struct{})
	go func() {
		close(secondStarted)
		secondDone <- startup.reconcile(context.Background(), "s2", deps)
	}()
	waitMattermostEvent(t, secondStarted, "different-server reconciliation start")
	select {
	case err := <-secondDone:
		t.Fatalf("different-server commit completed before first commit released: %v", err)
	default:
	}
	close(store.releaseFirst)
	if err := <-firstDone; err != nil {
		t.Fatal(err)
	}
	if err := <-secondDone; err != nil {
		t.Fatal(err)
	}
}

func TestReconciliationRejectsNilDependencies(t *testing.T) {
	db := newMattermostReconcileDB(t)
	var typedNil *cache.DB
	valid := mattermostReconcileDeps{
		Cache: db, Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	}
	for _, tt := range []struct {
		name string
		deps mattermostReconcileDeps
		want string
	}{
		{name: "cache", deps: mattermostReconcileDeps{Send: valid.Send, Clock: valid.Clock, ActiveSelection: valid.ActiveSelection}, want: "cache"},
		{name: "typed nil cache", deps: mattermostReconcileDeps{Cache: typedNil, Send: valid.Send, Clock: valid.Clock, ActiveSelection: valid.ActiveSelection}, want: "cache"},
		{name: "send", deps: mattermostReconcileDeps{Cache: db, Clock: valid.Clock, ActiveSelection: valid.ActiveSelection}, want: "send"},
		{name: "clock", deps: mattermostReconcileDeps{Cache: db, Send: valid.Send, ActiveSelection: valid.ActiveSelection}, want: "clock"},
		{name: "active selection", deps: mattermostReconcileDeps{Cache: db, Send: valid.Send, Clock: valid.Clock}, want: "active selection"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			startup := reconciliationStartup(reconciliationBootstrapClient(), service.ServerSnapshot{})
			err := startup.reconcile(context.Background(), "s1", tt.deps)
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.want) {
				t.Fatalf("error=%v want dependency name %q", err, tt.want)
			}
		})
	}
}

func TestReconciliationHistoryFailureRetainsCommittedSnapshotAndRefresh(t *testing.T) {
	db := newMattermostReconcileDB(t)
	client := reconciliationBootstrapClient()
	client.channels[0].DisplayName = "committed"
	client.postErr = errors.New("history unavailable")
	startup := reconciliationStartup(client, service.ServerSnapshot{})
	refreshed := false
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db, Clock: time.Now,
		Send: func(msg tea.Msg) {
			if _, ok := msg.(ui.ServerRefreshedMsg); ok {
				refreshed = true
			}
			acknowledgeMattermostRefresh(msg)
		},
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
	})
	if err == nil || !strings.Contains(err.Error(), "history") {
		t.Fatalf("history error=%v", err)
	}
	if !refreshed {
		t.Fatal("history failure prevented refresh")
	}
	loaded, loadErr := db.LoadMattermostBootstrapSnapshot("s1")
	if loadErr != nil || len(loaded.Channels) != 1 || loaded.Channels[0].DisplayName != "committed" {
		t.Fatalf("committed cache=%#v err=%v", loaded.Channels, loadErr)
	}
	if got := startup.viewState("s1"); len(got.Channels) != 1 || got.Channels[0].Name != "committed" {
		t.Fatalf("committed memory=%#v", got)
	}
}

func TestReconciliationErrorDoesNotExposeClientConfigurationSecret(t *testing.T) {
	const sentinel = "fake-internal-token-sentinel"
	client := &reconciliationClient{bootstrapErr: errors.New("request failed"), internalToken: sentinel}
	startup := reconciliationStartup(client, service.ServerSnapshot{})
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: newMattermostReconcileDB(t), Send: func(tea.Msg) {}, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	})
	if err == nil {
		t.Fatal("expected reconciliation error")
	}
	if strings.Contains(err.Error(), sentinel) {
		t.Fatalf("error exposed internal configuration: %v", err)
	}
}

func TestMattermostReconcileDispatcherCoalescesPendingRequests(t *testing.T) {
	started := make(chan int, 2)
	release := make(chan struct{})
	var mu sync.Mutex
	runs := 0
	dispatcher := newMattermostReconcileDispatcher(func(ctx context.Context) error {
		mu.Lock()
		runs++
		run := runs
		mu.Unlock()
		started <- run
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()

	dispatcher.Enqueue()
	if got := waitMattermostEvent(t, started, "first reconciliation"); got != 1 {
		t.Fatalf("run=%d want 1", got)
	}
	for range 5 {
		dispatcher.Enqueue()
	}
	release <- struct{}{}
	if got := waitMattermostEvent(t, started, "coalesced reconciliation"); got != 2 {
		t.Fatalf("run=%d want 2", got)
	}
	release <- struct{}{}
	select {
	case run := <-started:
		t.Fatalf("unexpected duplicate reconciliation %d", run)
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	waitMattermostEvent(t, done, "reconcile dispatcher shutdown")
}

func TestMattermostReconcileDispatcherCancellationStopsRunningWork(t *testing.T) {
	started := make(chan struct{})
	dispatcher := newMattermostReconcileDispatcher(func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		return ctx.Err()
	}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()
	dispatcher.Enqueue()
	waitMattermostEvent(t, started, "running reconciliation")
	cancel()
	waitMattermostEvent(t, done, "reconcile dispatcher shutdown")
}

func TestReconciliationLocalReadReplayDrainsPendingOverlayAtCommit(t *testing.T) {
	client := newLocalReadReconciliationClient()
	db := newMattermostReconcileDB(t)
	store := &countingReconciliationStore{DB: db}
	startup := reconciliationStartup(client, localReadSnapshot(false))
	read := newMattermostUIReadService(context.Background(), startup, nil)
	refreshed := make(chan ui.ServerViewState, 1)
	dispatcher := newMattermostReconcileDispatcher(func(ctx context.Context) error {
		return startup.reconcile(ctx, "s1", mattermostReconcileDeps{
			Cache: store,
			Send: func(msg tea.Msg) {
				if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
					refreshed <- refresh.Server
				}
				acknowledgeMattermostRefresh(msg)
			},
			Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" },
		})
	}, func(err error) { t.Errorf("unexpected reconciliation diagnostic: %v", err) })
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { dispatcher.Run(ctx); close(done) }()
	t.Cleanup(func() { cancel(); waitMattermostEvent(t, done, "local-read dispatcher shutdown") })

	dispatcher.Enqueue()
	waitMattermostEvent(t, client.firstCaptured, "stale unread reconciliation fetch")
	optimistic, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	if optimistic.ReadState["c1"].HasUnread || cmd == nil {
		t.Fatalf("optimistic state=%#v cmd=%v", optimistic, cmd)
	}
	if msg := cmd(); msg == nil {
		t.Fatal("REST correction did not publish changed state")
	}
	close(client.releaseFirst)
	committed := waitMattermostEvent(t, refreshed, "overlay-replayed authoritative reconciliation")
	if committed.ReadState["c1"].HasUnread {
		t.Fatalf("retried UI state regressed read: %#v", committed.ReadState)
	}

	if attempts := client.bootstrapAttempts(); attempts != 1 {
		t.Fatalf("bootstrap attempts=%d want 1", attempts)
	}
	if replaces := store.replaceCount(); replaces != 1 {
		t.Fatalf("cache replacements=%d want only retry commit", replaces)
	}
	if state := startup.viewState("s1"); state.ReadState["c1"].HasUnread {
		t.Fatalf("retained state regressed read: %#v", state)
	}
	startup.mu.RLock()
	tracked := len(startup.contexts["s1"].localReadOverlays)
	startup.mu.RUnlock()
	if tracked != 0 {
		t.Fatalf("successful authoritative reconciliation retained %d local read overlays", tracked)
	}
	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Memberships) != 1 || loaded.Memberships[0].MsgCount != 3 || loaded.Memberships[0].MentionCount != 1 || loaded.Memberships[0].LastViewedAt != 10 {
		t.Fatalf("durable cache did not retain raw fetched membership: %#v", loaded.Memberships)
	}
}

func TestChannelViewedEqualDuplicateDuringFetchDoesNotClearLaterUnread(t *testing.T) {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	client.memberships["t1"][0].MsgCount = 5
	client.memberships["t1"][0].LastViewedAt = 100
	fetchCaptured := make(chan struct{})
	releaseFetch := make(chan struct{})
	client.currentUserHook = func(ctx context.Context) error {
		close(fetchCaptured)
		select {
		case <-releaseFetch:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	store := newMattermostEventDB(t, "s1", "c1")
	startup := reconciliationStartup(client, localReadSnapshot(true))
	done := make(chan error, 1)
	go func() {
		done <- startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
			Cache: store, Send: acknowledgeMattermostRefresh, Clock: time.Now,
			ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
		})
	}()
	waitMattermostEvent(t, fetchCaptured, "authoritative fetch")
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup, ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" }})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "later", ChannelID: "c1", CreatedAt: 200}})
	adapter.Handle(context.Background(), "s1", mattermost.ChannelViewedEvent{UserID: "u1", Updates: []mattermost.ChannelViewUpdate{{ChannelID: "c1", ViewedAt: 100, HasViewedAt: true}}})
	startup.mu.RLock()
	_, tracked := startup.contexts["s1"].localReadOverlays["c1"]
	startup.mu.RUnlock()
	if tracked {
		t.Fatal("equal viewed event recorded reconciliation overlay")
	}
	close(releaseFetch)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Membership.LastViewedAt != 100 || !service.ChannelHasUnread(entry) {
		t.Fatalf("equal viewed duplicate cleared later unread: %#v", entry)
	}
}

func TestReconciliationLocalReadDuringCacheCommitBelongsToNextOverlayEpoch(t *testing.T) {
	client := newLocalReadReconciliationClient()
	close(client.releaseFirst)
	db := newMattermostReconcileDB(t)
	store := &blockingLocalReadReconciliationStore{
		DB:      db,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	startup := reconciliationStartup(client, localReadSnapshot(false))
	read := newMattermostUIReadService(context.Background(), startup, nil)
	sent := false
	done := make(chan error, 1)
	go func() {
		done <- startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
			Cache: store,
			Send: func(msg tea.Msg) {
				sent = true
				acknowledgeMattermostRefresh(msg)
			},
			Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" },
		})
	}()
	waitMattermostEvent(t, store.entered, "reconciliation cache commit")
	readReturned := make(chan struct{})
	readDone := make(chan tea.Msg, 1)
	go func() {
		optimistic, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
		if optimistic.ReadState["c1"].HasUnread || cmd == nil {
			readDone <- fmt.Errorf("optimistic state=%#v cmd=%v", optimistic, cmd)
			close(readReturned)
			return
		}
		close(readReturned)
		readDone <- cmd()
	}()
	waitMattermostEvent(t, readReturned, "local read during cache commit")
	close(store.release)
	if err := waitMattermostEvent(t, done, "linearized cache commit"); err != nil {
		t.Fatalf("reconciliation error=%v", err)
	}
	if !sent {
		t.Fatal("authoritative reconciliation did not publish UI")
	}
	if msg := waitMattermostEvent(t, readDone, "read ordered after cache commit"); msg == nil {
		t.Fatal("read REST correction did not publish after authoritative commit")
	}
	if state := startup.viewState("s1"); state.ReadState["c1"].HasUnread {
		t.Fatalf("retained state regressed read: %#v", state)
	}
	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Memberships) != 1 || loaded.Memberships[0].MsgCount != 3 || loaded.Memberships[0].MentionCount != 1 || loaded.Memberships[0].LastViewedAt != 10 {
		t.Fatalf("durable cache did not retain authoritative unread commit: %#v", loaded.Memberships)
	}
}

func TestLocalReadReplayPreservesFetchedMembershipAuthority(t *testing.T) {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	client.memberships["t1"][0] = mattermost.ChannelMembership{
		ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 2, LastViewedAt: 40, UpdatedAt: 999,
	}
	startup := reconciliationStartup(client, localReadSnapshot(false))
	if _, ok := startup.optimisticallyViewChannel("s1", "c1"); !ok {
		t.Fatal("optimistic read failed")
	}

	if err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: newMattermostReconcileDB(t), Send: acknowledgeMattermostRefresh, Clock: time.Now,
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	}); err != nil {
		t.Fatal(err)
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Membership.ChannelID != "c1" || entry.Membership.UserID != "u1" || entry.Membership.UpdatedAt != 999 || entry.Membership.LastViewedAt != 40 {
		t.Fatalf("overlay replaced fetched authoritative fields: %#v", entry.Membership)
	}
	if entry.Membership.MsgCount != 5 || entry.Membership.MentionCount != 0 {
		t.Fatalf("overlay did not apply owned viewed boundary: %#v", entry.Membership)
	}
}

func TestReconciliationSuccessfulInstallConsumesLocalReadOverlayOnce(t *testing.T) {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	client.memberships["t1"][0] = mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 1}
	startup := reconciliationStartup(client, localReadSnapshot(false))
	if _, ok := startup.optimisticallyViewChannel("s1", "c1"); !ok {
		t.Fatal("optimistic read failed")
	}
	deps := mattermostReconcileDeps{Cache: newMattermostReconcileDB(t), Send: acknowledgeMattermostRefresh, Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" }}
	if err := startup.reconcile(context.Background(), "s1", deps); err != nil {
		t.Fatal(err)
	}
	if state := startup.viewState("s1"); state.ReadState["c1"].HasUnread {
		t.Fatal("first install did not replay overlay")
	}
	if err := startup.reconcile(context.Background(), "s1", deps); err != nil {
		t.Fatal(err)
	}
	if state := startup.viewState("s1"); !state.ReadState["c1"].HasUnread {
		t.Fatal("consumed overlay replayed into next authoritative install")
	}
}

func TestReconciliationFailedInstallRetainsLocalReadOverlay(t *testing.T) {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	client.memberships["t1"][0] = mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 1}
	startup := reconciliationStartup(client, localReadSnapshot(false))
	if _, ok := startup.optimisticallyViewChannel("s1", "c1"); !ok {
		t.Fatal("optimistic read failed")
	}
	failing := &failingReconciliationStore{DB: newMattermostReconcileDB(t)}
	err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{Cache: failing, Send: acknowledgeMattermostRefresh, Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" }})
	if err == nil {
		t.Fatal("expected persistence failure")
	}
	startup.mu.RLock()
	_, retained := startup.contexts["s1"].localReadOverlays["c1"]
	startup.mu.RUnlock()
	if !retained {
		t.Fatal("failed install consumed local read overlay")
	}
}

func TestReconciliationPostAppliedBeforeFetchUsesAuthoritativeSnapshot(t *testing.T) {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 0
	startup := reconciliationStartup(client, localReadSnapshot(true))
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           &atomicMattermostEventStore{inserted: true},
		Startup:         startup,
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
	})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})

	failing := &failingReconciliationStore{DB: newMattermostReconcileDB(t)}
	if err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{Cache: failing, Send: acknowledgeMattermostRefresh, Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" }}); err == nil {
		t.Fatal("expected persistence failure")
	}
	if err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{Cache: failing.DB, Send: acknowledgeMattermostRefresh, Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" }}); err != nil {
		t.Fatal(err)
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != client.channels[0].TotalMsgCount {
		t.Fatalf("pre-fetch post was not replaced by authoritative snapshot: %#v", entry)
	}
}

func TestReconciliationCoveredPostBoundaryDoesNotDoubleIncrementAndClearsJournal(t *testing.T) {
	for _, lastPostAt := range []int64{10, 11} {
		t.Run(fmt.Sprintf("last_post_at_%d", lastPostAt), func(t *testing.T) {
			client := reconciliationBootstrapClient()
			client.channels[0].LastPostAt = lastPostAt
			startup := reconciliationStartup(client, localReadSnapshot(true))
			store := newMattermostEventDB(t, "s1", "c1")
			adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: store, Startup: startup, ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" }})
			adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-1", ChannelID: "c1", CreatedAt: 10}})
			if err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{Cache: store, Send: acknowledgeMattermostRefresh, Clock: time.Now, ActiveSelection: func() (ids.ServerID, string) { return "", "" }}); err != nil {
				t.Fatal(err)
			}
			entry := unreadMattermostEntry(t, startup, "s1", "c1")
			if entry.Channel.TotalMsgCount != client.channels[0].TotalMsgCount {
				t.Fatalf("covered post was rebased: %#v", entry.Channel)
			}
			state := startup.reconcileState("s1")
			state.runtime.Lock()
			inFlight := state.inFlight
			state.runtime.Unlock()
			if inFlight != nil {
				t.Fatalf("authoritative commit retained journal: %#v", inFlight)
			}
		})
	}
}

func TestReconciliationLastPostAtRuntimeBoundaryRetainsDurableCoverage(t *testing.T) {
	db := newMattermostReconcileDB(t)
	initial := localReadSnapshot(true)
	initial.Sections[0].Channels[0].Channel.LastPostAt = 200
	if err := db.ReplaceMattermostBootstrapSnapshot(mattermostCacheSnapshot(initial, time.UnixMilli(200))); err != nil {
		t.Fatal(err)
	}

	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	client.channels[0].LastPostAt = 100
	client.memberships["t1"][0].MsgCount = 5
	post := mattermost.Message{ID: "post-150", ChannelID: "c1", UserID: "u2", Text: "history first", CreatedAt: 150}
	client.postPage = mattermost.MessagePage{Messages: []mattermost.Message{post}}
	if _, err := service.NewMattermostHistoryService("s1", client, db, 20).FetchRecent(context.Background(), "c1"); err != nil {
		t.Fatal(err)
	}

	startup := reconciliationStartup(client, initial)
	if err := startup.reconcile(context.Background(), "s1", mattermostReconcileDeps{
		Cache: db, Send: acknowledgeMattermostRefresh, Clock: func() time.Time { return time.UnixMilli(300) },
		ActiveSelection: func() (ids.ServerID, string) { return "", "" },
	}); err != nil {
		t.Fatal(err)
	}

	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Channels[0].LastPostAt != 200 {
		t.Fatalf("durable last_post_at=%d want 200", loaded.Channels[0].LastPostAt)
	}
	entry := unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.LastPostAt != 200 {
		t.Errorf("runtime last_post_at=%d want retained durable boundary 200", entry.Channel.LastPostAt)
	}
	beforeTotal, beforeViewed := entry.Channel.TotalMsgCount, entry.Membership.MsgCount

	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache: db, Startup: startup,
		ActiveSelection: func() (ids.ServerID, string) { return "s2", "other" },
	})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: post})
	entry = unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != beforeTotal || entry.Membership.MsgCount != beforeViewed {
		t.Errorf("covered websocket post changed counts to %d/%d want %d/%d", entry.Channel.TotalMsgCount, entry.Membership.MsgCount, beforeTotal, beforeViewed)
	}
	claimed, err := db.UpsertMattermostRealtimePostContext(context.Background(), "s1", cache.MattermostPost{ID: post.ID, ChannelID: post.ChannelID, UserID: post.UserID, Text: post.Text, CreatedAt: post.CreatedAt})
	if err != nil {
		t.Fatal(err)
	}
	if !claimed {
		t.Error("covered websocket post claimed the history row runtime transition")
	}
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: post})
	entry = unreadMattermostEntry(t, startup, "s1", "c1")
	if entry.Channel.TotalMsgCount != beforeTotal || entry.Membership.MsgCount != beforeViewed {
		t.Errorf("duplicate websocket post changed counts to %d/%d want %d/%d", entry.Channel.TotalMsgCount, entry.Membership.MsgCount, beforeTotal, beforeViewed)
	}
}

type reconciliationPostCall struct {
	channelID string
	options   mattermost.ChannelPostsOptions
}

type fakeReconcileUIHistory struct{}

func (*fakeReconcileUIHistory) ReadCached(ui.HistoryRequest, string) ([]messages.MessageItem, error) {
	return nil, nil
}

func (*fakeReconcileUIHistory) FetchRecent(context.Context, ui.HistoryRequest) ui.MattermostMessagesLoadedMsg {
	return ui.MattermostMessagesLoadedMsg{}
}

func (*fakeReconcileUIHistory) FetchOlder(context.Context, ui.HistoryRequest, string) ui.MattermostOlderMessagesLoadedMsg {
	return ui.MattermostOlderMessagesLoadedMsg{}
}

type reconciliationClient struct {
	currentUser     mattermost.User
	teams           []mattermost.Team
	channels        []mattermost.Channel
	memberships     map[string][]mattermost.ChannelMembership
	postPage        mattermost.MessagePage
	postErr         error
	postCalls       []reconciliationPostCall
	bootstrapErr    error
	internalToken   string
	currentUserHook func(context.Context) error
	postHook        func(context.Context) error
	mu              sync.Mutex
}

type localReadReconciliationClient struct {
	*reconciliationClient
	firstCaptured chan struct{}
	releaseFirst  chan struct{}
	mu            sync.Mutex
	attempt       int
}

func newLocalReadReconciliationClient() *localReadReconciliationClient {
	client := reconciliationBootstrapClient()
	client.channels[0].TotalMsgCount = 5
	return &localReadReconciliationClient{
		reconciliationClient: client,
		firstCaptured:        make(chan struct{}),
		releaseFirst:         make(chan struct{}),
	}
}

func (c *localReadReconciliationClient) CurrentUser(context.Context) (*mattermost.User, error) {
	c.mu.Lock()
	c.attempt++
	c.mu.Unlock()
	return &mattermost.User{ID: "u1"}, nil
}

func (c *localReadReconciliationClient) ChannelMembershipsForUser(ctx context.Context, _, _ string) ([]mattermost.ChannelMembership, error) {
	c.mu.Lock()
	attempt := c.attempt
	c.mu.Unlock()
	if attempt == 1 {
		close(c.firstCaptured)
		select {
		case <-c.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		return []mattermost.ChannelMembership{{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 1, LastViewedAt: 10}}, nil
	}
	return []mattermost.ChannelMembership{{ChannelID: "c1", UserID: "u1", MsgCount: 5, LastViewedAt: 100}}, nil
}

func (c *localReadReconciliationClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{LastViewedAtTimes: map[string]int64{"c1": 100}}, nil
}

func (c *localReadReconciliationClient) bootstrapAttempts() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attempt
}

func localReadSnapshot(read bool) service.ServerSnapshot {
	msgCount, mentionCount, viewedAt := int64(3), int64(1), int64(10)
	if read {
		msgCount, mentionCount, viewedAt = 5, 0, 100
	}
	return service.ServerSnapshot{
		Server: mattermost.Server{ID: "s1", Name: "One", URL: "https://one.example", UserID: "u1"}, CurrentUser: mattermost.User{ID: "u1"},
		Teams: []mattermost.Team{{ID: "t1"}},
		Sections: []service.ChannelSection{{ID: "t1", Channels: []service.ChannelEntry{{
			Channel:    mattermost.Channel{ID: "c1", TeamID: "t1", Kind: mattermost.ChannelKindPublic, TotalMsgCount: 5},
			Membership: &mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: msgCount, MentionCount: mentionCount, LastViewedAt: viewedAt},
		}}}},
	}
}

type countingReconciliationStore struct {
	*cache.DB
	mu       sync.Mutex
	replaces int
}

type blockingLocalReadReconciliationStore struct {
	*cache.DB
	entered chan struct{}
	release chan struct{}
}

func (s *blockingLocalReadReconciliationStore) ReplaceMattermostBootstrapSnapshotContext(ctx context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	close(s.entered)
	select {
	case <-s.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return s.DB.ReplaceMattermostBootstrapSnapshotContext(ctx, snapshot)
}

func (s *countingReconciliationStore) ReplaceMattermostBootstrapSnapshotContext(ctx context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	s.mu.Lock()
	s.replaces++
	s.mu.Unlock()
	return s.DB.ReplaceMattermostBootstrapSnapshotContext(ctx, snapshot)
}

func (s *countingReconciliationStore) replaceCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.replaces
}

func reconciliationBootstrapClient() *reconciliationClient {
	return &reconciliationClient{
		currentUser: mattermost.User{ID: "u1"},
		teams:       []mattermost.Team{{ID: "t1"}},
		channels:    []mattermost.Channel{{ID: "c1", TeamID: "t1", Kind: mattermost.ChannelKindPublic}},
		memberships: map[string][]mattermost.ChannelMembership{"t1": {{ChannelID: "c1", UserID: "u1"}}},
	}
}

func (c *reconciliationClient) CurrentUser(ctx context.Context) (*mattermost.User, error) {
	if c.currentUserHook != nil {
		if err := c.currentUserHook(ctx); err != nil {
			return nil, err
		}
	}
	if c.bootstrapErr != nil {
		return nil, c.bootstrapErr
	}
	user := c.currentUser
	return &user, nil
}

func (c *reconciliationClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return append([]mattermost.Team(nil), c.teams...), nil
}

func (c *reconciliationClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return append([]mattermost.Channel(nil), c.channels...), nil
}

func (c *reconciliationClient) ChannelMembershipsForUser(_ context.Context, _ string, teamID string) ([]mattermost.ChannelMembership, error) {
	return append([]mattermost.ChannelMembership(nil), c.memberships[teamID]...), nil
}

func (*reconciliationClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}

func (*reconciliationClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}

func (c *reconciliationClient) ChannelPosts(ctx context.Context, channelID string, options mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	if c.postHook != nil {
		if err := c.postHook(ctx); err != nil {
			return mattermost.MessagePage{}, err
		}
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.postCalls = append(c.postCalls, reconciliationPostCall{channelID: channelID, options: options})
	return c.postPage, c.postErr
}

func (*reconciliationClient) RunWebSocket(ctx context.Context, _ func(), _ func(mattermost.Event), _ func(error)) error {
	<-ctx.Done()
	return ctx.Err()
}

func (*reconciliationClient) CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error) {
	return mattermost.Message{}, errors.New("unused send")
}

func (*reconciliationClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, errors.New("unused view")
}

type orderedReconciliationStore struct {
	*cache.DB
	persisted bool
}

func (s *orderedReconciliationStore) ReplaceMattermostBootstrapSnapshot(snapshot cache.MattermostBootstrapSnapshot) error {
	if err := s.DB.ReplaceMattermostBootstrapSnapshot(snapshot); err != nil {
		return err
	}
	s.persisted = true
	return nil
}

func (s *orderedReconciliationStore) ReplaceMattermostBootstrapSnapshotContext(ctx context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	if err := s.DB.ReplaceMattermostBootstrapSnapshotContext(ctx, snapshot); err != nil {
		return err
	}
	s.persisted = true
	return nil
}

type failingReconciliationStore struct{ *cache.DB }

func (*failingReconciliationStore) ReplaceMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error {
	return errors.New("disk full")
}

func (*failingReconciliationStore) ReplaceMattermostBootstrapSnapshotContext(context.Context, cache.MattermostBootstrapSnapshot) error {
	return errors.New("disk full")
}

type blockingReconciliationStore struct {
	*cache.DB
	firstEntered chan struct{}
	releaseFirst chan struct{}
	once         sync.Once
}

func newBlockingReconciliationStore(t *testing.T) *blockingReconciliationStore {
	return &blockingReconciliationStore{DB: newMattermostReconcileDB(t), firstEntered: make(chan struct{}), releaseFirst: make(chan struct{})}
}

func (s *blockingReconciliationStore) ReplaceMattermostBootstrapSnapshot(snapshot cache.MattermostBootstrapSnapshot) error {
	if snapshot.Server.ID == "s1" {
		s.once.Do(func() { close(s.firstEntered) })
		<-s.releaseFirst
	}
	return s.DB.ReplaceMattermostBootstrapSnapshot(snapshot)
}

func (s *blockingReconciliationStore) ReplaceMattermostBootstrapSnapshotContext(ctx context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	if snapshot.Server.ID == "s1" {
		s.once.Do(func() { close(s.firstEntered) })
		select {
		case <-s.releaseFirst:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return s.DB.ReplaceMattermostBootstrapSnapshotContext(ctx, snapshot)
}

func reconciliationStartup(client mattermostStartupClient, snapshot service.ServerSnapshot) *mattermostStartup {
	return &mattermostStartup{contexts: map[ids.ServerID]mattermostServerContext{
		"s1": {
			server:   mattermost.Server{ID: "s1", Name: "One", URL: "https://one.example", UserID: "u1"},
			client:   client,
			snapshot: snapshot,
			usable:   snapshot.Server.ID != "",
		},
	}}
}

func acknowledgeMattermostRefresh(msg tea.Msg) {
	if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
		refresh.Applied.MarkApplied()
	}
}

func newMattermostReconcileDB(t *testing.T) *cache.DB {
	t.Helper()
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return db
}

func reconcileCacheTeamIDs(teams []cache.MattermostTeam) []string {
	ids := make([]string, len(teams))
	for i, team := range teams {
		ids[i] = team.ID
	}
	return ids
}

func reconcileCacheChannelIDs(channels []cache.MattermostChannel) []string {
	ids := make([]string, len(channels))
	for i, channel := range channels {
		ids[i] = channel.ID
	}
	return ids
}
