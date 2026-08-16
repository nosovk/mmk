package ui

import (
	"context"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/statusbar"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

type recordingMattermostThreadService struct {
	cached  []messages.MessageItem
	result  ThreadRepliesLoadedMsg
	context context.Context
	request HistoryRequest
}

func (*recordingMattermostThreadService) Fetch(ids.ChannelID, ids.ThreadTS) tea.Msg { return nil }
func (*recordingMattermostThreadService) CacheRead(ids.ChannelID, ids.ThreadTS) []messages.MessageItem {
	return nil
}
func (s *recordingMattermostThreadService) FetchScoped(ctx context.Context, request HistoryRequest, _ ids.ThreadTS) tea.Msg {
	s.context, s.request = ctx, request
	return s.result
}
func (s *recordingMattermostThreadService) CacheReadScoped(HistoryRequest, ids.ThreadTS) []messages.MessageItem {
	return s.cached
}
func (*recordingMattermostThreadService) Mark(ids.ChannelID, ids.ThreadTS, ids.MessageTS) {}
func (*recordingMattermostThreadService) SendReply(ids.ChannelID, ids.ThreadTS, string) tea.Msg {
	return nil
}
func (*recordingMattermostThreadService) ListFetch(ids.TeamID) tea.Msg         { return nil }
func (*recordingMattermostThreadService) EnsureSubscriptions(ids.TeamID)       {}
func (*recordingMattermostThreadService) ChannelLastRead(ids.ChannelID) string { return "" }

func TestMattermostThreadOpenShowsCacheThenAuthoritativeLiveReplies(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	active := a.activeHistoryRequest
	root := messages.MessageItem{ID: "root-1", Format: messages.FormatMattermostPlain, Text: "root"}
	cached := messages.MessageItem{ID: "cached-reply", RootID: "root-1", Format: messages.FormatMattermostPlain, Text: "cached"}
	live := messages.MessageItem{ID: "live-reply", RootID: "root-1", Format: messages.FormatMattermostPlain, Text: "live"}
	service := &recordingMattermostThreadService{cached: []messages.MessageItem{root, cached}, result: ThreadRepliesLoadedMsg{Request: active, ThreadTS: "root-1", Replies: []messages.MessageItem{live}}}
	a.SetThreadService(service)

	msgs := drainBatch(a.openThreadPanel(root, active.ChannelID, "root-1"))
	if len(msgs) != 2 {
		t.Fatalf("open messages=%#v want cache and live", msgs)
	}
	_, _ = a.Update(msgs[0])
	if got := a.threadPanel.Replies(); len(got) != 1 || got[0].ID != "cached-reply" {
		t.Fatalf("cached replies=%#v", got)
	}
	_, _ = a.Update(msgs[1])
	if got := a.threadPanel.Replies(); len(got) != 1 || got[0].ID != "live-reply" {
		t.Fatalf("live replies=%#v", got)
	}
	if service.context != a.activeHistoryContext || service.request != active {
		t.Fatalf("fetch scope context=%p request=%#v want %p %#v", service.context, service.request, a.activeHistoryContext, active)
	}
}

func TestMattermostThreadRootOnlySuccessClearsCacheAndFailurePreservesIt(t *testing.T) {
	for _, tc := range []struct {
		name    string
		replies []messages.MessageItem
		want    string
	}{
		{name: "root only", replies: []messages.MessageItem{}, want: ""},
		{name: "failure", replies: nil, want: "cached-reply"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a := newMattermostSendApp(t, &recordingMattermostSendService{})
			active := a.activeHistoryRequest
			root := messages.MessageItem{ID: "root-1", Format: messages.FormatMattermostPlain}
			cached := messages.MessageItem{ID: "cached-reply", RootID: "root-1", Format: messages.FormatMattermostPlain}
			service := &recordingMattermostThreadService{cached: []messages.MessageItem{root, cached}, result: ThreadRepliesLoadedMsg{Request: active, ThreadTS: "root-1", Replies: tc.replies}}
			a.SetThreadService(service)
			msgs := drainBatch(a.openThreadPanel(root, active.ChannelID, "root-1"))
			_, _ = a.Update(msgs[0])
			_, _ = a.Update(msgs[1])
			got := a.threadPanel.Replies()
			if tc.want == "" && len(got) != 0 {
				t.Fatalf("replies=%#v want cleared", got)
			}
			if tc.want != "" && (len(got) != 1 || got[0].ID != tc.want) {
				t.Fatalf("replies=%#v want preserved %q", got, tc.want)
			}
		})
	}
}

