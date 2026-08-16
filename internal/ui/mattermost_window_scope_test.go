package ui

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

func mattermostSyncingVisible(a *App) bool {
	return strings.Contains(a.statusbar.View(160), "○")
}

func twoChannelMattermostVerifyingApp(t *testing.T) (*App, *fakeUIHistory, wintree.LeafID, wintree.LeafID, HistoryRequest, HistoryRequest) {
	t.Helper()
	history := &fakeUIHistory{cached: map[string][]messages.MessageItem{
		"server-1:c1:": {{ID: "c1-cached"}},
		"server-1:c2:": {{ID: "c2-cached"}},
	}}
	a := NewApp()
	a.width = 200
	a.height = 50
	a.SetMattermostHistoryService(history)
	_, selectC1 := a.Update(ServerReadyMsg{Server: ServerViewState{ServerID: "server-1", InitialActive: true, Channels: testMattermostChannels()}})
	_, _ = a.Update(selectC1())
	w1 := a.focusedWin
	c1 := a.activeHistoryRequest
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("split failed")
	}
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	c2 := a.activeHistoryRequest
	return a, history, w1, w2, c1, c2
}

func crossChannelMattermostApp(t *testing.T, send MattermostSendService, history MattermostHistoryService) (*App, wintree.LeafID, wintree.LeafID) {
	t.Helper()
	a := newMattermostSendApp(t, send)
	a.SetMattermostHistoryService(history)
	w1 := a.focusedWin
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("split failed")
	}
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	return a, w1, w2
}

func TestMattermostFocusRestoresC1ScopeBeforeSend(t *testing.T) {
	send := &recordingMattermostSendService{}
	a, w1, _ := crossChannelMattermostApp(t, send, nil)
	_ = a.focusWindow(w1)

	_, cmd := a.Update(SendMessageMsg{ChannelID: "c1", Text: "from c1"})
	if cmd == nil {
		t.Fatal("C1 send returned nil after focusing C1 window")
	}
	_ = cmd()
	if len(send.requests) != 1 {
		t.Fatalf("requests=%#v", send.requests)
	}
	request := send.requests[0]
	if request.ServerID != "server-1" || request.ChannelID != "c1" || request.Generation == 0 || request.Text != "from c1" {
		t.Fatalf("request=%#v want focused C1 scope", request)
	}
	if rows := a.winModels[w1].Messages(); len(rows) != 1 || rows[0].DeliveryState != messages.DeliveryPending {
		t.Fatalf("C1 optimistic rows=%#v", rows)
	}
}

func TestMattermostCrossChannelFocusDoesNotCancelOtherWindowScope(t *testing.T) {
	history := &fakeUIHistory{cached: map[string][]messages.MessageItem{}}
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	a.SetMattermostHistoryService(history)
	w1 := a.focusedWin
	c1Context := a.activeHistoryContext
	c1Request := a.activeHistoryRequest
	_ = a.splitWindow(wintree.SplitSideBySide)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})

	select {
	case <-c1Context.Done():
		t.Fatal("selecting C2 in the cloned window canceled C1 still owned by W1")
	default:
	}
	_ = a.focusWindow(w1)
	if a.activeHistoryContext != c1Context || a.activeHistoryRequest != c1Request {
		t.Fatalf("active scope=(%#v,%p) want C1 (%#v,%p)", a.activeHistoryRequest, a.activeHistoryContext, c1Request, c1Context)
	}
}

