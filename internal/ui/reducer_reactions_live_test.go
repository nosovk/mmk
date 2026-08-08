package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
)

// TestLiveOwnReactionStyledViaWorkspaceReady drives the PRODUCTION path:
// WorkspaceReadyMsg sets the current user (and must wire the panes), so a
// live own-device reaction renders as ours (HasReacted=true). Regression
// guard for the review on PR #68 — the prior code assigned a.currentUserID
// directly in the reducer, leaving the pane's currentUserID empty in real
// sessions even though SetCurrentUserID-based tests passed.
func TestLiveOwnReactionStyledViaWorkspaceReady(t *testing.T) {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 60})

	a.Update(WorkspaceReadyMsg{
		TeamID:        "T1",
		InitialActive: true,
		UserID:        "ME",
		Channels:      []sidebar.ChannelItem{{ID: "C1", Name: "general", Type: "channel"}},
	})
	// Phase 3 routes reaction events to the windows viewing the
	// channel; apply the queued selection so the pane views C1.
	// (WorkspaceReadyMsg queues it as a cmd the test never runs.)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	a.messagepane.SetMessages([]messages.MessageItem{{TS: "100.0", Text: "hi"}})

	a.Update(ReactionAddedMsg{ChannelID: "C1", MessageTS: "100.0", UserID: "ME", Emoji: "tada"})
	msg, _ := a.messagepane.SelectedMessage()
	if len(msg.Reactions) != 1 {
		t.Fatalf("expected 1 reaction, got %+v", msg.Reactions)
	}
	if !msg.Reactions[0].HasReacted {
		t.Error("live own-device reaction must render as ours (HasReacted=true) via the WorkspaceReadyMsg path")
	}
}

// TestReactionAddedAppliesOwnUserLive is the headline of the live-reactions
// fix: a ReactionAddedMsg by OUR OWN user that has no local optimistic
// update (e.g. made from web/mobile) must be applied by the reducer. The
// previous code dropped any echo whose userID == currentUserID.
func TestReactionAddedAppliesOwnUserLive(t *testing.T) {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	a.SetCurrentUserID("ME")
	// Phase 3: the pane must view C1 for C1 reaction events to route.
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	a.messagepane.SetMessages([]messages.MessageItem{{TS: "100.0", Text: "hi"}})

	// No optimistic update here — this is purely the WS echo of a reaction
	// we made elsewhere. It must show live.
	a.Update(ReactionAddedMsg{ChannelID: "C1", MessageTS: "100.0", UserID: "ME", Emoji: "tada"})
	msg, _ := a.messagepane.SelectedMessage()
	if len(msg.Reactions) != 1 || msg.Reactions[0].Count != 1 {
		t.Fatalf("own-device reaction must show live, got %+v", msg.Reactions)
	}

	// Optimistic add + its own WS echo must collapse to one count —
	// idempotency is what makes dropping the self-filter safe.
	a.updateReactionOnMessage("C1", "100.0", "rocket", "ME", false) // optimistic
	a.Update(ReactionAddedMsg{ChannelID: "C1", MessageTS: "100.0", UserID: "ME", Emoji: "rocket"})
	msg, _ = a.messagepane.SelectedMessage()
	var rocketCount int
	found := false
	for _, r := range msg.Reactions {
		if r.Emoji == "rocket" {
			rocketCount = r.Count
			found = true
		}
	}
	if !found || rocketCount != 1 {
		t.Errorf("optimistic + echo must collapse to count 1, got %+v", msg.Reactions)
	}
}
