package ui

import (
	"context"
	"errors"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
)

type fakeUIHistory struct {
	cached map[string][]messages.MessageItem
	recent []HistoryRequest
	older  []struct {
		Request HistoryRequest
		Before  string
	}
}

func (f *fakeUIHistory) ReadCached(request HistoryRequest, before string) ([]messages.MessageItem, error) {
	return append([]messages.MessageItem(nil), f.cached[string(request.ServerID)+":"+request.ChannelID+":"+before]...), nil
}
func (f *fakeUIHistory) FetchRecent(_ context.Context, request HistoryRequest) MattermostMessagesLoadedMsg {
	f.recent = append(f.recent, request)
	return MattermostMessagesLoadedMsg{Request: request}
}
func (f *fakeUIHistory) FetchOlder(_ context.Context, request HistoryRequest, before string) MattermostOlderMessagesLoadedMsg {
	f.older = append(f.older, struct {
		Request HistoryRequest
		Before  string
	}{request, before})
	return MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: before}
}

func TestMattermostHistorySelectionCacheFirstAndColdLoading(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "cached", Format: messages.FormatMattermostPlain}}}}
	a.SetMattermostHistoryService(h)
	_, cmd := a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"cached"}) || a.messagepane.IsLoading() {
		t.Fatalf("cache-first ids=%v loading=%v", got, a.messagepane.IsLoading())
	}
	if cmd == nil {
		t.Fatal("cached selection must verify live")
	}
	runHistoryCmd(cmd)
	_, cmd = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	if !a.messagepane.IsLoading() || len(a.messagepane.Messages()) != 0 || cmd == nil {
		t.Fatalf("cold selection loading=%v count=%d", a.messagepane.IsLoading(), len(a.messagepane.Messages()))
	}
}

func TestMattermostHistoryResultsPreserveCacheOnFailureAndClearOnAuthoritativeEmpty(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "cached"}}}}
	a.SetMattermostHistoryService(h)
	_, cmd := a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	runHistoryCmd(cmd)
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: request, Err: errors.New("offline")})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"cached"}) {
		t.Fatalf("failure erased cache: %v", got)
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: request, Messages: []messages.MessageItem{}, HasMore: false})
	if len(a.messagepane.Messages()) != 0 || a.messagepane.IsLoading() {
		t.Fatal("authoritative empty did not clear/finish")
	}
}

func TestMattermostHistoryDropsStaleChannelReselectAndServerCollision(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{}})
	_, _ = a.Update(ChannelSelectedMsg{ID: "same", Name: "Same"})
	first := a.activeHistoryRequest
	_, _ = a.Update(ChannelSelectedMsg{ID: "other", Name: "Other"})
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: first, Messages: []messages.MessageItem{{ID: "old-channel"}}})
	if len(a.messagepane.Messages()) != 0 {
		t.Fatal("late old channel applied")
	}
	_, _ = a.Update(ChannelSelectedMsg{ID: "same", Name: "Same"})
	second := a.activeHistoryRequest
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: first, Messages: []messages.MessageItem{{ID: "old-generation"}}})
	if len(a.messagepane.Messages()) != 0 || second.Generation == first.Generation {
		t.Fatal("late reselect applied or generation did not change")
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: HistoryRequest{ServerID: "s2", ChannelID: "same", Generation: second.Generation}, Messages: []messages.MessageItem{{ID: "other-server"}}})
	if len(a.messagepane.Messages()) != 0 {
		t.Fatal("same channel ID from other server applied")
	}
}

func TestMattermostOlderHistoryUsesOpaqueAnchorAndStopsAfterTerminalPage(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "opaque-old"}, {ID: "new"}}}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	cmd := a.maybeFetchOlderHistory(true)
	if cmd == nil {
		t.Fatal("older fetch not started")
	}
	runHistoryCmd(cmd)
	if len(h.older) != 1 || h.older[0].Before != "opaque-old" {
		t.Fatalf("older=%#v", h.older)
	}
	if again := a.maybeFetchOlderHistory(true); again != nil {
		t.Fatal("duplicate in-flight fetch")
	}
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "wrong", Messages: []messages.MessageItem{{ID: "ignored"}}, HasMore: false})
	if a.mattermostFetchingOlder[request] {
		t.Fatal("anchor mismatch did not clear own in-flight")
	}
	cmd = a.maybeFetchOlderHistory(true)
	if cmd == nil {
		t.Fatal("retry after mismatched anchor should be allowed")
	}
	runHistoryCmd(cmd)
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "opaque-old", Messages: []messages.MessageItem{}, HasMore: false})
	if next := a.maybeFetchOlderHistory(true); next != nil {
		t.Fatal("terminal page fetched repeatedly")
	}
}

func TestMattermostServerActivationQueuesInitialChannelHistory(t *testing.T) {
	a := NewApp()
	a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{}})
	_, cmd := a.Update(ServerReadyMsg{Server: ServerViewState{ServerID: ids.ServerID("s1"), InitialActive: true, Channels: testMattermostChannels()}})
	if cmd == nil {
		t.Fatal("server ready did not queue channel selection")
	}
	msg := cmd()
	if _, ok := findHistoryChannelSelected(msg); !ok {
		t.Fatalf("command=%T did not contain ChannelSelectedMsg", msg)
	}
}

func newMattermostHistoryApp(t *testing.T, server string) *App {
	t.Helper()
	a := NewApp()
	_, _ = a.Update(ServerReadyMsg{Server: ServerViewState{ServerID: ids.ServerID(server), InitialActive: true, Channels: testMattermostChannels()}})
	return a
}
func testMattermostChannels() []sidebar.ChannelItem {
	return []sidebar.ChannelItem{{ID: "c1", Name: "One", Type: "channel"}, {ID: "c2", Name: "Two", Type: "channel"}}
}
func historyItemIDs(items []messages.MessageItem) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].MessageID()
	}
	return out
}
func findHistoryChannelSelected(msg tea.Msg) (ChannelSelectedMsg, bool) {
	if m, ok := msg.(ChannelSelectedMsg); ok {
		return m, true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, cmd := range batch {
			if cmd != nil {
				if m, ok := findHistoryChannelSelected(cmd()); ok {
					return m, true
				}
			}
		}
	}
	return ChannelSelectedMsg{}, false
}

func runHistoryCmd(cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		for _, next := range batch {
			runHistoryCmd(next)
		}
	}
}