func TestMattermostRetryAfterCrossChannelFocusKeepsCorrelationAndText(t *testing.T) {
	send := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, send)
	request := a.activeHistoryRequest
	_, firstCmd := a.Update(SendMessageMsg{ChannelID: "c1", Text: "retry original"})
	correlationID := a.messagepane.Messages()[0].CorrelationID
	send.result = MattermostMessageSendFailedMsg{Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: "c1", Generation: request.Generation, Text: "retry original", CorrelationID: correlationID}}
	_, _ = a.Update(firstCmd())
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	_ = a.focusWindow(w1)

	retryCmd := handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if retryCmd == nil {
		t.Fatal("retry returned nil after returning to failed C1 row")
	}
	_ = retryCmd()
	if len(send.requests) != 2 {
		t.Fatalf("requests=%#v", send.requests)
	}
	retry := send.requests[1]
	if retry.ChannelID != "c1" || retry.CorrelationID != correlationID || retry.Text != "retry original" {
		t.Fatalf("retry=%#v want original correlation/text in C1", retry)
	}
	row := mattermostRowByCorrelation(t, a.winModels[w1], correlationID)
	if row.DeliveryState != messages.DeliveryPending || row.DeliveryGeneration != retry.Generation {
		t.Fatalf("row=%#v retry=%#v", row, retry)
	}
}

func TestMattermostOlderHistoryUsesFocusedWindowScopeAndContext(t *testing.T) {
	history := &fakeUIHistory{cached: map[string][]messages.MessageItem{}}
	a, w1, w2 := crossChannelMattermostApp(t, &recordingMattermostSendService{}, history)
	a.winModels[w1].SetMessages([]messages.MessageItem{{ID: "c1-anchor"}})
	a.winModels[w2].SetMessages([]messages.MessageItem{{ID: "c2-anchor"}})

	_ = a.focusWindow(w1)
	c1Request, c1Context := a.activeHistoryRequest, a.activeHistoryContext
	runHistoryCmd(a.maybeFetchOlderHistory(true))
	_ = a.focusWindow(w2)
	c2Request, c2Context := a.activeHistoryRequest, a.activeHistoryContext
	runHistoryCmd(a.maybeFetchOlderHistory(true))

	if len(history.older) != 2 {
		t.Fatalf("older=%#v", history.older)
	}
	if history.older[0].Request != c1Request || history.older[0].Before != "c1-anchor" || history.older[1].Request != c2Request || history.older[1].Before != "c2-anchor" {
		t.Fatalf("older=%#v want per-window requests", history.older)
	}
	if c1Request.ChannelID != "c1" || c2Request.ChannelID != "c2" || c1Request.Generation == c2Request.Generation || c1Context == c2Context {
		t.Fatalf("C1=(%#v,%p) C2=(%#v,%p)", c1Request, c1Context, c2Request, c2Context)
	}
}

func TestMattermostLateResultFromDiscardedWindowScopeIsIgnored(t *testing.T) {
	a, w1, _ := crossChannelMattermostApp(t, &recordingMattermostSendService{}, nil)
	_ = a.focusWindow(w1)
	old := a.activeHistoryRequest
	_, _ = a.Update(ChannelSelectedMsg{ID: "c3", Name: "Three"})
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: old, Messages: []messages.MessageItem{{ID: "stale"}}})
	if a.messagepane.ContainsMessageID("stale") {
		t.Fatal("discarded C1 scope result applied to C3")
	}
}

