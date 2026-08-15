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
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

func TestMarkChannelReadOptimisticSnapshotAndReturnedTimesCorrection(t *testing.T) {
	client := &recordingViewClient{responses: []mattermost.ViewChannelResult{{LastViewedAtTimes: map[string]int64{"c1": 40}}}}
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
	read := newMattermostUIReadService(context.Background(), startup, nil)

	state, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	if state.ReadState["c1"].HasUnread || !state.ReadState["c2"].HasUnread {
		t.Fatalf("optimistic read state=%#v", state.ReadState)
	}
	before := startup.contexts["s1"].snapshot.Sections[0].Channels
	if before[0].Membership.MsgCount != before[0].Channel.TotalMsgCount || before[0].Membership.MentionCount != 0 || before[1].Membership.MsgCount == before[1].Channel.TotalMsgCount {
		t.Fatalf("optimistic snapshot=%#v", before)
	}

	msg := cmd()
	refresh, ok := msg.(ui.ServerRefreshedMsg)
	if !ok || refresh.Server.ReadState["c1"].HasUnread {
		t.Fatalf("correction=%#v", msg)
	}
	after := startup.contexts["s1"].snapshot.Sections[0].Channels
	if after[0].Membership.LastViewedAt != 40 {
		t.Fatalf("last viewed=%d", after[0].Membership.LastViewedAt)
	}
	if after[1].Membership.LastViewedAt != 20 || after[1].Membership.MsgCount == after[1].Channel.TotalMsgCount {
		t.Fatalf("absent response channel changed=%#v", after[1])
	}
}

func TestMarkChannelReadSerializesSameServerAndUsesSameServerPreviousOnly(t *testing.T) {
	client1 := newBlockingViewClient()
	client2 := newBlockingViewClient()
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client1, "s2": client2})
	read := newMattermostUIReadService(context.Background(), startup, nil)

	requests := []ui.MattermostReadRequest{
		{ServerID: "s1", ChannelID: "c1", PreviousServerID: "s1", PreviousChannelID: "c0"},
		{ServerID: "s1", ChannelID: "c2", PreviousServerID: "s1", PreviousChannelID: "c1"},
		{ServerID: "s1", ChannelID: "c3", PreviousServerID: "s1", PreviousChannelID: "c2"},
		{ServerID: "s2", ChannelID: "c1", PreviousServerID: "s1", PreviousChannelID: "c3"},
	}
	cmds := make([]tea.Cmd, len(requests))
	for i, request := range requests {
		_, cmds[i] = read.View(request)
	}
	results := make(chan tea.Msg, len(cmds))
	for _, cmd := range cmds {
		go func(cmd tea.Cmd) { results <- cmd() }(cmd)
	}

	first := waitViewCall(t, client1.calls, "first s1 call")
	if first.channelID != "c1" || first.previousChannelID != "c0" {
		t.Fatalf("first=%#v", first)
	}
	other := waitViewCall(t, client2.calls, "cross-server call")
	if other.channelID != "c1" || other.previousChannelID != "" {
		t.Fatalf("cross-server=%#v", other)
	}
	assertNoViewCall(t, client1.calls)
	close(first.release)
	second := waitViewCall(t, client1.calls, "second s1 call")
	if second.channelID != "c2" || second.previousChannelID != "c1" {
		t.Fatalf("second=%#v", second)
	}
	assertNoViewCall(t, client1.calls)
	close(second.release)
	third := waitViewCall(t, client1.calls, "third s1 call")
	if third.channelID != "c3" || third.previousChannelID != "c2" {
		t.Fatalf("third=%#v", third)
	}
	close(third.release)
	close(other.release)
	for range cmds {
		<-results
	}
}

func TestMarkChannelReadFailureRetainsOptimisticStateAndSanitizesDiagnostic(t *testing.T) {
	client := &recordingViewClient{errs: []error{errors.New("secret-token response payload")}}
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
	var diagnostic error
	read := newMattermostUIReadService(context.Background(), startup, func(err error) { diagnostic = err })

	state, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	if state.ReadState["c1"].HasUnread {
		t.Fatal("optimistic state was not cleared")
	}
	if msg := cmd(); msg != nil {
		t.Fatalf("failure returned UI correction %#v", msg)
	}
	retained := startup.viewState("s1")
	if retained.ReadState["c1"].HasUnread {
		t.Fatal("REST failure reverted optimistic state")
	}
	if diagnostic == nil || diagnostic.Error() != "Mattermost channel view request failed" {
		t.Fatalf("diagnostic=%v", diagnostic)
	}
}

