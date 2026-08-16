package ui

import (
	"context"
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/statusbar"
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

func openMattermostThreadForSend(t *testing.T, service *recordingMattermostSendService) *App {
	t.Helper()
	a := newMattermostSendApp(t, service)
	a.SetNowTimestampFormatter(func() string { return "9:41 AM" })
	a.threadVisible = true
	a.threadPanel.SetThread(messages.MessageItem{ID: "root-1", Format: messages.FormatMattermostPlain, ReplyCount: 0}, nil, a.activeChannelID, "root-1")
	return a
}

func TestMattermostThreadReplyOptimisticAndExactRequest(t *testing.T) {
	service := &recordingMattermostSendService{}
	a := openMattermostThreadForSend(t, service)
	active := a.activeHistoryRequest

	_, cmd := a.Update(SendThreadReplyMsg{ChannelID: active.ChannelID, ThreadTS: "root-1", Text: "**exact Mattermost**"})
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
	_, cmd := a.Update(SendThreadReplyMsg{ChannelID: active.ChannelID, ThreadTS: "root-1", Text: "reply"})
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
	_, cmd := a.Update(SendThreadReplyMsg{ChannelID: active.ChannelID, ThreadTS: "root-1", Text: "fail"})
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
	_, _ = a.Update(SendThreadReplyMsg{ChannelID: active.ChannelID, ThreadTS: "root-1", Text: "pending"})
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
