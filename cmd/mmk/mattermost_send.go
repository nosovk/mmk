package main

import (
	"context"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

const mattermostSendFailureReason = "Message could not be sent"

type mattermostUISendService struct {
	ctx     context.Context
	startup *mattermostStartup
}

func (s mattermostUISendService) Send(ctx context.Context, request ui.MattermostSendRequest) tea.Msg {
	if s.ctx == nil || s.startup == nil || s.ctx.Err() != nil {
		return mattermostSendFailed(request)
	}

	s.startup.mu.RLock()
	serverContext, ok := s.startup.contexts[ids.ServerID(request.ServerID)]
	userNames := make(map[string]string, len(serverContext.snapshot.Users))
	for _, user := range serverContext.snapshot.Users {
		userNames[user.ID] = user.DisplayName()
	}
	s.startup.mu.RUnlock()
	if !ok || !serverContext.usable || serverContext.client == nil {
		return mattermostSendFailed(request)
	}

	callCtx, cancel := context.WithCancel(ctx)
	stopRunCancel := context.AfterFunc(s.ctx, cancel)
	defer func() {
		stopRunCancel()
		cancel()
	}()

	message, err := service.NewMattermostSendService(serverContext.client).Send(callCtx, request.ChannelID, request.Text, request.CorrelationID)
	if err != nil {
		return mattermostSendFailed(request)
	}
	userName := userNames[message.UserID]
	if userName == "" {
		userName = message.UserID
	}
	return ui.MattermostMessageSentMsg{
		Request: request,
		Message: messages.MessageItem{
			ID:            message.ID,
			CorrelationID: message.CorrelationID,
			CreatedAt:     message.CreatedAt,
			RootID:        message.RootID,
			Format:        messages.FormatMattermostPlain,
			UserID:        message.UserID,
			UserName:      userName,
			Text:          message.Text,
			ReplyCount:    int(message.ReplyCount),
			IsEdited:      message.EditedAt != 0,
		},
	}
}

func mattermostSendFailed(request ui.MattermostSendRequest) ui.MattermostMessageSendFailedMsg {
	return ui.MattermostMessageSendFailedMsg{Request: request, Reason: mattermostSendFailureReason}
}
