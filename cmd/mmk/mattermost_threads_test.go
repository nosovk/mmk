package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

type stubMattermostThreadService struct {
	cached []service.MattermostHistoryMessage
	live   []service.MattermostHistoryMessage
	err    error
}

func (s stubMattermostThreadService) ReadCached(string, string) ([]service.MattermostHistoryMessage, error) {
	return s.cached, s.err
}

func (s stubMattermostThreadService) Fetch(context.Context, string, string) ([]service.MattermostHistoryMessage, error) {
	return s.live, s.err
}

func TestMattermostUIThreadsCacheReadConvertsRootAndReplies(t *testing.T) {
	source := []service.MattermostHistoryMessage{
		{Message: mattermost.Message{ID: "root-1", ChannelID: "c1", UserID: "u1", Text: "root", CorrelationID: "root-corr", CreatedAt: 10, EditedAt: 11, ReplyCount: 2}, UserName: "alice"},
		{Message: mattermost.Message{ID: "reply-1", ChannelID: "c1", UserID: "u2", RootID: "root-1", Text: "reply", CorrelationID: "reply-corr", CreatedAt: 20}, UserName: "bob"},
	}
	adapter := mattermostUIThreadService{service: stubMattermostThreadService{cached: source}}

	got := adapter.CacheRead("c1", "root-1")
	want := []messages.MessageItem{
		{ID: "root-1", CorrelationID: "root-corr", CreatedAt: 10, UserID: "u1", UserName: "alice", Text: "root", ReplyCount: 2, IsEdited: true},
		{ID: "reply-1", CorrelationID: "reply-corr", CreatedAt: 20, RootID: "root-1", UserID: "u2", UserName: "bob", Text: "reply"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("cached items=%#v want %#v", got, want)
	}
}

func TestMattermostUIThreadsFetchPreservesSuccessAndFailureSemantics(t *testing.T) {
	request := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	for _, tc := range []struct {
		name    string
		service stubMattermostThreadService
		want    []messages.MessageItem
	}{
		{name: "reply success", service: stubMattermostThreadService{live: []service.MattermostHistoryMessage{{Message: mattermost.Message{ID: "root-1"}}, {Message: mattermost.Message{ID: "reply-1", RootID: "root-1"}}}}, want: []messages.MessageItem{{ID: "reply-1", RootID: "root-1"}}},
		{name: "root only", service: stubMattermostThreadService{live: []service.MattermostHistoryMessage{{Message: mattermost.Message{ID: "root-1"}}}}, want: []messages.MessageItem{}},
		{name: "deleted root", service: stubMattermostThreadService{live: []service.MattermostHistoryMessage{{Message: mattermost.Message{ID: "reply-1", RootID: "root-1"}}, {Message: mattermost.Message{ID: "reply-2", RootID: "root-1"}}}}, want: []messages.MessageItem{{ID: "reply-1", RootID: "root-1"}, {ID: "reply-2", RootID: "root-1"}}},
		{name: "failure", service: stubMattermostThreadService{err: errors.New("offline")}, want: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			adapter := mattermostUIThreadService{service: tc.service, request: request}
			got := adapter.Fetch(context.Background(), "c1", "root-1")
			if got.Request != request || got.ThreadTS != "root-1" || !reflect.DeepEqual(got.Replies, tc.want) {
				t.Fatalf("message=%#v want request=%#v replies=%#v", got, request, tc.want)
			}
		})
	}
}
