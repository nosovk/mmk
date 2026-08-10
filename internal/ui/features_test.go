package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/help"
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

func helpContains(entries []help.Entry, description string) bool {
	for _, entry := range entries {
		if entry.Desc == description {
			return true
		}
	}
	return false
}
