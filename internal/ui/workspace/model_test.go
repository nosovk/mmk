package workspace

import (
	"errors"
	"strings"
	"testing"
)

func TestServerRailPreservesOrderDuplicateNamesAndStateByID(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "server-b", Name: "Chat", Initials: "CH", State: ItemStateLoading},
		{ID: "server-a", Name: "Chat", Initials: "CH", State: ItemStateLoading},
	}, 0)

	m.SetState("server-a", ItemStateReady, nil)
	m.SetState("server-b", ItemStateError, errors.New("offline"))

	items := m.Items()
	if items[0].ID != "server-b" || items[1].ID != "server-a" {
		t.Fatalf("rail order = [%s %s]", items[0].ID, items[1].ID)
	}
	if items[0].State != ItemStateError || items[0].Error == "" {
		t.Fatalf("server-b state = %#v, want error with message", items[0])
	}
	if items[1].State != ItemStateReady || items[1].Error != "" {
		t.Fatalf("server-a state = %#v, want ready without error", items[1])
	}
}

func TestServerRailRepresentsRealtimeConnectionStates(t *testing.T) {
	m := New([]WorkspaceItem{{ID: "s1", Name: "One", Initials: "ON", State: ItemStateLoading}}, 0)
	for _, state := range []ItemState{ItemStateConnecting, ItemStateReady, ItemStateOffline, ItemStateReconnecting} {
		m.SetState("s1", state, nil)
		items := m.Items()
		if items[0].State != state || items[0].Error != "" {
			t.Fatalf("state=%v item=%#v", state, items[0])
		}
		if view := m.View(10); !strings.Contains(view, connectionMarker(state)) {
			t.Fatalf("state=%v view=%q missing marker %q", state, view, connectionMarker(state))
		}
		if got, ok := m.ClickAt(1); !ok || got.ID != "s1" {
			t.Fatalf("state=%v made server unswitchable: item=%#v ok=%v", state, got, ok)
		}
	}
}

func TestWorkspaceRailView(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "T1", Name: "Acme Corp", Initials: "AC", HasUnread: false},
		{ID: "T2", Name: "Beta Inc", Initials: "BI", HasUnread: true},
	}, 0)

	view := m.View(20) // 20 rows height
	if !strings.Contains(view, "AC") {
		t.Error("expected 'AC' in view")
	}
	if !strings.Contains(view, "BI") {
		t.Error("expected 'BI' in view")
	}
}

func TestWorkspaceRailSelect(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "T1", Name: "Acme", Initials: "AC"},
		{ID: "T2", Name: "Beta", Initials: "BE"},
	}, 0)

	if m.SelectedID() != "T1" {
		t.Error("expected T1 selected initially")
	}

	m.Select(1)
	if m.SelectedID() != "T2" {
		t.Error("expected T2 selected after Select(1)")
	}
}

// TestClickAt asserts ClickAt's mapping from rail-local y to workspace
// item using the rail's View() layout: row 0 is the top padding, row
// 1 is item 0, row 2 is the gap between items, row 3 is item 1, and
// so on (Padding(1,0) above and "\n\n"-joined item rows).
func TestClickAt(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "T1", Name: "Acme", Initials: "AC"},
		{ID: "T2", Name: "Beta", Initials: "BE"},
		{ID: "T3", Name: "Gamma", Initials: "GA"},
	}, 0)

	cases := []struct {
		name   string
		y      int
		wantID string
		wantOK bool
	}{
		{"top padding", 0, "", false},
		{"first item", 1, "T1", true},
		{"gap between items 0 and 1", 2, "", false},
		{"second item", 3, "T2", true},
		{"gap between items 1 and 2", 4, "", false},
		{"third item", 5, "T3", true},
		{"below last item", 6, "", false},
		{"well below content", 99, "", false},
		{"negative y", -1, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := m.ClickAt(tc.y)
			if ok != tc.wantOK {
				t.Fatalf("ClickAt(%d) ok=%v want %v", tc.y, ok, tc.wantOK)
			}
			if got.ID != tc.wantID {
				t.Errorf("ClickAt(%d) ID=%q want %q", tc.y, got.ID, tc.wantID)
			}
		})
	}
}

func TestClickAt_EmptyRail(t *testing.T) {
	m := New(nil, 0)
	if _, ok := m.ClickAt(1); ok {
		t.Error("ClickAt on empty rail must return ok=false")
	}
}

func TestNameByID(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "T1", Name: "SWAP", Initials: "SW"},
		{ID: "T2", Name: "Home", Initials: "HO"},
	}, 0)
	cases := map[string]string{
		"T1":        "SWAP",
		"T2":        "Home",
		"T-missing": "",
		"":          "",
	}
	for id, want := range cases {
		if got := m.NameByID(id); got != want {
			t.Errorf("NameByID(%q) = %q want %q", id, got, want)
		}
	}
}

func TestOtherUnreadCount_NoReader(t *testing.T) {
	m := New([]WorkspaceItem{{ID: "T1"}}, 0)
	if got := m.OtherUnreadCount("T1"); got != 0 {
		t.Errorf("OtherUnreadCount with no reader = %d want 0", got)
	}
}

func TestOtherUnreadCount(t *testing.T) {
	m := New([]WorkspaceItem{
		{ID: "T1"}, {ID: "T2"}, {ID: "T3"},
	}, 0)
	m.SetUnreadReader(func() []string { return []string{"T1", "T2", "T3"} })

	cases := []struct {
		activeID string
		want     int
	}{
		{"T1", 2},
		{"T2", 2},
		{"T3", 2},
		{"T-missing", 3}, // active workspace not in unread set: counts all
		{"", 3},          // empty active: counts all; caller is responsible
	}
	for _, tc := range cases {
		if got := m.OtherUnreadCount(tc.activeID); got != tc.want {
			t.Errorf("OtherUnreadCount(%q) = %d want %d", tc.activeID, got, tc.want)
		}
	}
}

func TestOtherUnreadCount_EmptyReaderResult(t *testing.T) {
	m := New([]WorkspaceItem{{ID: "T1"}, {ID: "T2"}}, 0)
	m.SetUnreadReader(func() []string { return nil })
	if got := m.OtherUnreadCount("T1"); got != 0 {
		t.Errorf("OtherUnreadCount with empty reader = %d want 0", got)
	}
}
