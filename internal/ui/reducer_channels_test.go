package ui

import (
	"testing"

	"github.com/nosovk/mmk/internal/ui/messages"
)

// Anchor validation: an in-flight FetchOlder keyed to the OLD buffer's
// OldestTS must be dropped when it lands after FetchAround replaced the
// buffer (otherwise the prepend splices an unrelated older block onto
// the new window, producing an out-of-order/duplicated buffer).
func TestOlderMessagesLoaded_StaleAnchorDropped(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	// Seed the original buffer; a FetchOlder is in flight keyed to its
	// oldest ts.
	app.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1700000010.000000", Text: "old-buffer oldest"},
		{TS: "1700000011.000000", Text: "old-buffer newer"},
	})
	app.fetchingOlder["C1"] = true
	app.messagepane.SetLoading(true)

	// Jump-to-message replaces the buffer mid-flight.
	app.Update(MessagesAroundLoadedMsg{
		ChannelID: "C1",
		TargetTS:  "1700000050.000000",
		Messages: []messages.MessageItem{
			{TS: "1700000049.000000", Text: "window a"},
			{TS: "1700000050.000000", Text: "window b"},
		},
	})

	// The stale FetchOlder result lands, anchored to the OLD oldest.
	app.Update(OlderMessagesLoadedMsg{
		ChannelID: "C1",
		AnchorTS:  "1700000010.000000",
		Messages: []messages.MessageItem{
			{TS: "1700000001.000000", Text: "stale older"},
		},
	})

	if got := app.messagepane.OldestTS(); got != "1700000049.000000" {
		t.Errorf("stale-anchor prepend was applied: OldestTS = %q, want %q",
			got, "1700000049.000000")
	}
	if app.fetchingOlder["C1"] {
		t.Error("fetchingOlder not reset after stale-anchor drop")
	}
	if app.messagepane.IsLoading() {
		t.Error("messagepane loading not cleared after stale-anchor drop")
	}
}

// Matching anchor: the normal backfill path must still prepend.
func TestOlderMessagesLoaded_MatchingAnchorPrepends(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	app.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1700000010.000000", Text: "oldest"},
	})
	app.fetchingOlder["C1"] = true

	app.Update(OlderMessagesLoadedMsg{
		ChannelID: "C1",
		AnchorTS:  "1700000010.000000",
		Messages: []messages.MessageItem{
			{TS: "1700000001.000000", Text: "older"},
		},
	})

	if got := app.messagepane.OldestTS(); got != "1700000001.000000" {
		t.Errorf("prepend not applied: OldestTS = %q, want %q",
			got, "1700000001.000000")
	}
	if app.fetchingOlder["C1"] {
		t.Error("fetchingOlder not reset after successful prepend")
	}
}

// Stale-channel drop must still reset fetchingOlder, otherwise
// scroll-backfill is permanently disabled after navigating away
// mid-fetch.
func TestOlderMessagesLoaded_StaleChannelResetsFetchingOlder(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C2"
	app.fetchingOlder["C1"] = true

	app.Update(OlderMessagesLoadedMsg{
		ChannelID: "C1",
		AnchorTS:  "1700000010.000000",
		Messages: []messages.MessageItem{
			{TS: "1700000001.000000", Text: "for old channel"},
		},
	})

	if app.fetchingOlder["C1"] {
		t.Error("fetchingOlder not reset on stale-channel drop")
	}
}

// SelectByTS miss: when the fetched window doesn't contain TargetTS,
// surface a toast instead of silently leaving the selection wherever
// it was.
func TestMessagesAroundLoaded_TargetMissingToasts(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	_, cmd := app.Update(MessagesAroundLoadedMsg{
		ChannelID: "C1",
		TargetTS:  "1700000099.000000", // not in Messages
		Messages: []messages.MessageItem{
			{TS: "1700000001.000000", Text: "a"},
			{TS: "1700000002.000000", Text: "b"},
		},
	})
	found := false
	for _, m := range drainCmd(cmd) {
		if _, ok := m.(ToastMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("expected ToastMsg when TargetTS is missing from the loaded window")
	}
}