func TestMarkChannelReadCorrectionDoesNotClearPostArrivingAfterOptimisticView(t *testing.T) {
	client := newBlockingViewClient()
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
	read := newMattermostUIReadService(context.Background(), startup, nil)

	_, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()
	call := waitViewCall(t, client.calls, "view request before later post")
	if _, changed := startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s1", "c2", true); !changed {
		t.Fatal("post did not update retained snapshot")
	}
	close(call.release)
	msg := waitMattermostEvent(t, result, "view correction after later post")
	refresh, ok := msg.(ui.ServerRefreshedMsg)
	if !ok {
		t.Fatalf("correction=%#v", msg)
	}
	if !refresh.Server.ReadState["c1"].HasUnread {
		t.Fatal("late correction cleared post that arrived after view")
	}
	entry := startup.contexts["s1"].snapshot.Sections[0].Channels[0]
	if entry.Membership.LastViewedAt != 100 || entry.Membership.MsgCount == entry.Channel.TotalMsgCount {
		t.Fatalf("corrected entry=%#v", entry)
	}
}

func TestMarkChannelReadOlderRESTCorrectionDoesNotRegressNewerRealtimeUI(t *testing.T) {
	client := &recordingViewClient{responses: []mattermost.ViewChannelResult{{LastViewedAtTimes: map[string]int64{"c1": 40}}}}
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
	read := newMattermostUIReadService(context.Background(), startup, nil)
	app := ui.NewApp()
	app.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", Name: "One"}})
	_, _ = app.Update(tea.WindowSizeMsg{Width: 120, Height: 30})
	initialState := startup.viewState("s1")
	initialState.InitialActive = true
	_, selectInitial := app.Update(ui.ServerReadyMsg{Server: initialState})
	if selectInitial != nil {
		for _, msg := range drainMattermostCmd(selectInitial) {
			if selected, ok := msg.(ui.ChannelSelectedMsg); ok {
				_, _ = app.Update(selected)
			}
		}
	}
	app.SetInitialChannel("c2", "c2", []messages.MessageItem{{ID: "visible"}})
	app.SetMattermostReadService(ui.NewMattermostReadService(read.View))

	_, selectionCmd := app.Update(ui.ChannelSelectedMsg{ID: "c1", Name: "c1", Type: "channel"})
	var oldCorrection ui.ServerRefreshedMsg
	for _, msg := range drainMattermostCmd(selectionCmd) {
		if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
			oldCorrection = refresh
		}
	}
	if oldCorrection.Server.Revision == 0 {
		t.Fatalf("old correction missing revision: %#v", oldCorrection)
	}

	var realtime ui.ServerRefreshedMsg
	adapter := newMattermostEventAdapter(mattermostEventDeps{
		Cache:           &atomicMattermostEventStore{inserted: true},
		Send:            func(_ context.Context, msg tea.Msg) error { realtime, _ = msg.(ui.ServerRefreshedMsg); return nil },
		ActiveSelection: func() (ids.ServerID, string) { return "s1", "c1" },
		Startup:         startup,
	})
	adapter.Handle(context.Background(), "s1", mattermost.PostedEvent{Message: mattermost.Message{ID: "post-new", ChannelID: "c3", CreatedAt: 10}})
	if realtime.Server.Revision <= oldCorrection.Server.Revision {
		t.Fatalf("realtime revision=%d old correction=%d", realtime.Server.Revision, oldCorrection.Server.Revision)
	}
	_, _ = app.Update(realtime)
	before := app.ActiveChannelID()
	beforeScreen := app.View().Content
	_, _ = app.Update(oldCorrection)

	if app.ActiveChannelID() != before || app.ActiveChannelID() != "c1" {
		t.Fatalf("stale correction regressed selection: before=%q after=%q", before, app.ActiveChannelID())
	}
	canonical := startup.viewState("s1")
	if !canonical.ReadState["c3"].HasUnread {
		t.Fatal("retained state missing newer realtime unread")
	}
	if oldCorrection.Server.ReadState["c3"].HasUnread {
		t.Fatal("test did not capture older correction state")
	}
	if afterScreen := app.View().Content; afterScreen != beforeScreen {
		t.Fatalf("stale correction regressed rendered sidebar/rail/history:\nbefore:\n%s\nafter:\n%s", beforeScreen, afterScreen)
	}
}

