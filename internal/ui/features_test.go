package ui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/help"
	"github.com/nosovk/mmk/internal/ui/messages"
)

func TestMattermostFeatureGatesActionsAndHelpWhileSlackDefaultsRemainEnabled(t *testing.T) {
	legacy := NewApp()
	if !legacy.features.Allows(FeatureReactions) || !helpContains(help.FromKeyMap(legacy.keys), "add reaction") {
		t.Fatal("legacy Slack defaults must retain reactions")
	}

	mattermostApp := NewApp()
	mattermostApp.Update(ServerReadyMsg{Server: ServerViewState{ServerID: ids.ServerID("s1"), InitialActive: true}})
	if mattermostApp.features.Allows(FeatureReactions) {
		t.Fatal("Mattermost reactions must be disabled")
	}
	if cmd := handleNormalMode(mattermostApp, tea.KeyPressMsg{Code: 'r', Text: "r"}); cmd != nil || mattermostApp.mode != ModeNormal {
		t.Fatal("disabled reaction action crossed operation boundary")
	}
	entries := mattermostApp.helpEntries()
	for _, hidden := range []string{"add reaction", "edit message", "delete message", "mark unread", "search workspace", "new message", "copy permalink", "set status"} {
		if helpContains(entries, hidden) {
			t.Fatalf("Mattermost help exposes %q", hidden)
		}
	}
}

func TestMattermostReducerOperationBoundariesNoOp(t *testing.T) {
	app := NewApp()
	app.Update(ServerReadyMsg{Server: ServerViewState{ServerID: "s1", InitialActive: true}})
	beforeMode := app.mode
	for _, msg := range []tea.Msg{
		EditMessageMsg{ChannelID: "c1", TS: "1", NewText: "no"},
		DeleteMessageMsg{ChannelID: "c1", TS: "1"},
		MarkUnreadMsg{ChannelID: "c1", BoundaryTS: "1"},
		EnterNewMessageMsg{},
	} {
		if _, cmd := app.Update(msg); cmd != nil {
			t.Fatalf("%T returned command", msg)
		}
	}
	if app.mode != beforeMode || app.view == ViewThreads || app.threadVisible {
		t.Fatalf("disabled operation mutated UI: mode=%v view=%v thread=%v", app.mode, app.view, app.threadVisible)
	}
	for _, visible := range []string{"toggle thread", "save thread"} {
		if !helpContains(app.helpEntries(), visible) {
			t.Fatalf("Mattermost help hides enabled action %q", visible)
		}
	}
}

func TestMattermostChannelSelectionsUpdateContextWithoutLegacyFetchOrLoading(t *testing.T) {
	app := NewApp()
	app.Update(ServerReadyMsg{Server: ServerViewState{ServerID: "s1", InitialActive: true}})
	var calls []string
	app.SetChannelService(NewChannelService(ChannelServiceFuncs{
		ReadCache:       func(ids.ChannelID) []messages.MessageItem { calls = append(calls, "cache"); return nil },
		Fetch:           func(ids.ChannelID, string) tea.Msg { calls = append(calls, "fetch"); return nil },
		RecordVisit:     func(ids.ChannelID) { calls = append(calls, "visit") },
		MembershipFetch: func(ids.ChannelID) { calls = append(calls, "membership") },
	}))
	for _, selected := range []ChannelSelectedMsg{{ID: "c1", Name: "One"}, {ID: "c2", Name: "Two"}, {ID: "c1", Name: "One"}} {
		_, _ = app.Update(selected)
		if app.activeChannelID != selected.ID || app.messagepane.IsLoading() || len(app.messagepane.Messages()) != 0 {
			t.Fatalf("selection %#v left active=%q loading=%v messages=%d", selected, app.activeChannelID, app.messagepane.IsLoading(), len(app.messagepane.Messages()))
		}
	}
	if !reflect.DeepEqual(calls, []string(nil)) {
		t.Fatalf("legacy channel calls = %v", calls)
	}
}

func TestMattermostThreadHelpersNoOpAtOperationBoundary(t *testing.T) {
	a := NewApp()
	a.features = MattermostTask8Features()
	a.messagepane.SetMessages([]messages.MessageItem{{TS: "1"}})
	a.threadsView.SetSummaries([]cache.ThreadSummary{{ChannelID: "c1", ThreadTS: "1"}})
	for name, cmd := range map[string]tea.Cmd{
		"selected message": a.openThreadForSelectedMessage(),
		"threads view":     a.openSelectedThreadCmd(false),
		"message nav":      a.openThreadForMessageNav("c1", "1"),
	} {
		if cmd != nil {
			t.Errorf("%s helper returned command", name)
		}
	}
	if a.threadVisible {
		t.Fatal("disabled helper opened thread")
	}
}

func TestMattermostThreadsPanelEnabledWhileSidebarThreadsRemainDisabled(t *testing.T) {
	a := NewApp()
	_, _ = a.Update(ServerReadyMsg{Server: ServerViewState{
		ServerID:      "server-1",
		InitialActive: true,
	}})

	if !a.features.Allows(FeatureThreadPanel) {
		t.Fatal("Mattermost channel-level thread panels should be enabled")
	}
	if a.features.Allows(FeatureThreads) {
		t.Fatal("Mattermost workspace-wide Threads workflow should remain disabled")
	}
	a.sidebar.SelectThreadsRow()
	if a.sidebar.IsThreadsSelected() {
		t.Fatal("Mattermost workspace-wide Threads row should remain disabled")
	}
}

func TestMattermostThreadPanelReplyAllowedWhileGlobalThreadsDestinationRejected(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	a.features = MattermostTask14Features()
	a.activeChannelID = a.activeHistoryRequest.ChannelID
	a.messagepane.SetMessages([]messages.MessageItem{{ID: "root-post-1", Text: "root"}})
	a.SetThreadService(NewThreadService(ThreadServiceFuncs{
		Fetch: func(_ ids.ChannelID, threadTS ids.ThreadTS) tea.Msg {
			return ThreadRepliesLoadedMsg{ThreadTS: string(threadTS)}
		},
	}))

	if cmd := a.openThreadForSelectedMessage(); cmd == nil || !a.threadVisible {
		t.Fatal("Mattermost channel thread panel should open")
	}
	if _, cmd := a.Update(mattermostThreadReplyMsg(a, "reply")); cmd == nil {
		t.Fatal("Mattermost channel thread reply should cross the panel capability gate")
	}

	if _, cmd := a.Update(ThreadsViewActivatedMsg{}); cmd != nil || a.view == ViewThreads {
		t.Fatalf("global Threads activation crossed disabled gate: cmd=%v view=%v", cmd != nil, a.view)
	}
	a.channelFinder.Open()
	for _, r := range "Threads" {
		a.channelFinder.HandleKey(string(r))
	}
	cmd := handleChannelFinderMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("seeded synthetic Threads destination should remain inaccessible")
	}
}

func TestSlackFeaturesEnableThreadPanelAndGlobalThreads(t *testing.T) {
	features := SlackFeatures()
	if !features.Allows(FeatureThreadPanel) || !features.Allows(FeatureThreads) {
		t.Fatalf("Slack features: panel=%v global=%v", features.Allows(FeatureThreadPanel), features.Allows(FeatureThreads))
	}
}

func helpContains(entries []help.Entry, description string) bool {
	for _, entry := range entries {
		if entry.Desc == description {
			return true
		}
	}
	return false
}
