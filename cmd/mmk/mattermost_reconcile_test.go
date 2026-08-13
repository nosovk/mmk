package main

import (
	"context"
	"errors"
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
	if err := db.UpsertMattermostPost("s1", cache.MattermostPost{ID: "retained-post", ChannelID: "retired-channel", Text: "retained"}); err != nil {
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
		postPage: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "recent", ChannelID: "active-channel", UserID: "u1", Text: "recent"}}},
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
	client.postPage = mattermost.MessagePage{Messages: []mattermost.Message{{ID: "stale", ChannelID: "c1", Text: "stale result"}}}
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

	startup.setClient("s1", newClient)
	newInvoked := make(chan struct{})
	newDone := make(chan error, 1)
	go func() {
		close(newInvoked)
		newDone <- startup.reconcile(context.Background(), "s1", deps)
	}()
	<-newInvoked
	select {
	case <-newRESTEntered:
		t.Fatal("new reconciliation entered REST while prior apply was in progress")
	case <-time.After(20 * time.Millisecond):
	}

	close(store.releaseFirst)
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

func TestReconciliationDifferentServersDoNotShareCommitLock(t *testing.T) {
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
	go func() { secondDone <- startup.reconcile(context.Background(), "s2", deps) }()
	select {
	case err := <-secondDone:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("different server blocked by first server reconciliation")
	}
	close(store.releaseFirst)
	if err := <-firstDone; err != nil {
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