func TestMarkChannelReadRejectsInvalidRetainedSelectionWithoutREST(t *testing.T) {
	for _, tt := range []struct {
		name   string
		mutate func(*mattermostStartup)
	}{
		{name: "unknown channel"},
		{name: "nil membership", mutate: func(startup *mattermostStartup) {
			startup.contexts["s1"].snapshot.Sections[0].Channels[0].Membership = nil
		}},
		{name: "membership user mismatch", mutate: func(startup *mattermostStartup) {
			startup.contexts["s1"].snapshot.Sections[0].Channels[0].Membership.UserID = "u2"
		}},
		{name: "missing current user", mutate: func(startup *mattermostStartup) {
			serverContext := startup.contexts["s1"]
			serverContext.snapshot.CurrentUser.ID = ""
			startup.contexts["s1"] = serverContext
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			client := &recordingViewClient{}
			startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
			if tt.mutate != nil {
				tt.mutate(startup)
			}
			channelID := "c1"
			if tt.name == "unknown channel" {
				channelID = "missing"
			}
			state, cmd := newMattermostUIReadService(context.Background(), startup, nil).View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: channelID})
			if !reflect.DeepEqual(state, ui.ServerViewState{}) || cmd != nil {
				t.Fatalf("invalid selection returned state=%#v cmd=%v", state, cmd)
			}
			if client.callCount() != 0 {
				t.Fatalf("invalid selection issued %d REST calls", client.callCount())
			}
		})
	}
}

func TestMarkChannelReadKnownAlreadyReadStillIssuesIdempotentREST(t *testing.T) {
	client := &recordingViewClient{responses: []mattermost.ViewChannelResult{{LastViewedAtTimes: map[string]int64{"c3": 40}}}}
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": client})
	state, cmd := newMattermostUIReadService(context.Background(), startup, nil).View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c3"})
	if state.ServerID != "s1" || cmd == nil {
		t.Fatalf("already-read selection state=%#v cmd=%v", state, cmd)
	}
	_ = cmd()
	if client.callCount() != 1 {
		t.Fatalf("REST calls=%d want 1", client.callCount())
	}
}

func TestMarkChannelReadCachedStartupClearsBeforeClientAndViewsAfterInstall(t *testing.T) {
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": nil})
	read := newMattermostUIReadService(context.Background(), startup, nil)

	state, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	if state.ServerID != "s1" || state.ReadState["c1"].HasUnread || cmd == nil {
		t.Fatalf("cached selection state=%#v cmd=%v", state, cmd)
	}
	optimisticRevision := state.Revision
	client := newBlockingViewClient()
	result := make(chan tea.Msg, 1)
	go func() { result <- cmd() }()

	startup.setClient("s1", client)
	call := waitViewCall(t, client.calls, "cached read after client install")
	if call.channelID != "c1" || call.previousChannelID != "" {
		t.Fatalf("call=%#v", call)
	}
	close(call.release)
	msg := waitMattermostEvent(t, result, "cached read correction")
	refresh, ok := msg.(ui.ServerRefreshedMsg)
	if !ok || refresh.Server.Revision <= optimisticRevision {
		t.Fatalf("correction=%#v optimistic revision=%d", msg, optimisticRevision)
	}
}

