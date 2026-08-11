package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
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
	recentContexts []context.Context
}

func (f *fakeUIHistory) ReadCached(request HistoryRequest, before string) ([]messages.MessageItem, error) {
	return append([]messages.MessageItem(nil), f.cached[string(request.ServerID)+":"+request.ChannelID+":"+before]...), nil
}
func (f *fakeUIHistory) FetchRecent(ctx context.Context, request HistoryRequest) MattermostMessagesLoadedMsg {
	f.recentContexts = append(f.recentContexts, ctx)
	f.recent = append(f.recent, request)
	return MattermostMessagesLoadedMsg{Request: request}
}

func TestMattermostHistorySelectionCancelsPreviousGenerationAndBoundsState(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{}}
	a.SetMattermostHistoryService(h)
	_, cmd1 := a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	runHistoryCmd(cmd1)
	ctx1 := h.recentContexts[len(h.recentContexts)-1]
	first := a.activeHistoryRequest
	a.mattermostFetchingOlder[first] = true
	a.mattermostHistoryExhausted[first] = true
	_, cmd2 := a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	runHistoryCmd(cmd2)
	select {
	case <-ctx1.Done():
	default:
		t.Fatal("previous generation context not canceled")
	}
	if len(a.mattermostFetchingOlder) > 0 || len(a.mattermostHistoryExhausted) > 0 {
		t.Fatalf("state maps fetching=%d exhausted=%d", len(a.mattermostFetchingOlder), len(a.mattermostHistoryExhausted))
	}
	if len(h.recentContexts) != 2 {
		t.Fatalf("contexts=%d", len(h.recentContexts))
	}
}

func TestMattermostServerSwitchWithoutChannelsCancelsHistoryGeneration(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{}}
	a.SetMattermostHistoryService(h)
	_, cmd := a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	runHistoryCmd(cmd)
	ctx := h.recentContexts[len(h.recentContexts)-1]
	_, _ = a.Update(ServerSwitchedMsg{Server: ServerViewState{ServerID: "s2"}})
	select {
	case <-ctx.Done():
	default:
		t.Fatal("server switch without channel selection did not cancel history")
	}
	if len(a.mattermostFetchingOlder) > 0 || len(a.mattermostHistoryExhausted) > 0 {
		t.Fatal("server switch retained generation maps")
	}
}

func TestMattermostServerActivationImmediatelyInvalidatesOldRecentAndOlderResults(t *testing.T) {
	for _, tt := range []struct {
		name string
		msg  tea.Msg
	}{
		{"switched zero", ServerSwitchedMsg{Server: ServerViewState{ServerID: "s2"}}},
		{"switched channels", ServerSwitchedMsg{Server: ServerViewState{ServerID: "s2", Channels: testMattermostChannels()}}},
		{"refreshed channels", ServerRefreshedMsg{Server: ServerViewState{ServerID: "s1", Channels: testMattermostChannels()}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			a := newMattermostHistoryApp(t, "s1")
			a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{}})
			_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
			old := a.activeHistoryRequest
			_, cmd := a.Update(tt.msg)
			if a.activeHistoryRequest == old || a.activeHistoryRequest.ChannelID != "" {
				t.Fatalf("request not invalidated: old=%#v active=%#v", old, a.activeHistoryRequest)
			}
			_, _ = a.Update(MattermostMessagesLoadedMsg{Request: old, Messages: []messages.MessageItem{{ID: "stale-recent"}}})
			_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: old, AnchorID: "anything", Messages: []messages.MessageItem{{ID: "stale-older"}}})
			for _, id := range historyItemIDs(a.messagepane.Messages()) {
				if strings.HasPrefix(id, "stale-") {
					t.Fatalf("stale applied before cmd: %v", historyItemIDs(a.messagepane.Messages()))
				}
			}
			_ = cmd
		})
	}
}