func TestMattermostFinalScopeReleaseCancelsAndFailsSurvivingPendingCopy(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	w1 := a.focusedWin
	_, _ = a.Update(SendMessageMsg{ChannelID: "c1", Text: "pending"})
	pending := a.winModels[w1].Messages()[0]
	oldContext := a.activeHistoryContext
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})

	// Give W2 an independent current C1 scope while retaining the copied row.
	a.releaseMattermostWindowScope(w2)
	newScope := a.newMattermostHistoryScope("server-1", "c1")
	a.mattermostWindowScopes[w2] = newScope
	a.wins.SetChannel(w2, wintree.Channel{ID: "c1", Name: "One"})
	a.winModels[w2].SetMessages([]messages.MessageItem{pending})
	a.setFocusedMattermostScope(newScope)
	a.activeChannelID = "c1"

	// Replacing W1's last ownership of the old scope must fail W2's copied row.
	_ = a.focusWindow(w1)
	_, _ = a.Update(ChannelSelectedMsg{ID: "c3", Name: "Three"})

	select {
	case <-oldContext.Done():
	default:
		t.Fatal("discarded old C1 scope was not canceled")
	}
	row := mattermostRowByCorrelation(t, a.winModels[w2], pending.CorrelationID)
	if row.DeliveryState != messages.DeliveryFailed {
		t.Fatalf("surviving copied row=%#v want failed after old scope cancellation", row)
	}
	_, _ = a.Update(MattermostMessageSentMsg{
		Request: MattermostSendRequest{ServerID: "server-1", ChannelID: "c1", Generation: pending.DeliveryGeneration, CorrelationID: pending.CorrelationID},
		Message: messages.MessageItem{ID: "stale-success", CorrelationID: pending.CorrelationID},
	})
	row = mattermostRowByCorrelation(t, a.winModels[w2], pending.CorrelationID)
	if row.ID == "stale-success" || row.DeliveryState != messages.DeliveryFailed {
		t.Fatalf("canceled stale result changed row=%#v", row)
	}
}

func TestMattermostRetryResultFansOutToCopiedC1RowWithFreshWindowScope(t *testing.T) {
	send := &recordingMattermostSendService{}
	a := newMattermostSendApp(t, send)
	request := a.activeHistoryRequest
	_, firstCmd := a.Update(SendMessageMsg{ChannelID: "c1", Text: "retry copied"})
	correlationID := a.messagepane.Messages()[0].CorrelationID
	send.result = MattermostMessageSendFailedMsg{Request: MattermostSendRequest{ServerID: request.ServerID, ChannelID: "c1", Generation: request.Generation, Text: "retry copied", CorrelationID: correlationID}}
	_, _ = a.Update(firstCmd())
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin

	// Give W2 an independent fresh C1 scope while retaining the failed copy.
	a.releaseMattermostWindowScope(w2)
	fresh := a.newMattermostHistoryScope("server-1", "c1")
	a.mattermostWindowScopes[w2] = fresh
	a.setFocusedMattermostScope(fresh)
	retryCmd := handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEnter})
	if retryCmd == nil {
		t.Fatal("retry command is nil")
	}
	for _, win := range []wintree.LeafID{w1, w2} {
		row := mattermostRowByCorrelation(t, a.winModels[win], correlationID)
		if row.DeliveryGeneration != fresh.request.Generation || row.DeliveryState != messages.DeliveryPending {
			t.Fatalf("window %v row=%#v want fresh pending scope", win, row)
		}
	}
	send.result = MattermostMessageSentMsg{Request: MattermostSendRequest{ServerID: fresh.request.ServerID, ChannelID: "c1", Generation: fresh.request.Generation, Text: "retry copied", CorrelationID: correlationID}, Message: messages.MessageItem{ID: "retry-success", CorrelationID: correlationID, Text: "sent"}}
	_, _ = a.Update(retryCmd())
	for _, win := range []wintree.LeafID{w1, w2} {
		rows := a.winModels[win].Messages()
		if len(rows) != 1 || rows[0].ID != "retry-success" {
			t.Fatalf("window %v rows=%#v want retry success", win, rows)
		}
	}
}

func TestMattermostCloseOnlyAndResetCancelDiscardedWindowScopes(t *testing.T) {
	t.Run("close", func(t *testing.T) {
		a, w1, _ := crossChannelMattermostApp(t, &recordingMattermostSendService{}, nil)
		_ = a.focusWindow(w1)
		c1Context := a.activeHistoryContext
		_ = a.closeWindow()
		assertContextCanceled(t, c1Context)
	})
	t.Run("only", func(t *testing.T) {
		a, w1, w2 := crossChannelMattermostApp(t, &recordingMattermostSendService{}, nil)
		_ = a.focusWindow(w1)
		c1Context := a.activeHistoryContext
		_ = a.focusWindow(w2)
		a.onlyWindow()
		assertContextCanceled(t, c1Context)
	})
	t.Run("reset", func(t *testing.T) {
		a, w1, _ := crossChannelMattermostApp(t, &recordingMattermostSendService{}, nil)
		_ = a.focusWindow(w1)
		c1Context := a.activeHistoryContext
		a.resetWindowTree()
		assertContextCanceled(t, c1Context)
	})
}