func TestMarkChannelReadDuringBootstrapInstallPreservesOnlyLocalOverlay(t *testing.T) {
	registry := config.NewServerRegistry()
	registry.Servers = []config.MattermostServer{{ID: "s1", URL: "https://one.example", UserID: "u1"}}
	client := newBootstrapOverlayClient()
	store := &bootstrapOverlaySnapshotStore{snapshot: unreadCachedMattermostSnapshot()}
	messages := make(chan tea.Msg, 16)
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry:  registry,
		Secrets:   fakeMattermostSecrets{tokens: map[string]string{"s1": "token"}},
		NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) { return client, nil },
		Cache:     store,
		Send:      func(msg tea.Msg) { messages <- msg },
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { startup.Cancel(); startup.Wait() })
	waitMattermostEvent(t, client.bootstrapCaptured, "live bootstrap unread snapshot")

	var diagnostics int
	read := newMattermostUIReadService(context.Background(), startup, func(error) { diagnostics++ })
	optimistic, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: "c1"})
	if optimistic.ReadState["c1"].HasUnread || cmd == nil {
		t.Fatalf("optimistic state=%#v cmd=%v", optimistic, cmd)
	}
	readDone := make(chan tea.Msg, 1)
	go func() { readDone <- cmd() }()
	close(client.releaseBootstrap)

	var live ui.ServerRefreshedMsg
	for live.Server.ServerID == "" {
		msg := waitMattermostEvent(t, messages, "overlay-preserving live refresh")
		if refresh, ok := msg.(ui.ServerRefreshedMsg); ok {
			live = refresh
		}
	}
	if live.Server.ReadState["c1"].HasUnread {
		t.Fatalf("live install regressed local read: %#v", live.Server.ReadState)
	}
	if live.Server.ReadState["c2"].HasUnread {
		t.Fatalf("unrelated channel did not use authoritative live state: %#v", live.Server.ReadState)
	}
	if msg := waitMattermostEvent(t, readDone, "queued read REST failure"); msg != nil {
		t.Fatalf("failed read returned correction %#v", msg)
	}
	if diagnostics != 1 {
		t.Fatalf("read diagnostics=%d want 1", diagnostics)
	}
	if state := startup.viewState("s1"); state.ReadState["c1"].HasUnread || state.ReadState["c2"].HasUnread {
		t.Fatalf("retained state lost overlay or authority: %#v", state.ReadState)
	}
	startup.mu.RLock()
	_, tracked := startup.contexts["s1"].localReadOverlays["c1"]
	_, otherTracked := startup.contexts["s1"].localReadOverlays["c2"]
	startup.mu.RUnlock()
	if tracked || otherTracked {
		t.Fatalf("tracked local reads: c1=%v c2=%v", tracked, otherTracked)
	}
	persisted := store.persistedSnapshot()
	if len(persisted.Memberships) != 2 {
		t.Fatalf("persisted memberships=%#v", persisted.Memberships)
	}
	for _, membership := range persisted.Memberships {
		switch membership.ChannelID {
		case "c1":
			if membership.MsgCount != 3 || membership.MentionCount != 1 || membership.LastViewedAt != 10 {
				t.Fatalf("durable cache received optimistic c1 overlay: %#v", membership)
			}
		case "c2":
			if membership.MsgCount != 7 || membership.MentionCount != 0 || membership.LastViewedAt != 70 {
				t.Fatalf("durable cache missing authoritative c2 state: %#v", membership)
			}
		}
	}
}

func TestMarkChannelReadCachedStartupQueuesSameServerReadsUntilClientInstall(t *testing.T) {
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": nil})
	read := newMattermostUIReadService(context.Background(), startup, nil)
	requests := []ui.MattermostReadRequest{
		{ServerID: "s1", ChannelID: "c1"},
		{ServerID: "s1", ChannelID: "c2", PreviousServerID: "s1", PreviousChannelID: "c1"},
		{ServerID: "s1", ChannelID: "c3", PreviousServerID: "s1", PreviousChannelID: "c2"},
	}
	results := make(chan tea.Msg, len(requests))
	for _, request := range requests {
		_, cmd := read.View(request)
		if cmd == nil {
			t.Fatalf("request %#v returned nil command", request)
		}
		go func() { results <- cmd() }()
	}

	client := newBlockingViewClient()
	startup.setClient("s1", client)
	for i, want := range []viewCall{{channelID: "c1"}, {channelID: "c2", previousChannelID: "c1"}, {channelID: "c3", previousChannelID: "c2"}} {
		call := waitViewCall(t, client.calls, "queued cached read")
		if call.channelID != want.channelID || call.previousChannelID != want.previousChannelID {
			t.Fatalf("call %d=%#v want channel=%q prev=%q", i, call, want.channelID, want.previousChannelID)
		}
		close(call.release)
	}
	for range requests {
		<-results
	}
	if client.callCount() != len(requests) {
		t.Fatalf("REST calls=%d want %d", client.callCount(), len(requests))
	}
}

