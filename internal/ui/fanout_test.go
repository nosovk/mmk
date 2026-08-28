// internal/ui/fanout_test.go
//
// Channel-scoped event fan-out (window-management Phase 3, Task 2):
// events carrying a ChannelID must reach EVERY window viewing that
// channel — focused or not — and must not leak into windows viewing
// other channels.
package ui

import (
	"fmt"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

// twoWindowApp returns an app with window 1 on C1 and window 2
// (focused) on C2.
func twoWindowApp(t *testing.T) (*App, wintree.LeafID, wintree.LeafID) {
	t.Helper()
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "C2", Name: "ops", Type: "channel"})
	return a, w1, w2
}

// testMessageItems builds n items with distinct TS and greppable text
// ("msg-1", "msg-2", ...).
func testMessageItems(n int) []messages.MessageItem {
	out := make([]messages.MessageItem, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, messages.MessageItem{
			TS:        fmt.Sprintf("%d.0", i),
			UserID:    "U1",
			UserName:  "alice",
			Text:      fmt.Sprintf("msg-%d", i),
			Timestamp: "1:00 PM",
		})
	}
	return out
}

// inboundMsg builds the NewMessageMsg used across these tests: a
// top-level message from another user (not self) for channelID.
func inboundMsg(channelID string) NewMessageMsg {
	return NewMessageMsg{
		ChannelID: channelID,
		Message: messages.MessageItem{
			TS:        "9.0",
			UserID:    "U9",
			UserName:  "zoe",
			Text:      "ping",
			Timestamp: "1:00 PM",
		},
	}
}

func TestFanout_NewMessageReachesUnfocusedWindow(t *testing.T) {
	a, w1, _ := twoWindowApp(t)
	before := len(a.winModels[w1].Messages())
	_, _ = a.Update(inboundMsg("C1"))
	if got := len(a.winModels[w1].Messages()); got != before+1 {
		t.Fatalf("unfocused window on C1 should receive the message: %d -> %d", before, got)
	}
}

func TestFanout_NewMessageDoesNotReachOtherChannelWindow(t *testing.T) {
	a, _, w2 := twoWindowApp(t)
	before := len(a.winModels[w2].Messages())
	_, _ = a.Update(inboundMsg("C1"))
	if got := len(a.winModels[w2].Messages()); got != before {
		t.Fatalf("window on C2 must not receive a C1 message: %d -> %d", before, got)
	}
}

// sameChannelApp returns an app with two windows both viewing C1
// (window 2 focused — split clones the source channel).
func sameChannelApp(t *testing.T) (*App, wintree.LeafID, wintree.LeafID) {
	t.Helper()
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide) // clone: both on C1
	return a, w1, a.focusedWin
}

func TestFanout_SameChannelTwiceBothUpdate(t *testing.T) {
	a, w1, w2 := sameChannelApp(t)
	_, _ = a.Update(inboundMsg("C1"))
	n1, n2 := len(a.winModels[w1].Messages()), len(a.winModels[w2].Messages())
	if n1 != n2 || n1 == 0 {
		t.Fatalf("both C1 windows must update: w1=%d w2=%d", n1, n2)
	}
}

func TestFanout_MessagesLoadedSeedsUnfocusedWindow(t *testing.T) {
	a, w1, _ := twoWindowApp(t)
	_, _ = a.Update(MessagesLoadedMsg{ChannelID: "C1", Messages: testMessageItems(3)})
	if got := len(a.winModels[w1].Messages()); got != 3 {
		t.Fatalf("MessagesLoaded for C1 must apply to the unfocused C1 window, got %d", got)
	}
}

// TestFanout_SameChannelLoadDoesNotAliasSlices guards the deep-copy
// rule: when two windows view the same channel, a single
// MessagesLoadedMsg must give each model its OWN top-level slice (and
// Reactions slices) — UpdateReaction mutates elements in place, so a
// shared backing array would let one window's reaction event corrupt
// the other's view.
func TestFanout_SameChannelLoadDoesNotAliasSlices(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide) // clone: both on C1
	w2 := a.focusedWin
	items := testMessageItems(2)
	items[0].Reactions = []messages.ReactionItem{{Emoji: "tada", Count: 1, UserIDs: []string{"U1"}}}
	_, _ = a.Update(MessagesLoadedMsg{ChannelID: "C1", Messages: items})

	// Mutate window 2's copy in place; window 1 must be unaffected.
	a.winModels[w2].UpdateReaction("1.0", "tada", "U2", false)
	got := a.winModels[w1].Messages()[0].Reactions
	if len(got) != 1 || got[0].Count != 1 {
		t.Fatalf("window 1's reactions corrupted by window 2's in-place update: %+v", got)
	}
}

