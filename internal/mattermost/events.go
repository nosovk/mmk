package mattermost

import (
	"encoding/json"
	"fmt"
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

type webSocketEventEnvelope struct {
	Event string          `json:"event"`
	Data  json.RawMessage `json:"data"`
}

type webSocketPostedData struct {
	Post string `json:"post"`
}

func decodeWebSocketEvent(message []byte) (Event, error) {
	var envelope webSocketEventEnvelope
	if err := json.Unmarshal(message, &envelope); err != nil {
		return nil, fmt.Errorf("decode Mattermost WebSocket event: %w", err)
	}
	if envelope.Event != "posted" {
		return nil, nil
	}

	var data webSocketPostedData
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
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
	return PostedEvent{Message: post.domain()}, nil
}