func TestMarkChannelReadCachedStartupCancellationBeforeClientReleasesQueuedCommands(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": nil})
	var diagnostics int
	read := newMattermostUIReadService(ctx, startup, func(error) { diagnostics++ })
	results := make(chan tea.Msg, 2)
	for _, channelID := range []string{"c1", "c2"} {
		_, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: channelID})
		if cmd == nil {
			t.Fatalf("cached %s command=nil", channelID)
		}
		go func() { results <- cmd() }()
	}
	cancel()
	for range 2 {
		if msg := waitMattermostEvent(t, results, "canceled cached read"); msg != nil {
			t.Fatalf("canceled command returned %#v", msg)
		}
	}
	client := &recordingViewClient{}
	startup.setClient("s1", client)
	if client.callCount() != 0 || diagnostics != 0 {
		t.Fatalf("calls=%d diagnostics=%d", client.callCount(), diagnostics)
	}
}

func TestMarkChannelReadCachedStartupTerminalFailureReleasesQueuedCommands(t *testing.T) {
	const sentinel = "raw-secret-terminal-sentinel"
	for _, tt := range []struct {
		name       string
		secretErr  error
		clientErr  error
		bootstrap  error
		persistErr error
		want       string
	}{
		{name: "credential", secretErr: errors.New(sentinel), want: "Mattermost credential unavailable"},
		{name: "client", clientErr: errors.New(sentinel), want: "Mattermost client initialization failed"},
		{name: "bootstrap", bootstrap: errors.New(sentinel), want: "Mattermost bootstrap failed"},
		{name: "persist", persistErr: errors.New(sentinel), want: "Mattermost bootstrap persistence failed"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			registry := config.NewServerRegistry()
			registry.Servers = []config.MattermostServer{{ID: "s1", URL: "https://one.example", UserID: "u1"}}
			secrets := &gatedTerminalFailureSecrets{entered: make(chan struct{}), release: make(chan struct{}), err: tt.secretErr}
			store := &terminalFailureSnapshotStore{snapshot: unreadCachedMattermostSnapshot(), persistErr: tt.persistErr}
			client := &terminalFailureStartupClient{reconciliationClient: reconciliationBootstrapClient()}
			client.bootstrapErr = tt.bootstrap
			var messagesMu sync.Mutex
			var stateErrors []error
			stateError := make(chan struct{})
			var stateErrorOnce sync.Once
			startup, err := startMattermost(context.Background(), mattermostStartupDeps{
				Registry: registry,
				Secrets:  secrets,
				NewClient: func(mattermost.Server, string) (mattermostStartupClient, error) {
					if tt.clientErr != nil {
						return nil, tt.clientErr
					}
					return client, nil
				},
				Cache: store,
				Send: func(msg tea.Msg) {
					if state, ok := msg.(ui.ServerStateMsg); ok && state.State == workspace.ItemStateError {
						messagesMu.Lock()
						stateErrors = append(stateErrors, state.Err)
						messagesMu.Unlock()
						stateErrorOnce.Do(func() { close(stateError) })
					}
				},
			})
			if err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { startup.Cancel(); startup.Wait() })
			waitMattermostEvent(t, secrets.entered, "terminal startup failure gate")

			var readDiagnostics int
			read := newMattermostUIReadService(context.Background(), startup, func(error) { readDiagnostics++ })
			results := make(chan tea.Msg, 3)
			for _, channelID := range []string{"c1", "c2", "c1"} {
				_, cmd := read.View(ui.MattermostReadRequest{ServerID: "s1", ChannelID: channelID})
				if cmd == nil {
					t.Fatalf("cached %s command=nil", channelID)
				}
				go func() { results <- cmd() }()
			}
			close(secrets.release)
			for range 3 {
				if msg := waitMattermostEvent(t, results, "terminal cached read completion"); msg != nil {
					t.Fatalf("terminal command returned %#v", msg)
				}
			}
			waitMattermostEvent(t, stateError, "terminal startup diagnostic")

			if calls := client.viewCallCount(); calls != 0 {
				t.Fatalf("ViewChannel calls=%d want 0", calls)
			}
			if readDiagnostics != 0 {
				t.Fatalf("read diagnostics=%d want 0", readDiagnostics)
			}
			messagesMu.Lock()
			defer messagesMu.Unlock()
			if len(stateErrors) != 1 || stateErrors[0] == nil || stateErrors[0].Error() != tt.want {
				t.Fatalf("startup diagnostics=%v want one %q", stateErrors, tt.want)
			}
			if strings.Contains(stateErrors[0].Error(), sentinel) {
				t.Fatalf("startup diagnostic exposed raw failure: %v", stateErrors[0])
			}
		})
	}
}

