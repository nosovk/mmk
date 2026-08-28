package ui

import (
	"errors"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
)

func linkTestApp(t *testing.T) (*App, *string) {
	t.Helper()
	app := NewApp()
	app.activeServerID = "T1"
	var opened string
	app.browserOpener = func(url string) tea.Cmd {
		opened = url
		return nil
	}
	return app, &opened
}

func drainCmd(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	var out []tea.Msg
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, c := range batch {
			out = append(out, drainCmd(c)...)
		}
		return out
	}
	if msg != nil {
		out = append(out, msg)
	}
	return out
}

func TestOpenLink_NonSlackURL_OpensBrowser(t *testing.T) {
	app, opened := linkTestApp(t)
	_, cmd := app.Update(OpenLinkMsg{URL: "https://github.com/foo/bar"})
	drainCmd(cmd)
	if *opened != "https://github.com/foo/bar" {
		t.Errorf("browser opened %q", *opened)
	}
}

func TestOpenLink_ActiveSlackPermalinkOpensBrowserWithoutChangingView(t *testing.T) {
	app, opened := linkTestApp(t)
	app.activeChannelID = "C054JFCBN69"
	app.messagepane.SetMessages([]messages.MessageItem{
		{TS: "1779284733.270139", Text: "target"},
		{TS: "1779284734.000000", Text: "newer"},
	})
	url := "https://myteam.slack.com/archives/C054JFCBN69/p1779284733270139"
	_, cmd := app.Update(OpenLinkMsg{URL: url})
	drainCmd(cmd)
	sel, ok := app.messagepane.SelectedMessage()
	if !ok || sel.TS != "1779284734.000000" {
		t.Errorf("selected = %+v ok=%v", sel, ok)
	}
	if *opened != url {
		t.Errorf("browser opened %q, want %q", *opened, url)
	}
	if app.threadVisible {
		t.Fatal("external link changed thread visibility")
	}
}

func TestMessagesLoaded_CompletesPendingMessageNav(t *testing.T) {
	app, _ := linkTestApp(t)
	app.activeChannelID = "C054JFCBN69"
	app.pendingMessageNav = &pendingMessageNav{
		channelID: "C054JFCBN69",
		messageTS: "1779284733.270139",
	}
	_, cmd := app.Update(MessagesLoadedMsg{
		ChannelID: "C054JFCBN69",
		Messages: []messages.MessageItem{
			{TS: "1779284733.270139", Text: "target"},
			{TS: "1779284734.000000", Text: "newer"},
		},
	})
	drainCmd(cmd)
	sel, ok := app.messagepane.SelectedMessage()
	if !ok || sel.TS != "1779284733.270139" {
		t.Errorf("selected = %+v ok=%v", sel, ok)
	}
	if app.pendingMessageNav != nil {
		t.Errorf("pendingMessageNav not cleared: %+v", app.pendingMessageNav)
	}
}

func TestChannelSelected_DifferentChannel_DropsPendingNav(t *testing.T) {
	app, _ := linkTestApp(t)
	app.pendingMessageNav = &pendingMessageNav{channelID: "C054JFCBN69", messageTS: "1.0"}
	_, cmd := app.Update(ChannelSelectedMsg{ID: "COTHER", Name: "other", Type: "channel"})
	drainCmd(cmd)
	if app.pendingMessageNav != nil {
		t.Errorf("pendingMessageNav should be dropped on unrelated navigation: %+v", app.pendingMessageNav)
	}
}

func TestMessagesAroundLoaded_ReplacesBufferAndSelects(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	app.Update(MessagesAroundLoadedMsg{
		ChannelID: "C1",
		TargetTS:  "1700000004.000000",
		Messages: []messages.MessageItem{
			{TS: "1700000003.000000", Text: "a"},
			{TS: "1700000004.000000", Text: "b"},
			{TS: "1700000005.000000", Text: "c"},
		},
	})
	sel, ok := app.messagepane.SelectedMessage()
	if !ok || sel.TS != "1700000004.000000" {
		t.Fatalf("selected %v ok=%v, want target ts", sel.TS, ok)
	}
}

// A failed jump must be non-destructive: if the fetched window doesn't
// contain the target, the current buffer (and position) stays intact —
// per the spec's error table — and the user just gets a toast.
func TestMessagesAroundLoaded_TargetMissingKeepsBuffer(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	app.messagepane.SetMessages([]messages.MessageItem{{TS: "1.0", Text: "keep"}})
	_, cmd := app.Update(MessagesAroundLoadedMsg{
		ChannelID: "C1",
		TargetTS:  "9.0",
		Messages:  []messages.MessageItem{{TS: "2.0", Text: "window"}},
	})
	sel, ok := app.messagepane.SelectedMessage()
	if !ok || sel.Text != "keep" {
		t.Fatalf("buffer replaced on failed jump: sel=%+v ok=%v", sel, ok)
	}
	var toast string
	for _, m := range drainCmd(cmd) {
		if tm, ok := m.(ToastMsg); ok {
			toast = tm.Text
		}
	}
	if toast != "Message not found in loaded history" {
		t.Fatalf("toast = %q", toast)
	}
}

func TestMessagesAroundLoaded_ErrorToasts(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C1"
	_, cmd := app.Update(MessagesAroundLoadedMsg{ChannelID: "C1", TargetTS: "1", Err: errors.New("boom")})
	msgs := drainCmd(cmd)
	found := false
	for _, m := range msgs {
		if _, ok := m.(ToastMsg); ok {
			found = true
		}
	}
	if !found {
		t.Fatal("expected ToastMsg on fetch failure")
	}
}

func TestMessagesAroundLoaded_StaleChannelDropped(t *testing.T) {
	app := NewApp()
	app.activeChannelID = "C2"
	app.messagepane.SetMessages([]messages.MessageItem{{TS: "1.0", Text: "keep"}})
	app.Update(MessagesAroundLoadedMsg{
		ChannelID: "C1",
		TargetTS:  "2.0",
		Messages:  []messages.MessageItem{{TS: "2.0", Text: "stale"}},
	})
	sel, _ := app.messagepane.SelectedMessage()
	if sel.Text != "keep" {
		t.Fatal("stale MessagesAroundLoadedMsg replaced active channel buffer")
	}
}

func TestCompletePendingMessageNav_OffBufferTriggersFetchAround(t *testing.T) {
	app, _ := linkTestApp(t)
	var fetchedChannel, fetchedTS string
	setChannelFetchAroundForTest(app, func(channelID ids.ChannelID, ts ids.MessageTS) tea.Msg {
		fetchedChannel, fetchedTS = string(channelID), string(ts)
		return nil
	})
	app.activeChannelID = "C054JFCBN69"
	app.pendingMessageNav = &pendingMessageNav{channelID: "C054JFCBN69", messageTS: "1700000001.000000"}

	_, cmd := app.Update(MessagesLoadedMsg{ChannelID: "C054JFCBN69", Messages: []messages.MessageItem{{TS: "1700000099.000000"}}})
	drainCmd(cmd)

	if fetchedChannel != "C054JFCBN69" || fetchedTS != "1700000001.000000" {
		t.Fatalf("FetchAround not dispatched: ch=%q ts=%q", fetchedChannel, fetchedTS)
	}
	if app.pendingMessageNav != nil {
		t.Fatal("pendingMessageNav not cleared")
	}
}