func TestMattermostInitialServerReadyImmediatelyInvalidatesOldResults(t *testing.T) {
	a := NewApp()
	a.features = MattermostTask8Features()
	a.historyGeneration = 7
	old := HistoryRequest{ServerID: "old", ChannelID: "c1", Generation: 7}
	a.activeHistoryRequest = old
	_, cmd := a.Update(ServerReadyMsg{Server: ServerViewState{ServerID: "s1", InitialActive: true, Channels: testMattermostChannels()}})
	if cmd == nil || a.activeHistoryRequest == old || a.activeHistoryRequest.ServerID != "s1" || a.activeHistoryRequest.ChannelID != "" {
		t.Fatalf("active=%#v cmd=%v", a.activeHistoryRequest, cmd)
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: old, Messages: []messages.MessageItem{{ID: "stale"}}})
	if len(a.messagepane.Messages()) != 0 {
		t.Fatal("old ready-gap result applied")
	}
}

func TestMattermostOlderLiveReconcilesCapturedCachedSegment(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "anchor"}, {ID: "new"}}, "s1:c1:anchor": {{ID: "p1", Text: "cached"}, {ID: "p3"}}}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	cmd := a.maybeFetchOlderHistory(true)
	runHistoryCmd(cmd)
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "anchor", CachedIDs: []string{"p1", "p3"}, Messages: []messages.MessageItem{{ID: "p1", Text: "updated"}, {ID: "p2"}}, HasMore: true})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"p1", "p2", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestMattermostRecentResultPreservesOlderPageLoadedWhileInFlight(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "p3", Text: "cached"}, {ID: "p4"}}}})
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	a.messagepane.PrependMessages([]messages.MessageItem{{ID: "p1"}, {ID: "p2"}})
	a.messagepane.SelectByID("p2")
	_ = a.messagepane.View(8, 24)
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: request, CachedIDs: []string{"p3", "p4"}, Messages: []messages.MessageItem{{ID: "p3", Text: "updated"}, {ID: "p5"}}, HasMore: true})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"p1", "p2", "p3", "p5"}) {
		t.Fatalf("ids=%v", got)
	}
	selected, _ := a.messagepane.SelectedMessage()
	if selected.MessageID() != "p2" {
		t.Fatalf("selected=%q", selected.MessageID())
	}
}

func TestMattermostTerminalRecentRemovesOlderPageLoadedWhileInFlight(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "cached"}}}})
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	a.messagepane.PrependMessages([]messages.MessageItem{{ID: "older"}})
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: request, CachedIDs: []string{"cached"}, Messages: []messages.MessageItem{{ID: "live"}}, HasMore: false})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"live"}) {
		t.Fatalf("ids=%v", got)
	}
	if !a.mattermostHistoryExhausted[request] {
		t.Fatal("terminal recent not exhausted")
	}
}

func TestMattermostTerminalOlderRemovesStalePrefixAndExhausts(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	a.SetMattermostHistoryService(&fakeUIHistory{cached: map[string][]messages.MessageItem{}})
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	a.messagepane.SetMessages([]messages.MessageItem{{ID: "stale"}, {ID: "cached"}, {ID: "anchor"}, {ID: "new"}})
	a.mattermostFetchingOlder[request] = true
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "anchor", CachedIDs: []string{"cached"}, Messages: []messages.MessageItem{{ID: "oldest"}}, HasMore: false})
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"oldest", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
	if !a.mattermostHistoryExhausted[request] || a.mattermostFetchingOlder[request] {
		t.Fatal("terminal older state incorrect")
	}
}

func TestMattermostOlderDispatchAttachesCapturedCachedIDs(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "anchor"}}, "s1:c1:anchor": {{ID: "p1"}, {ID: "p3"}}}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	msg, ok := findOlderLoadedMsg(a.maybeFetchOlderHistory(true))
	if !ok {
		t.Fatal("older result absent")
	}
	if !reflect.DeepEqual(msg.CachedIDs, []string{"p1", "p3"}) {
		t.Fatalf("cached ids=%v", msg.CachedIDs)
	}
}