func TestMattermostScopedLiveFetchHydratesReplyOpenedRootStub(t *testing.T) {
	a := newMattermostSendApp(t, &recordingMattermostSendService{})
	active := a.activeHistoryRequest
	reply := messages.MessageItem{ID: "reply-1", RootID: "root-1", Format: messages.FormatMattermostPlain, Text: "reply"}
	root := messages.MessageItem{ID: "root-1", Format: messages.FormatMattermostPlain, UserID: "u1", UserName: "alice", CreatedAt: 123, Text: "authoritative root"}
	service := &recordingMattermostThreadService{
		cached: []messages.MessageItem{root, reply},
		result: ThreadRepliesLoadedMsg{Request: active, ThreadTS: "root-1", Replies: []messages.MessageItem{reply}},
	}
	a.SetThreadService(service)
	a.messagepane.SetMessages([]messages.MessageItem{reply})

	cmd := a.openThreadForSelectedMessage()
	if cmd == nil || !isThreadParentStub(a.threadPanel.ParentMsg(), "root-1") {
		t.Fatalf("initial parent=%#v want root stub", a.threadPanel.ParentMsg())
	}
	msgs := drainBatch(cmd)
	if len(msgs) != 2 {
		t.Fatalf("open messages=%#v want cached and live", msgs)
	}
	_, _ = a.Update(msgs[1])
	if got := a.threadPanel.ParentMsg(); !reflect.DeepEqual(got, root) {
		t.Fatalf("hydrated parent=%#v want %#v", got, root)
	}
}

func openMattermostThreadForSend(t *testing.T, service *recordingMattermostSendService) *App {
	t.Helper()
	a := newMattermostSendApp(t, service)
	a.SetNowTimestampFormatter(func() string { return "9:41 AM" })
	a.threadVisible = true
	a.threadPanel.SetThread(messages.MessageItem{ID: "root-1", Format: messages.FormatMattermostPlain, ReplyCount: 0}, nil, a.activeChannelID, "root-1")
	return a
}

func mattermostThreadReplyMsg(a *App, text string) SendThreadReplyMsg {
	return SendThreadReplyMsg{
		ChannelID: a.activeHistoryRequest.ChannelID,
		ThreadTS:  a.threadPanel.ThreadTS(),
		Text:      text,
		Request:   a.activeHistoryRequest,
		RootID:    a.threadPanel.ThreadTS(),
		Context:   a.activeHistoryContext,
	}
}

func createMattermostThreadReplyIntent(t *testing.T, a *App, text string) (tea.Cmd, HistoryRequest, context.Context) {
	t.Helper()
	a.focusedPanel = PanelThread
	a.threadCompose.SetValue(text)
	request, ctx := a.activeHistoryRequest, a.activeHistoryContext
	cmd := a.handleInsertMode(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("thread reply intent command is nil")
	}
	return cmd, request, ctx
}

func TestMattermostThreadReplyIntentCapturesOriginScopeAndContext(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	cmd, request, ctx := createMattermostThreadReplyIntent(t, a, "origin")

	msg, ok := cmd().(SendThreadReplyMsg)
	if !ok {
		t.Fatalf("intent=%T want SendThreadReplyMsg", cmd())
	}
	if msg.Request != request || msg.RootID != "root-1" || msg.Context != ctx {
		t.Fatalf("intent=%#v want request=%#v root=root-1 context=%p", msg, request, ctx)
	}
}

