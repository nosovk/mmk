package ui

import (
	"reflect"
	"testing"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

func TestInactiveServerRevisionRejectsStaleRailRegression(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", Name: "One"}, {ID: "s2", Name: "Two"}})
	_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 1, false, "c1")})
	a.activeChannelID = "c1"
	a.messagepane.SetMessages([]messages.MessageItem{{ID: "visible"}})

	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s2", 5, true, "other")})
	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s2", 4, false, "stale")})

	items := a.workspaceRail.Items()
	if !items[1].HasUnread {
		t.Fatalf("stale refresh regressed inactive rail: %#v", items)
	}
	if a.activeServerID != "s1" || a.activeChannelID != "c1" || !reflect.DeepEqual(historyItemIDs(a.messagepane.Messages()), []string{"visible"}) {
		t.Fatalf("inactive refresh changed visible state: server=%q channel=%q history=%v", a.activeServerID, a.activeChannelID, historyItemIDs(a.messagepane.Messages()))
	}
}

func TestServerRevisionCrossServerIndependence(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1"}, {ID: "s2"}})
	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s1", 100, true, "one")})
	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s2", 1, true, "two")})

	items := a.workspaceRail.Items()
	if !items[0].HasUnread || !items[1].HasUnread {
		t.Fatalf("cross-server revisions interfered: %#v", items)
	}
}

func TestServerRevisionAcceptedInactiveRefreshNotifiesCommonUnreadState(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", Name: "One"}, {ID: "s2", Name: "Two"}})
	_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 1, false, "one")})
	var active, other int
	a.SetStatusReporter(func(activeUnread, otherUnread int, _, _ string) {
		active, other = activeUnread, otherUnread
	})

	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s2", 1, true, "two")})

	if active != 0 || other != 1 {
		t.Fatalf("status unread active=%d other=%d want 0/1", active, other)
	}
}

func TestServerRevisionAcceptedInactiveReadyUpdatesUnreadWithoutChangingVisibleState(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", Name: "One"}, {ID: "s2", Name: "Two"}})
	_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 1, false, "one")})
	a.activeChannelID = "one"
	a.messagepane.SetMessages([]messages.MessageItem{{ID: "visible"}})
	var active, other, notifications int
	a.SetStatusReporter(func(activeUnread, otherUnread int, _, _ string) {
		active, other = activeUnread, otherUnread
		notifications++
	})
	ready := revisionState("s2", 1, true, "two")
	ready.InitialActive = false

	_, cmd := a.Update(ServerReadyMsg{Server: ready})

	if cmd != nil {
		t.Fatal("inactive ready queued activation")
	}
	items := a.workspaceRail.Items()
	if len(items) != 2 || !items[1].HasUnread {
		t.Fatalf("inactive ready did not update rail: %#v", items)
	}
	if active != 0 || other != 1 || notifications != 1 {
		t.Fatalf("status active=%d other=%d notifications=%d want 0/1/1", active, other, notifications)
	}
	if a.activeServerID != "s1" || a.activeChannelID != "one" || !reflect.DeepEqual(historyItemIDs(a.messagepane.Messages()), []string{"visible"}) {
		t.Fatalf("inactive ready changed visible state: server=%q channel=%q history=%v", a.activeServerID, a.activeChannelID, historyItemIDs(a.messagepane.Messages()))
	}
	if got := a.sidebar.Items(); len(got) != 1 || got[0].ID != "one" {
		t.Fatalf("inactive ready changed sidebar: %#v", got)
	}
}

func TestServerRevisionEqualRefreshIgnoredWithoutNotification(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1", Name: "One"}})
	_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 3, true, "new")})
	notifications := 0
	a.SetStatusReporter(func(int, int, string, string) { notifications++ })

	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s1", 3, false, "old")})

	if notifications != 0 {
		t.Fatalf("equal refresh notified %d times", notifications)
	}
	if got := a.sidebar.Items(); len(got) != 1 || got[0].ID != "new" || a.sidebar.UnreadChannelCount() != 1 {
		t.Fatalf("equal refresh replaced canonical state: channels=%#v unread=%d", got, a.sidebar.UnreadChannelCount())
	}
}

func TestServerRevisionStaleAndEqualSwitchActivateCanonical(t *testing.T) {
	for _, revision := range []uint64{4, 5} {
		t.Run(string(rune('0'+revision)), func(t *testing.T) {
			a := NewApp()
			a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1"}, {ID: "s2"}})
			_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 1, false, "home")})
			_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s2", 5, true, "canonical")})

			_, cmd := a.Update(ServerSwitchedMsg{Server: revisionState("s2", revision, false, "stale")})

			got := a.sidebar.Items()
			if a.activeServerID != "s2" || len(got) != 1 || got[0].ID != "canonical" {
				t.Fatalf("switch revision %d did not activate canonical state: server=%q channels=%#v", revision, a.activeServerID, got)
			}
			if selected, ok := findHistoryChannelSelected(cmd()); !ok || selected.ID != "canonical" {
				t.Fatalf("switch revision %d selected %#v", revision, cmd())
			}
		})
	}
}

func TestServerRevisionZeroDoesNotOverwriteTrackedCanonical(t *testing.T) {
	a := NewApp()
	a.SetWorkspaces([]workspace.WorkspaceItem{{ID: "s1"}})
	_, _ = a.Update(ServerReadyMsg{Server: revisionState("s1", 2, true, "canonical")})
	_, _ = a.Update(ServerRefreshedMsg{Server: revisionState("s1", 0, false, "legacy")})

	if got := a.sidebar.Items(); len(got) != 1 || got[0].ID != "canonical" || a.sidebar.UnreadChannelCount() != 1 {
		t.Fatalf("legacy state overwrote canonical: channels=%#v unread=%d", got, a.sidebar.UnreadChannelCount())
	}
}

func revisionState(serverID string, revision uint64, unread bool, channelID string) ServerViewState {
	return ServerViewState{
		ServerID: ids.ServerID(serverID), Revision: revision, InitialActive: serverID == "s1",
		Channels:  []sidebar.ChannelItem{{ID: channelID, Name: channelID, Type: "channel"}},
		ReadState: map[string]cache.ReadState{channelID: {HasUnread: unread}}, HasUnread: unread,
	}
}