func TestMattermostOlderFailureAfterCachedPrependClearsInflightAndUsesAdvancedAnchor(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "p3"}, {ID: "p4"}}, "s1:c1:p3": {{ID: "p1"}, {ID: "p2"}}}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	request := a.activeHistoryRequest
	cmd := a.maybeFetchOlderHistory(true)
	runHistoryCmd(cmd)
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "p3", Err: errors.New("offline")})
	if a.mattermostFetchingOlder[request] {
		t.Fatal("inflight retained")
	}
	cmd = a.maybeFetchOlderHistory(true)
	runHistoryCmd(cmd)
	if len(h.older) != 2 || h.older[1].Before != "p1" {
		t.Fatalf("older=%#v", h.older)
	}
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

func TestMattermostOlderHistoryPrependsMultipleCachedPagesOfflineAndAdvancesAnchor(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{
		"s1:c1:":   {{ID: "p5"}, {ID: "p6"}},
		"s1:c1:p5": {{ID: "p3"}, {ID: "p4"}},
		"s1:c1:p3": {{ID: "p1"}, {ID: "p2"}},
	}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	cmd := a.maybeFetchOlderHistory(true)
	if cmd == nil {
		t.Fatal("first older nil")
	}
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"p3", "p4", "p5", "p6"}) {
		t.Fatalf("first cache=%v", got)
	}
	runHistoryCmd(cmd)
	request := a.activeHistoryRequest
	_, _ = a.Update(MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: "p5", Err: errors.New("offline")})
	cmd = a.maybeFetchOlderHistory(true)
	if cmd == nil {
		t.Fatal("second older nil")
	}
	if got := historyItemIDs(a.messagepane.Messages()); !reflect.DeepEqual(got, []string{"p1", "p2", "p3", "p4", "p5", "p6"}) {
		t.Fatalf("second cache=%v", got)
	}
	runHistoryCmd(cmd)
	if got := []string{h.older[0].Before, h.older[1].Before}; !reflect.DeepEqual(got, []string{"p5", "p3"}) {
		t.Fatalf("anchors=%v", got)
	}
}

func TestMattermostRecentLiveReplacementPreservesScrolledSelection(t *testing.T) {
	a := newMattermostHistoryApp(t, "s1")
	h := &fakeUIHistory{cached: map[string][]messages.MessageItem{"s1:c1:": {{ID: "a"}, {ID: "b", Text: strings.Repeat("b ", 30)}, {ID: "c"}}}}
	a.SetMattermostHistoryService(h)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c1", Name: "One"})
	a.messagepane.SelectByID("b")
	_ = a.messagepane.View(8, 24)
	before := a.messagepane.YOffset()
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: a.activeHistoryRequest, Messages: []messages.MessageItem{{ID: "a"}, {ID: "b", Text: strings.Repeat("live ", 30)}, {ID: "c"}, {ID: "d"}}})
	selected, _ := a.messagepane.SelectedMessage()
	if selected.MessageID() != "b" {
		t.Fatalf("selected=%q", selected.MessageID())
	}
	_ = a.messagepane.View(8, 24)
	if a.messagepane.YOffset() == 0 && before != 0 {
		t.Fatalf("viewport reset: before=%d after=%d", before, a.messagepane.YOffset())
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

func findOlderLoadedMsg(cmd tea.Cmd) (MattermostOlderMessagesLoadedMsg, bool) {
	if cmd == nil {
		return MattermostOlderMessagesLoadedMsg{}, false
	}
	msg := cmd()
	if loaded, ok := msg.(MattermostOlderMessagesLoadedMsg); ok {
		return loaded, true
	}
	if batch, ok := msg.(tea.BatchMsg); ok {
		for _, next := range batch {
			if loaded, ok := findOlderLoadedMsg(next); ok {
				return loaded, true
			}
		}
	}
	return MattermostOlderMessagesLoadedMsg{}, false
}