func TestMattermostDelayedThreadReplyIntentDoesNotBorrowNewScope(t *testing.T) {
	for _, tc := range []struct {
		name   string
		mutate func(*App)
	}{
		{name: "server", mutate: func(a *App) { a.activeHistoryRequest.ServerID = "server-2" }},
		{name: "channel", mutate: func(a *App) {
			a.activeHistoryRequest.ChannelID = "c2"
			a.threadPanel.SetThread(messages.MessageItem{ID: "root-2"}, nil, "c2", "root-2")
		}},
		{name: "generation", mutate: func(a *App) { a.activeHistoryRequest.Generation++ }},
		{name: "window", mutate: func(a *App) {
			_ = a.splitWindow(wintree.SplitSideBySide)
			_, _ = a.Update(ChannelSelectedMsg{ID: "c2", Name: "Two"})
		}},
		{name: "thread", mutate: func(a *App) {
			a.threadPanel.SetThread(messages.MessageItem{ID: "root-2"}, nil, a.activeHistoryRequest.ChannelID, "root-2")
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			service := &recordingMattermostSendService{}
			a := openMattermostThreadForSend(t, service)
			cmd, _, oldCtx := createMattermostThreadReplyIntent(t, a, "delayed")
			tc.mutate(a)
			newCtx := context.WithValue(context.Background(), struct{}{}, "new")
			a.activeHistoryContext = newCtx
			before := append([]messages.MessageItem(nil), a.threadPanel.Replies()...)

			_, sendCmd := a.Update(cmd())
			if sendCmd != nil {
				_ = sendCmd()
			}
			if got := a.threadPanel.Replies(); !reflect.DeepEqual(got, before) {
				t.Fatalf("delayed intent changed new panel from %#v to %#v", before, got)
			}
			if len(service.requests) != 0 || len(service.contexts) != 0 {
				t.Fatalf("delayed intent sent requests=%#v contexts=%#v", service.requests, service.contexts)
			}
			if oldCtx == newCtx {
				t.Fatal("test did not install a replacement context")
			}
		})
	}
}

func TestMattermostCanceledThreadReplyIntentDoesNotSend(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	cmd, _, ctx := createMattermostThreadReplyIntent(t, a, "canceled")
	scope := a.mattermostScope(a.activeHistoryRequest)
	if scope == nil {
		t.Fatal("active Mattermost scope missing")
	}
	scope.cancel()
	if ctx.Err() != context.Canceled {
		t.Fatalf("origin context error=%v want canceled", ctx.Err())
	}

	_, sendCmd := a.Update(cmd())
	if sendCmd != nil || len(a.threadPanel.Replies()) != 0 || len(service.requests) != 0 {
		t.Fatalf("canceled intent sendCmd=%v replies=%#v requests=%#v", sendCmd != nil, a.threadPanel.Replies(), service.requests)
	}
}

func TestMattermostThreadReplyOptimisticAndExactRequest(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	active := a.activeHistoryRequest

	_, cmd := a.Update(mattermostThreadReplyMsg(a, "**exact Mattermost**"))
	if cmd == nil {
		t.Fatal("send returned nil command")
	}
	replies := a.threadPanel.Replies()
	if len(replies) != 1 {
		t.Fatalf("replies=%#v want optimistic reply", replies)
	}
	row := replies[0]
	if row.ID == "" || row.ID != row.CorrelationID || row.TS != "" || row.RootID != "root-1" || row.Text != "**exact Mattermost**" || row.Format != messages.FormatMattermostPlain || row.DeliveryState != messages.DeliveryPending {
		t.Fatalf("optimistic reply=%#v", row)
	}
	if row.DeliveryServerID != string(active.ServerID) || row.DeliveryChannelID != active.ChannelID || row.DeliveryGeneration != active.Generation {
		t.Fatalf("optimistic scope=%#v", row)
	}

	_ = cmd()
	want := MattermostSendRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation, RootID: "root-1", Text: "**exact Mattermost**", CorrelationID: row.CorrelationID}
	if !reflect.DeepEqual(service.requests, []MattermostSendRequest{want}) {
		t.Fatalf("requests=%#v want %#v", service.requests, []MattermostSendRequest{want})
	}
}