func TestMarkChannelReadOverlayTracksOnlyOwnedChangedFields(t *testing.T) {
	startup := readTestStartup(map[ids.ServerID]mattermostStartupClient{"s1": &recordingViewClient{}})
	overlays := func() map[string]mattermostLocalReadOverlay {
		startup.mu.RLock()
		defer startup.mu.RUnlock()
		return startup.contexts["s1"].localReadOverlays
	}

	if _, changed := startup.updateRuntimeEvent("s1", mattermost.PostedEvent{Message: mattermost.Message{ChannelID: "c1"}}, "s2", "other", true); !changed {
		t.Fatal("realtime setup did not change retained state")
	}
	if got := len(overlays()); got != 0 {
		t.Fatalf("realtime tracked local reads=%d want 0", got)
	}
	if _, ok := startup.optimisticallyViewChannel("s1", "c1"); !ok {
		t.Fatal("optimistic view rejected known unread channel")
	}
	if got := overlays()["c1"]; got.ViewedMsgCount != 6 || got.HasLastViewedAt {
		t.Fatalf("optimistic overlay=%#v", got)
	}
	if _, ok := startup.optimisticallyViewChannel("s1", "c1"); !ok {
		t.Fatal("no-op view rejected known read channel")
	}
	if got := len(overlays()); got != 1 {
		t.Fatalf("no-op tracked local reads=%d want 1", got)
	}
	if _, changed := startup.applyViewChannelTimes("s1", map[string]int64{"c1": 100}, map[string]int64{"c1": 5}); !changed {
		t.Fatal("REST correction did not change retained viewed time")
	}
	if got := overlays()["c1"]; got.ViewedMsgCount != 6 || !got.HasLastViewedAt || got.LastViewedAt != 100 {
		t.Fatalf("REST correction overlay=%#v", got)
	}
}

func TestLocalReadReplayNewerFetchedPostsPreserveMentions(t *testing.T) {
	snapshot := localReadSnapshot(false)
	snapshot.Sections[0].Channels[0].Channel.TotalMsgCount = 6
	snapshot.Sections[0].Channels[0].Membership.MsgCount = 4
	snapshot.Sections[0].Channels[0].Membership.MentionCount = 3
	overlay := map[string]mattermostLocalReadOverlay{"c1": {ViewedMsgCount: 5}}

	entry, _ := mattermostSnapshotChannel(replayMattermostLocalReads(snapshot, overlay), "c1")
	if entry.Membership.MsgCount != 5 || entry.Membership.MentionCount != 3 {
		t.Fatalf("newer fetched posts lost mentions: %#v", entry.Membership)
	}
}

func TestLocalReadReplayBoundaryClearsMentions(t *testing.T) {
	snapshot := localReadSnapshot(false)
	overlay := map[string]mattermostLocalReadOverlay{"c1": {ViewedMsgCount: 5}}

	entry, _ := mattermostSnapshotChannel(replayMattermostLocalReads(snapshot, overlay), "c1")
	if entry.Membership.MsgCount != 5 || entry.Membership.MentionCount != 0 {
		t.Fatalf("viewed boundary did not clear mentions: %#v", entry.Membership)
	}
}

func TestLocalReadReplayRaisesLastViewedAtOnlyWithRESTOwnership(t *testing.T) {
	snapshot := localReadSnapshot(false)
	entry, _ := mattermostSnapshotChannel(replayMattermostLocalReads(snapshot, map[string]mattermostLocalReadOverlay{"c1": {ViewedMsgCount: 5}}), "c1")
	if entry.Membership.LastViewedAt != 10 {
		t.Fatalf("optimistic overlay owned timestamp: %#v", entry.Membership)
	}
	entry, _ = mattermostSnapshotChannel(replayMattermostLocalReads(snapshot, map[string]mattermostLocalReadOverlay{"c1": {ViewedMsgCount: 5, LastViewedAt: 100, HasLastViewedAt: true}}), "c1")
	if entry.Membership.LastViewedAt != 100 {
		t.Fatalf("REST-owned timestamp was not replayed: %#v", entry.Membership)
	}
}

