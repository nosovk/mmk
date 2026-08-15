package mattermost

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Event is a typed Mattermost realtime application event.
type Event interface {
	isMattermostEvent()
}

// PostedEvent reports a newly posted Mattermost message.
type PostedEvent struct {
	Message Message
}

func (PostedEvent) isMattermostEvent() {}

// ChannelViewUpdate reports a viewed channel and whether Mattermost supplied
// an authoritative view timestamp.
type ChannelViewUpdate struct {
	ChannelID   string
	ViewedAt    int64
	HasViewedAt bool
}

// ChannelViewedEvent reports authoritative viewed-channel updates for a user.
type ChannelViewedEvent struct {
	UserID  string
	Updates []ChannelViewUpdate
}

func (ChannelViewedEvent) isMattermostEvent() {}

type webSocketEventEnvelope struct {
	Event     string                  `json:"event"`
	Data      json.RawMessage         `json:"data"`
	Broadcast webSocketEventBroadcast `json:"broadcast"`
}

type webSocketEventBroadcast struct {
	UserID string `json:"user_id"`
}

type webSocketPostedData struct {
	Post string `json:"post"`
}

type webSocketMultipleChannelsViewedData struct {
	ChannelTimes map[string]*int64 `json:"channel_times"`
}

type webSocketChannelViewedData struct {
	ChannelID string `json:"channel_id"`
}

func decodeWebSocketEvent(message []byte) (Event, error) {
	var envelope webSocketEventEnvelope
	if err := json.Unmarshal(message, &envelope); err != nil {
		return nil, fmt.Errorf("decode Mattermost WebSocket event: %w", err)
	}
	switch envelope.Event {
	case "posted":
		return decodeWebSocketPostedEvent(envelope.Data)
	case "multiple_channels_viewed":
		return decodeWebSocketMultipleChannelsViewedEvent(envelope.Data, envelope.Broadcast.UserID)
	case "channel_viewed":
		return decodeWebSocketChannelViewedEvent(envelope.Data, envelope.Broadcast.UserID)
	default:
		return nil, nil
	}
}

func decodeWebSocketPostedEvent(raw json.RawMessage) (Event, error) {
	var data webSocketPostedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode Mattermost posted event data: %w", err)
	}
	if data.Post == "" {
		return nil, fmt.Errorf("decode Mattermost posted event: post must not be empty")
	}
	var post postResponse
	if err := json.Unmarshal([]byte(data.Post), &post); err != nil {
		return nil, fmt.Errorf("decode Mattermost posted event post: %w", err)
	}
	if strings.TrimSpace(post.ID) == "" {
		return nil, fmt.Errorf("decode Mattermost posted event: post ID must not be blank")
	}
	if strings.TrimSpace(post.ChannelID) == "" {
		return nil, fmt.Errorf("decode Mattermost posted event: post channel ID must not be blank")
	}
	if post.CreatedAt <= 0 {
		return nil, fmt.Errorf("decode Mattermost posted event: create_at must be positive")
	}
	return PostedEvent{Message: post.domain()}, nil
}

func decodeWebSocketMultipleChannelsViewedEvent(raw json.RawMessage, userID string) (Event, error) {
	if err := validateBulkID(userID); err != nil {
		return nil, fmt.Errorf("decode Mattermost multiple channels viewed event user ID: %w", err)
	}
	var data webSocketMultipleChannelsViewedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode Mattermost multiple channels viewed event data: %w", err)
	}
	if len(data.ChannelTimes) == 0 {
		return nil, fmt.Errorf("decode Mattermost multiple channels viewed event: channel update set must not be empty")
	}

	channelIDs := make([]string, 0, len(data.ChannelTimes))
	for channelID := range data.ChannelTimes {
		channelIDs = append(channelIDs, channelID)
	}
	sort.Strings(channelIDs)

	updates := make([]ChannelViewUpdate, 0, len(channelIDs))
	for _, channelID := range channelIDs {
		if err := validateBulkID(channelID); err != nil {
			return nil, fmt.Errorf("decode Mattermost multiple channels viewed event channel ID: %w", err)
		}
		timestamp := data.ChannelTimes[channelID]
		if timestamp == nil {
			return nil, fmt.Errorf("decode Mattermost multiple channels viewed event: null timestamp")
		}
		viewedAt := *timestamp
		if viewedAt < 0 {
			return nil, fmt.Errorf("decode Mattermost multiple channels viewed event: timestamp must not be negative")
		}
		updates = append(updates, ChannelViewUpdate{
			ChannelID:   channelID,
			ViewedAt:    viewedAt,
			HasViewedAt: true,
		})
	}
	return ChannelViewedEvent{UserID: userID, Updates: updates}, nil
}

func decodeWebSocketChannelViewedEvent(raw json.RawMessage, userID string) (Event, error) {
	if err := validateBulkID(userID); err != nil {
		return nil, fmt.Errorf("decode Mattermost channel viewed event user ID: %w", err)
	}
	var data webSocketChannelViewedData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode Mattermost channel viewed event data: %w", err)
	}
	if err := validateBulkID(data.ChannelID); err != nil {
		return nil, fmt.Errorf("decode Mattermost channel viewed event channel ID: %w", err)
	}
	return ChannelViewedEvent{
		UserID:  userID,
		Updates: []ChannelViewUpdate{{ChannelID: data.ChannelID}},
	}, nil
}
