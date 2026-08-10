package ui

import (
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
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
		SendMessageMsg{ChannelID: "c1", Text: "no"},
		SendThreadReplyMsg{ChannelID: "c1", ThreadTS: "1", Text: "no"},
		EditMessageMsg{ChannelID: "c1", TS: "1", NewText: "no"},
		DeleteMessageMsg{ChannelID: "c1", TS: "1"},
		MarkUnreadMsg{ChannelID: "c1", BoundaryTS: "1"},
		ThreadsViewActivatedMsg{}, EnterNewMessageMsg{},
	} {
		if _, cmd := app.Update(msg); cmd != nil {
			t.Fatalf("%T returned command", msg)
		}
	}
	if app.mode != beforeMode || app.view == ViewThreads || app.threadVisible {
		t.Fatalf("disabled operation mutated UI: mode=%v view=%v thread=%v", app.mode, app.view, app.threadVisible)
	}
	for _, hidden := range []string{"toggle thread", "save thread"} {
		if helpContains(app.helpEntries(), hidden) {
			t.Fatalf("Mattermost help exposes %q", hidden)
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

func helpContains(entries []help.Entry, description string) bool {
	for _, entry := range entries {
		if entry.Desc == description {
			return true
		}
	}
	return false
}
