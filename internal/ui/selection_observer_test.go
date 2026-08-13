package ui

import (
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/sidebar"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

func TestServerRefreshAppliedSignalsAfterSelectionObserver(t *testing.T) {
	app := NewApp()
	app.features = MattermostTask10Features()
	app.activeServerID = "s1"
	app.activeChannelID = "old"
	applied := NewUpdateApplied()
	observerRan := false
	app.SetSelectionObserver(func(ids.ServerID, string) {
		observerRan = true
	})
	observerRan = false
	_, _ = app.Update(ServerRefreshedMsg{
		Server:  ServerViewState{ServerID: "s1", Channels: []sidebar.ChannelItem{{ID: "new", Name: "New"}}},
		Applied: applied,
	})
	select {
	case <-applied.Done():
		if !observerRan {
			t.Fatal("refresh acknowledged before selection observer")
		}
	case <-time.After(time.Second):
		t.Fatal("refresh was not acknowledged")
	}
	_, _ = app.Update(ServerRefreshedMsg{Applied: applied})
}

func TestServerConnectionStateMsgUpdatesOnlyScopedRailItem(t *testing.T) {
	app := NewApp()
	app.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", State: workspace.ItemStateReady}, {ID: "s2", State: workspace.ItemStateReady}})
	_, _ = app.Update(ServerConnectionStateMsg{ServerID: "s2", State: workspace.ItemStateOffline})
	items := app.workspaceRail.Items()
	if items[0].State != workspace.ItemStateReady || items[1].State != workspace.ItemStateOffline {
		t.Fatalf("items=%#v", items)
	}
}

func TestSelectionObserverReceivesAppliedMattermostServerAndChannel(t *testing.T) {
	app := NewApp()
	var gotServer ids.ServerID
	var gotChannel string
	app.SetSelectionObserver(func(serverID ids.ServerID, channelID string) {
		gotServer, gotChannel = serverID, channelID
	})
	_, cmd := app.Update(ServerReadyMsg{Server: ServerViewState{
		ServerID:      "s1",
		InitialActive: true,
		Channels:      []sidebar.ChannelItem{{ID: "c1", Name: "One"}},
	}})
	if cmd == nil {
		t.Fatal("server activation did not select a channel")
	}
	_, _ = app.Update(cmd())
	if gotServer != "s1" || gotChannel != "c1" {
		t.Fatalf("observed selection = %q/%q", gotServer, gotChannel)
	}
}

func TestSelectionObserverTracksMattermostChannelChanges(t *testing.T) {
	app := NewApp()
	app.features = MattermostTask10Features()
	app.activeServerID = "s1"
	var gotServer ids.ServerID
	var gotChannel string
	app.SetSelectionObserver(func(serverID ids.ServerID, channelID string) {
		gotServer, gotChannel = serverID, channelID
	})
	_, _ = app.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	if gotServer != "s1" || gotChannel != "c2" {
		t.Fatalf("observed selection = %q/%q", gotServer, gotChannel)
	}
}

func TestSelectionObserverClearsChannelForEmptyMattermostServer(t *testing.T) {
	app := NewApp()
	app.features = MattermostTask10Features()
	app.activeServerID = "s1"
	app.activeChannelID = "c1"
	var gotServer ids.ServerID
	var gotChannel string
	app.SetSelectionObserver(func(serverID ids.ServerID, channelID string) {
		gotServer, gotChannel = serverID, channelID
	})
	_, _ = app.Update(ServerSwitchedMsg{Server: ServerViewState{ServerID: "s2"}})
	if gotServer != "s2" || gotChannel != "" {
		t.Fatalf("observed selection = %q/%q", gotServer, gotChannel)
	}
}