// TestFanout_MarkReadOnlyOnFocusedSelection pins the spec read-state
// rule: realtime traffic to an UNFOCUSED window must not trigger a
// mark-read — the read marker only advances on focused entry.
//
// Seam choice: the only UI-side mark-read producer is
// ChannelService.MarkRead, dispatched solely from the tier-1 branch
// of reduceChannelSelected (reducer_channels.go). reduceNewMessage
// never calls it, and nothing else in the NewMessageMsg path can.
// The strongest assertions available are therefore both of: (1) a
// MarkRead spy on the channel service records zero calls when the
// NewMessageMsg cmd batch is drained — guards against a future
// fan-out accidentally wiring mark-read into the path; and (2) the
// unfocused C1 model's LastReadTS is unchanged — guards the
// per-model write loop against advancing the local watermark.
func TestFanout_MarkReadOnlyOnFocusedSelection(t *testing.T) {
	a, w1, _ := twoWindowApp(t)
	a.winModels[w1].SetLastReadTS("5.0")
	markReadCalls := 0
	a.setChannelReadMarkerForTest(func(channelID ids.ChannelID, ts ids.MessageTS) tea.Msg {
		markReadCalls++
		return nil
	})
	_, cmd := a.Update(inboundMsg("C1"))
	_ = drainBatch(cmd) // execute every scheduled cmd
	if markReadCalls != 0 {
		t.Fatalf("NewMessage to an unfocused window must not mark read; MarkRead called %d times", markReadCalls)
	}
	if got := a.winModels[w1].LastReadTS(); got != "5.0" {
		t.Fatalf("unfocused window's lastReadTS must be unchanged: got %q, want %q", got, "5.0")
	}
}

// TestFanout_OlderMessagesPrependIntoSiblingWindow: an older-history
// backfill for C1 prepends into BOTH windows viewing C1.
func TestFanout_OlderMessagesPrependIntoSiblingWindow(t *testing.T) {
	a, w1, w2 := sameChannelApp(t)
	// Baseline newer than the backfill items so the model's overlap
	// guard (PrependMessages) keeps them.
	_, _ = a.Update(MessagesLoadedMsg{ChannelID: "C1", Messages: []messages.MessageItem{
		{TS: "5.0", UserID: "U1", UserName: "alice", Text: "baseline", Timestamp: "1:00 PM"},
	}})
	// AnchorTS must match the windows' oldest TS (post-merge contract:
	// blocks anchored to a replaced buffer are dropped per window).
	_, _ = a.Update(OlderMessagesLoadedMsg{ChannelID: "C1", AnchorTS: "5.0", Messages: testMessageItems(2)})
	for _, w := range []wintree.LeafID{w1, w2} {
		msgs := a.winModels[w].Messages()
		if len(msgs) != 3 {
			t.Fatalf("window %v: want 3 messages after backfill, got %d", w, len(msgs))
		}
		if msgs[0].TS != "1.0" {
			t.Fatalf("window %v: backfill items must be at the head, got %q", w, msgs[0].TS)
		}
	}
}

// TestFetchingOlder_PerChannelIsolation: an in-flight C1 backfill must
// not block a C2 backfill (per-channel flags), and a C1 completion
// clears C1's flag even when no window views C1 anymore.
func TestFetchingOlder_PerChannelIsolation(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C2", Name: "ops", Type: "channel"})
	a.messagepane.SetMessages(testMessageItems(2))
	called := false
	a.setOlderMessagesFetcherForTest(func(channelID ids.ChannelID, oldestTS ids.MessageTS) tea.Msg {
		if channelID != "C2" {
			t.Errorf("fetcher called for %q, want C2", channelID)
		}
		called = true
		return nil
	})
	a.fetchingOlder["C1"] = true // in-flight backfill on another channel
	cmd := a.maybeFetchOlderHistory(true)
	if cmd == nil {
		t.Fatal("C1's in-flight backfill must not block C2's")
	}
	_ = drainBatch(cmd)
	if !called {
		t.Fatal("expected the C2 fetcher to be dispatched")
	}
	if !a.fetchingOlder["C2"] {
		t.Fatal("expected fetchingOlder[C2]=true after dispatch")
	}
	// Completion for C1 clears its own flag even though no window
	// views C1 (the old global bool stayed stuck here).
	_, _ = a.Update(OlderMessagesLoadedMsg{ChannelID: "C1"})
	if a.fetchingOlder["C1"] {
		t.Fatal("OlderMessagesLoaded for C1 must clear C1's flag even with no viewing window")
	}
}
