package main

import (
	"context"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

type mattermostThreadReader interface {
	ReadCached(string, string) ([]service.MattermostHistoryMessage, error)
	Fetch(context.Context, string, string) ([]service.MattermostHistoryMessage, error)
}

type mattermostThreadClient interface {
	PostThread(context.Context, string) (mattermost.MessagePage, error)
	UsersByIDs(context.Context, []string) ([]mattermost.User, error)
}

type mattermostUIThreadService struct {
	service mattermostThreadReader
	request ui.HistoryRequest
}

func mattermostUIThreadAdapter(startup *mattermostStartup, db *cache.DB, request ui.HistoryRequest) mattermostUIThreadService {
	startup.mu.RLock()
	serverContext := startup.contexts[request.ServerID]
	startup.mu.RUnlock()
	client, _ := serverContext.client.(mattermostThreadClient)
	return mattermostUIThreadService{service: service.NewMattermostThreadService(string(request.ServerID), client, db), request: request}
}

func (s mattermostUIThreadService) CacheRead(channelID ids.ChannelID, rootID ids.ThreadTS) []messages.MessageItem {
	items, err := s.service.ReadCached(string(channelID), string(rootID))
	if err != nil {
		return nil
	}
	return mattermostHistoryItems(items)
}

func (s mattermostUIThreadService) Fetch(ctx context.Context, channelID ids.ChannelID, rootID ids.ThreadTS) ui.ThreadRepliesLoadedMsg {
	items, err := s.service.Fetch(ctx, string(channelID), string(rootID))
	if err != nil {
		return ui.ThreadRepliesLoadedMsg{Request: s.request, ThreadTS: string(rootID), Replies: nil}
	}
	replies := mattermostHistoryItems(items)
	if len(replies) > 0 {
		replies = replies[1:]
	}
	if replies == nil {
		replies = []messages.MessageItem{}
	}
	return ui.ThreadRepliesLoadedMsg{Request: s.request, ThreadTS: string(rootID), Replies: replies}
}
