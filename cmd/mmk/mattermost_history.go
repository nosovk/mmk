package main

import (
	"context"
	"errors"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

const mattermostHistoryPageSize = 50

type mattermostUIHistoryService struct {
	ctx     context.Context
	startup *mattermostStartup
	cache   *cache.DB
}

func (s mattermostUIHistoryService) ReadCached(request ui.HistoryRequest, before string) ([]messages.MessageItem, error) {
	history := service.NewMattermostHistoryService(string(request.ServerID), nil, s.cache, mattermostHistoryPageSize)
	page, err := history.ReadCached(request.ChannelID, before)
	if err != nil {
		return nil, err
	}
	return mattermostHistoryItems(page.Messages), nil
}

func (s mattermostUIHistoryService) FetchRecent(ctx context.Context, request ui.HistoryRequest) ui.MattermostMessagesLoadedMsg {
	page, err := s.fetch(ctx, request, "")
	return ui.MattermostMessagesLoadedMsg{Request: request, AuthoritativeIDs: page.AuthoritativeIDs, DeletedIDs: page.DeletedIDs, Messages: mattermostHistoryItems(page.Messages), HasMore: page.HasMore, Err: err}
}

func (s mattermostUIHistoryService) FetchOlder(ctx context.Context, request ui.HistoryRequest, before string) ui.MattermostOlderMessagesLoadedMsg {
	page, err := s.fetch(ctx, request, before)
	return ui.MattermostOlderMessagesLoadedMsg{Request: request, AnchorID: before, AuthoritativeIDs: page.AuthoritativeIDs, DeletedIDs: page.DeletedIDs, Messages: mattermostHistoryItems(page.Messages), HasMore: page.HasMore, Err: err}
}

func (s mattermostUIHistoryService) fetch(ctx context.Context, request ui.HistoryRequest, before string) (service.MattermostHistoryPage, error) {
	if s.ctx.Err() != nil {
		return service.MattermostHistoryPage{}, s.ctx.Err()
	}
	s.startup.mu.RLock()
	serverContext, ok := s.startup.contexts[ids.ServerID(request.ServerID)]
	s.startup.mu.RUnlock()
	if !ok || serverContext.client == nil {
		return service.MattermostHistoryPage{}, errors.New("Mattermost history network unavailable")
	}
	history := service.NewMattermostHistoryService(string(request.ServerID), serverContext.client, s.cache, mattermostHistoryPageSize)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		select {
		case <-s.ctx.Done():
			cancel()
		case <-ctx.Done():
		}
	}()
	if before == "" {
		return history.FetchRecent(ctx, request.ChannelID)
	}
	return history.FetchOlder(ctx, request.ChannelID, before)
}

func mattermostHistoryItems(source []service.MattermostHistoryMessage) []messages.MessageItem {
	out := make([]messages.MessageItem, len(source))
	for i, item := range source {
		out[i] = messages.MessageItem{ID: item.Message.ID, CreatedAt: item.Message.CreatedAt, RootID: item.Message.RootID, Format: messages.FormatMattermostPlain, UserID: item.Message.UserID, UserName: item.UserName, Text: item.Message.Text, ReplyCount: int(item.Message.ReplyCount), IsEdited: item.Message.EditedAt != 0}
	}
	return out
}