func TestMattermostThreadReplySuccessReplacesOptimisticAndIncrementsRoot(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	a.messagepane.SetMessages([]messages.MessageItem{{ID: "root-1", Format: messages.FormatMattermostPlain}})
	active := a.activeHistoryRequest
	_, cmd := a.Update(mattermostThreadReplyMsg(a, "reply"))
	correlationID := a.threadPanel.Replies()[0].CorrelationID
	service.result = MattermostMessageSentMsg{
		Request: MattermostSendRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation, RootID: "root-1", Text: "reply", CorrelationID: correlationID},
		Message: messages.MessageItem{ID: "reply-1", RootID: "root-1", CorrelationID: correlationID, Format: messages.FormatMattermostPlain, Text: "authoritative"},
	}

	_, _ = a.Update(cmd())
	replies := a.threadPanel.Replies()
	if len(replies) != 1 || replies[0].ID != "reply-1" || replies[0].Text != "authoritative" {
		t.Fatalf("replies=%#v want authoritative replacement", replies)
	}
	if got := a.messagepane.Messages()[0].ReplyCount; got != 1 {
		t.Fatalf("root reply count=%d want 1", got)
	}
}

func TestMattermostThreadReplyFailureRemovesMatchingOptimisticAndReportsError(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	active := a.activeHistoryRequest
	a.threadPanel.AddReply(messages.MessageItem{ID: "existing", RootID: "root-1", Text: "keep"})
	_, cmd := a.Update(mattermostThreadReplyMsg(a, "fail"))
	correlationID := a.threadPanel.Replies()[1].CorrelationID
	service.result = MattermostMessageSendFailedMsg{Request: MattermostSendRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation, RootID: "root-1", Text: "fail", CorrelationID: correlationID}}

	_, toast := a.Update(cmd())
	replies := a.threadPanel.Replies()
	if len(replies) != 1 || replies[0].ID != "existing" {
		t.Fatalf("replies=%#v want only existing reply", replies)
	}
	if toast == nil {
		t.Fatal("failure returned nil toast")
	}
	if _, ok := toast().(statusbar.SendFailedMsg); !ok {
		t.Fatalf("toast=%T want statusbar.SendFailedMsg", toast())
	}
}

func TestMattermostThreadReplyIgnoresStaleScope(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	active := a.activeHistoryRequest
	_, _ = a.Update(mattermostThreadReplyMsg(a, "pending"))
	before := a.threadPanel.Replies()
	stale := MattermostSendRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation + 1, RootID: "root-1", CorrelationID: before[0].CorrelationID}

	_, _ = a.Update(MattermostMessageSentMsg{Request: stale, Message: messages.MessageItem{ID: "stale", RootID: "root-1"}})
	if got := a.threadPanel.Replies(); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale success changed replies from %#v to %#v", before, got)
	}
}

func TestMattermostThreadFetchIgnoresStaleScope(t *testing.T) {
	a := openMattermostThreadForSend(t, &recordingMattermostSendService{})
	before := a.threadPanel.Replies()
	active := a.activeHistoryRequest
	_, _ = a.Update(ThreadRepliesLoadedMsg{Request: HistoryRequest{ServerID: active.ServerID, ChannelID: active.ChannelID, Generation: active.Generation + 1}, ThreadTS: "root-1", Replies: []messages.MessageItem{{ID: "stale", RootID: "root-1"}}})
	if got := a.threadPanel.Replies(); !reflect.DeepEqual(got, before) {
		t.Fatalf("stale fetch changed replies from %#v to %#v", before, got)
	}
}

func TestMattermostThreadResultIgnoresRetainedUnfocusedScope(t *testing.T) {
	a := openMattermostThreadForSend(t, &recordingMattermostSendService{})
	old := a.activeHistoryRequest
	a.activeHistoryRequest.Generation++
	before := a.threadPanel.Replies()

	_, _ = a.Update(ThreadRepliesLoadedMsg{Request: old, ThreadTS: "root-1", Replies: []messages.MessageItem{{ID: "stale", RootID: "root-1"}}})
	if got := a.threadPanel.Replies(); !reflect.DeepEqual(got, before) {
		t.Fatalf("retained unfocused result changed replies from %#v to %#v", before, got)
	}
}
