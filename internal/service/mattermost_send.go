package service

import (
	"context"
	"errors"
	"fmt"

	"github.com/nosovk/mmk/internal/mattermost"
)

type mattermostSendClient interface {
	CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error)
}

type MattermostSendService struct {
	client mattermostSendClient
}

func NewMattermostSendService(client mattermostSendClient) *MattermostSendService {
	return &MattermostSendService{client: client}
}

func (s *MattermostSendService) Send(ctx context.Context, channelID, text, correlationID string) (mattermost.Message, error) {
	return s.send(ctx, channelID, "", text, correlationID)
}

func (s *MattermostSendService) Reply(ctx context.Context, channelID, rootID, text, correlationID string) (mattermost.Message, error) {
	return s.send(ctx, channelID, rootID, text, correlationID)
}

func (s *MattermostSendService) send(ctx context.Context, channelID, rootID, text, correlationID string) (mattermost.Message, error) {
	if s == nil || isNilInterface(s.client) {
		return mattermost.Message{}, errors.New("send Mattermost message: client unavailable")
	}
	message, err := s.client.CreatePost(ctx, mattermost.CreatePostRequest{
		ChannelID:     channelID,
		RootID:        rootID,
		Message:       text,
		CorrelationID: correlationID,
	})
	if err != nil {
		return mattermost.Message{}, fmt.Errorf("send Mattermost message: %w", err)
	}
	return message, nil
}
