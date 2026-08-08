package main

import (
	"testing"

	"github.com/nosovk/mmk/internal/ui/sidebar"
)

func TestWorkspacePresenceIDs(t *testing.T) {
	wctx := &WorkspaceContext{
		UserID: "USELF",
		Channels: []sidebar.ChannelItem{
			{Type: "dm", DMUserID: "U1"},
			{Type: "channel"}, // not a DM — skipped
			{Type: "dm", DMUserID: "U2"},
			{Type: "dm", DMUserID: "U1"},     // duplicate peer — deduped
			{Type: "app", DMUserID: "UBOT"},  // app/bot DM — no presence dot
			{Type: "group_dm", DMUserID: ""}, // group DM — skipped
			{Type: "dm", DMUserID: ""},       // malformed — skipped
		},
	}

	got := workspacePresenceIDs(wctx)
	want := []string{"USELF", "U1", "U2"} // self first, then DM peers in order, deduped
	if len(got) != len(want) {
		t.Fatalf("workspacePresenceIDs() = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("workspacePresenceIDs()[%d] = %q, want %q (full %v)", i, got[i], want[i], got)
		}
	}
}
