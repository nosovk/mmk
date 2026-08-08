package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/messages"
)

// TestReactionSentMsgRollsBackOnFailure is the headline behavior: when the
// reactions.add/remove API call fails, the optimistic update is reverted
// so the UI doesn't show a reaction the server never accepted.
func TestReactionSentMsgRollsBackOnFailure(t *testing.T) {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	a.SetCurrentUserID("ME")
	// Make C1 the focused window's channel: reaction writes are
	// channel-scoped (fan-out to windows viewing the channel), so the
	// pane only receives them once it actually views C1.
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	a.messagepane.SetMessages([]messages.MessageItem{{TS: "100.0", Text: "hi"}})

	// Optimistic add, exactly as the toggle/picker paths do before the call.
	a.updateReactionOnMessage("C1", "100.0", "tada", "ME", false)
	if msg, _ := a.messagepane.SelectedMessage(); len(msg.Reactions) != 1 {
		t.Fatalf("setup: want 1 optimistic reaction, got %d", len(msg.Reactions))
	}

	// API call failed -> reducer rolls the optimistic add back.
	a.Update(ReactionSentMsg{
		ChannelID: "C1", MessageTS: "100.0", Emoji: "tada", UserID: "ME",
		Remove: false, Err: errors.New("invalid_name"),
	})
	if msg, _ := a.messagepane.SelectedMessage(); len(msg.Reactions) != 0 {
		t.Errorf("after failure: want reaction rolled back, got %d reactions", len(msg.Reactions))
	}
}

// TestReactionSentMsgKeepsOptimisticOnSuccess guards the other side: a
// successful call must NOT touch the already-applied optimistic update.
func TestReactionSentMsgKeepsOptimisticOnSuccess(t *testing.T) {
	a := NewApp()
	_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
	a.SetCurrentUserID("ME")
	// See TestReactionSentMsgRollsBackOnFailure: channel-scoped
	// reaction writes need the pane to actually view C1.
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	a.messagepane.SetMessages([]messages.MessageItem{{TS: "100.0", Text: "hi"}})
	a.updateReactionOnMessage("C1", "100.0", "tada", "ME", false)

	a.Update(ReactionSentMsg{
		ChannelID: "C1", MessageTS: "100.0", Emoji: "tada", UserID: "ME",
		Remove: false, Err: nil,
	})
	if msg, _ := a.messagepane.SelectedMessage(); len(msg.Reactions) != 1 {
		t.Errorf("after success: want reaction kept, got %d reactions", len(msg.Reactions))
	}
}