func TestMattermostSameChannelSplitSharesScopeUntilLastOwnerReleases(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	w1 := a.focusedWin
	request, ctx := a.activeHistoryRequest, a.activeHistoryContext
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin
	_ = a.focusWindow(w1)
	if a.activeHistoryRequest != request || a.activeHistoryContext != ctx {
		t.Fatal("source scope changed after same-channel split")
	}
	_ = a.focusWindow(w2)
	if a.activeHistoryRequest != request || a.activeHistoryContext != ctx {
		t.Fatal("clone did not retain source scope")
	}
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	select {
	case <-ctx.Done():
		t.Fatal("one owner releasing shared scope canceled the remaining owner")
	default:
	}
	_ = a.focusWindow(w1)
	if a.activeHistoryRequest != request || a.activeHistoryContext != ctx {
		t.Fatal("remaining owner lost shared scope")
	}
}

func TestMattermostScopeRequestsRemainGloballyUniqueAcrossWindows(t *testing.T) {
	a, w1, w2 := crossChannelMattermostApp(t, &recordingMattermostSendService{}, nil)
	_ = a.focusWindow(w1)
	c1 := a.activeHistoryRequest
	_ = a.focusWindow(w2)
	c2 := a.activeHistoryRequest
	if c1.Generation == 0 || c2.Generation == 0 || c1.Generation == c2.Generation {
		t.Fatalf("requests not globally unique: c1=%#v c2=%#v", c1, c2)
	}
	if reflect.DeepEqual(c1, c2) {
		t.Fatalf("requests unexpectedly equal: %#v", c1)
	}
}

func TestMattermostLiveHistoryRequestsTrackRetainedSplitScopes(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	var snapshots [][]HistoryRequest
	a.SetHistoryRequestsObserver(func(requests []HistoryRequest) {
		snapshots = append(snapshots, append([]HistoryRequest(nil), requests...))
	})
	c1 := a.activeHistoryRequest
	w1 := a.focusedWin
	_ = a.splitWindow(wintree.SplitSideBySide)
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	c2 := a.activeHistoryRequest

	if got := snapshots[len(snapshots)-1]; !reflect.DeepEqual(got, []HistoryRequest{c1, c2}) {
		t.Fatalf("live requests=%#v want c1/c2 %#v", got, []HistoryRequest{c1, c2})
	}
	a.releaseMattermostWindowScope(w1)
	if got := snapshots[len(snapshots)-1]; !reflect.DeepEqual(got, []HistoryRequest{c2}) {
		t.Fatalf("after c1 release live requests=%#v want c2 only", got)
	}
	a.releaseMattermostWindowScope(w2)
	if got := snapshots[len(snapshots)-1]; len(got) != 0 {
		t.Fatalf("after final release live requests=%#v want empty", got)
	}
}

func TestMattermostRecentCompletionOnlyClearsItsOwnScopeSyncing(t *testing.T) {
	a, _, w1, w2, c1, c2 := twoChannelMattermostVerifyingApp(t)
	if !mattermostSyncingVisible(a) {
		t.Fatal("focused C2 verification indicator missing")
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c1, Messages: []messages.MessageItem{{ID: "c1-live"}}})
	if !mattermostSyncingVisible(a) {
		t.Fatal("unfocused C1 completion cleared focused C2 indicator")
	}
	_ = a.focusWindow(w1)
	if mattermostSyncingVisible(a) {
		t.Fatal("completed C1 scope restored as syncing")
	}
	_ = a.focusWindow(w2)
	if !mattermostSyncingVisible(a) {
		t.Fatal("in-flight C2 scope did not restore syncing")
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c2, Messages: []messages.MessageItem{{ID: "c2-live"}}})
	if mattermostSyncingVisible(a) {
		t.Fatal("C2 completion did not clear focused indicator")
	}
}