type gatedTerminalFailureSecrets struct {
	entered chan struct{}
	release chan struct{}
	err     error
}

func (s *gatedTerminalFailureSecrets) Get(ctx context.Context, _ string) (string, error) {
	close(s.entered)
	select {
	case <-s.release:
		return "token", s.err
	case <-ctx.Done():
		return "", ctx.Err()
	}
}

type terminalFailureSnapshotStore struct {
	snapshot   cache.MattermostBootstrapSnapshot
	persistErr error
}

func (s *terminalFailureSnapshotStore) LoadMattermostBootstrapSnapshot(string) (cache.MattermostBootstrapSnapshot, error) {
	if s.snapshot.Server.ID == "" {
		return cache.MattermostBootstrapSnapshot{}, sql.ErrNoRows
	}
	return s.snapshot, nil
}

func (*terminalFailureSnapshotStore) ApplyMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error {
	return nil
}

func (s *terminalFailureSnapshotStore) ReplaceMattermostBootstrapSnapshotContext(context.Context, cache.MattermostBootstrapSnapshot) error {
	return s.persistErr
}

type terminalFailureStartupClient struct {
	*reconciliationClient
	mu        sync.Mutex
	viewCalls int
}

type bootstrapOverlayClient struct {
	*reconciliationClient
	bootstrapCaptured chan struct{}
	releaseBootstrap  chan struct{}
}

func newBootstrapOverlayClient() *bootstrapOverlayClient {
	client := reconciliationBootstrapClient()
	client.channels = []mattermost.Channel{
		{ID: "c1", TeamID: "t1", Kind: mattermost.ChannelKindPublic, TotalMsgCount: 5},
		{ID: "c2", TeamID: "t1", Kind: mattermost.ChannelKindPublic, TotalMsgCount: 7},
	}
	return &bootstrapOverlayClient{
		reconciliationClient: client,
		bootstrapCaptured:    make(chan struct{}),
		releaseBootstrap:     make(chan struct{}),
	}
}

func (c *bootstrapOverlayClient) ChannelMembershipsForUser(ctx context.Context, _, _ string) ([]mattermost.ChannelMembership, error) {
	close(c.bootstrapCaptured)
	select {
	case <-c.releaseBootstrap:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []mattermost.ChannelMembership{
		{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 1, LastViewedAt: 10},
		{ChannelID: "c2", UserID: "u1", MsgCount: 7, LastViewedAt: 70},
	}, nil
}

func (*bootstrapOverlayClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, errors.New("view failed")
}

type bootstrapOverlaySnapshotStore struct {
	mu        sync.Mutex
	snapshot  cache.MattermostBootstrapSnapshot
	persisted cache.MattermostBootstrapSnapshot
}

func (s *bootstrapOverlaySnapshotStore) LoadMattermostBootstrapSnapshot(string) (cache.MattermostBootstrapSnapshot, error) {
	return s.snapshot, nil
}

func (*bootstrapOverlaySnapshotStore) ApplyMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error {
	return nil
}

func (s *bootstrapOverlaySnapshotStore) ReplaceMattermostBootstrapSnapshotContext(_ context.Context, snapshot cache.MattermostBootstrapSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.persisted = snapshot
	return nil
}

func (s *bootstrapOverlaySnapshotStore) persistedSnapshot() cache.MattermostBootstrapSnapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.persisted
}

func (c *terminalFailureStartupClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.viewCalls++
	return mattermost.ViewChannelResult{}, nil
}

func (c *terminalFailureStartupClient) viewCallCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.viewCalls
}

func unreadCachedMattermostSnapshot() cache.MattermostBootstrapSnapshot {
	return cache.MattermostBootstrapSnapshot{
		Server:      cache.MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1"},
		CurrentUser: cache.MattermostUser{ID: "u1"},
		Teams:       []cache.MattermostTeam{{ID: "t1", DisplayName: "Team"}},
		Channels: []cache.MattermostChannel{
			{ID: "c1", TeamID: "t1", Kind: "public", TotalMsgCount: 5},
			{ID: "c2", TeamID: "t1", Kind: "public", TotalMsgCount: 7},
		},
		Memberships: []cache.MattermostChannelMembership{
			{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 1, LastViewedAt: 10},
			{ChannelID: "c2", UserID: "u1", MsgCount: 6, MentionCount: 1, LastViewedAt: 20},
		},
	}
}

type viewCall struct {
	userID, channelID, previousChannelID string
	release                              chan struct{}
}

type blockingViewClient struct {
	mattermostStartupClient
	calls chan viewCall
	mu    sync.Mutex
	count int
}

func newBlockingViewClient() *blockingViewClient {
	return &blockingViewClient{calls: make(chan viewCall, 4)}
}

func (c *blockingViewClient) ViewChannel(ctx context.Context, userID, channelID, previousChannelID string) (mattermost.ViewChannelResult, error) {
	c.mu.Lock()
	c.count++
	c.mu.Unlock()
	call := viewCall{userID: userID, channelID: channelID, previousChannelID: previousChannelID, release: make(chan struct{})}
	c.calls <- call
	select {
	case <-call.release:
		return mattermost.ViewChannelResult{LastViewedAtTimes: map[string]int64{channelID: 100}}, nil
	case <-ctx.Done():
		return mattermost.ViewChannelResult{}, ctx.Err()
	}
}

func (c *blockingViewClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.count
}

type recordingViewClient struct {
	mattermostStartupClient
	mu        sync.Mutex
	responses []mattermost.ViewChannelResult
	errs      []error
	calls     int
}

func (c *recordingViewClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	var result mattermost.ViewChannelResult
	var err error
	if len(c.responses) > 0 {
		result, c.responses = c.responses[0], c.responses[1:]
	}
	if len(c.errs) > 0 {
		err, c.errs = c.errs[0], c.errs[1:]
	}
	return result, err
}

func (c *recordingViewClient) callCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func readTestStartup(clients map[ids.ServerID]mattermostStartupClient) *mattermostStartup {
	contexts := make(map[ids.ServerID]mattermostServerContext, len(clients))
	for serverID, client := range clients {
		entries := []service.ChannelEntry{
			{Channel: mattermost.Channel{ID: "c1", TotalMsgCount: 5}, Membership: &mattermost.ChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: 3, MentionCount: 2, LastViewedAt: 10}},
			{Channel: mattermost.Channel{ID: "c2", TotalMsgCount: 7}, Membership: &mattermost.ChannelMembership{ChannelID: "c2", UserID: "u1", MsgCount: 6, LastViewedAt: 20}},
			{Channel: mattermost.Channel{ID: "c3", TotalMsgCount: 1}, Membership: &mattermost.ChannelMembership{ChannelID: "c3", UserID: "u1", MsgCount: 1, LastViewedAt: 30}},
		}
		contexts[serverID] = mattermostServerContext{server: mattermost.Server{ID: string(serverID)}, client: client, usable: true, snapshot: service.ServerSnapshot{
			Server: mattermost.Server{ID: string(serverID)}, CurrentUser: mattermost.User{ID: "u1"}, Sections: []service.ChannelSection{{ID: "t1", Channels: entries}},
		}}
	}
	return &mattermostStartup{contexts: contexts}
}

func drainMattermostCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var messages []tea.Msg
		for _, next := range batch {
			messages = append(messages, drainMattermostCmd(next)...)
		}
		return messages
	}
	return []tea.Msg{msg}
}

func waitViewCall(t *testing.T, calls <-chan viewCall, label string) viewCall {
	t.Helper()
	select {
	case call := <-calls:
		return call
	case <-time.After(time.Second):
		t.Fatalf("timed out waiting for %s", label)
		return viewCall{}
	}
}

func assertNoViewCall(t *testing.T, calls <-chan viewCall) {
	t.Helper()
	select {
	case call := <-calls:
		t.Fatalf("unexpected concurrent call %#v", call)
	case <-time.After(25 * time.Millisecond):
	}
}