func TestMattermostRecentErrorClearsOnlyExactScopeAndFocusReappliesState(t *testing.T) {
	a, _, w1, w2, c1, c2 := twoChannelMattermostVerifyingApp(t)
	_ = a.focusWindow(w1)
	if !mattermostSyncingVisible(a) {
		t.Fatal("in-flight C1 scope did not restore syncing")
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c2, Err: errors.New("offline")})
	if !mattermostSyncingVisible(a) {
		t.Fatal("unfocused C2 error cleared focused C1 indicator")
	}
	_ = a.focusWindow(w2)
	if mattermostSyncingVisible(a) {
		t.Fatal("errored C2 scope restored as syncing")
	}
	_ = a.focusWindow(w1)
	if !mattermostSyncingVisible(a) {
		t.Fatal("still in-flight C1 scope lost syncing after focus round-trip")
	}
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c1, Err: errors.New("offline")})
	if mattermostSyncingVisible(a) {
		t.Fatal("focused C1 error did not clear indicator")
	}
}

func TestMattermostColdLoadDoesNotShowSyncingWhileCachedVerificationDoes(t *testing.T) {
	history := &fakeUIHistory{cached: map[string][]messages.MessageItem{
		"server-1:c2:": {{ID: "c2-cached"}},
	}}
	a := NewApp()
	a.width = 200
	a.height = 50
	a.SetMattermostHistoryService(history)
	_, selectC1 := a.Update(ServerReadyMsg{Server: ServerViewState{ServerID: "server-1", InitialActive: true, Channels: testMattermostChannels()}})
	_, _ = a.Update(selectC1())
	w1 := a.focusedWin
	c1 := a.activeHistoryRequest
	if mattermostSyncingVisible(a) {
		t.Fatal("cold C1 load displayed cache-verification indicator")
	}
	if !a.messagepane.IsLoading() {
		t.Fatal("cold C1 load lost loading state")
	}
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("split failed")
	}
	w2 := a.focusedWin
	_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
	c2 := a.activeHistoryRequest
	if !mattermostSyncingVisible(a) {
		t.Fatal("cached C2 verification indicator missing")
	}
	if a.messagepane.IsLoading() {
		t.Fatal("cached C2 verification incorrectly uses cold loading state")
	}

	_ = a.focusWindow(w1)
	if mattermostSyncingVisible(a) || !a.messagepane.IsLoading() {
		t.Fatalf("cold C1 focus state syncing=%v loading=%v", mattermostSyncingVisible(a), a.messagepane.IsLoading())
	}
	_ = a.focusWindow(w2)
	if !mattermostSyncingVisible(a) || a.messagepane.IsLoading() {
		t.Fatalf("cached C2 focus state syncing=%v loading=%v", mattermostSyncingVisible(a), a.messagepane.IsLoading())
	}

	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c2, Err: errors.New("offline")})
	if mattermostSyncingVisible(a) {
		t.Fatal("cached C2 error did not clear verification indicator")
	}
	_ = a.focusWindow(w1)
	_, _ = a.Update(MattermostMessagesLoadedMsg{Request: c1, Messages: []messages.MessageItem{{ID: "c1-live"}}})
	if mattermostSyncingVisible(a) || a.messagepane.IsLoading() {
		t.Fatalf("cold C1 completion syncing=%v loading=%v", mattermostSyncingVisible(a), a.messagepane.IsLoading())
	}
}

func assertContextCanceled(t *testing.T, ctx context.Context) {
	t.Helper()
	select {
	case <-ctx.Done():
	default:
		t.Fatal("context not canceled")
	}
}
