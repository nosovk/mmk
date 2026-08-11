package mattermost

import (
	"reflect"
	"strings"
	"testing"
)

func TestWebSocketPostedEventDecodesNestedPost(t *testing.T) {
	event, err := decodeWebSocketEvent([]byte(`{
		"event":"posted",
		"data":{"post":"{\"id\":\"post-1\",\"channel_id\":\"channel-1\",\"user_id\":\"user-1\",\"root_id\":\"root-1\",\"message\":\"hello\",\"pending_post_id\":\"correlation-1\",\"create_at\":10,\"update_at\":11,\"edit_at\":12,\"delete_at\":13,\"reply_count\":4}"},
		"broadcast":{"channel_id":"channel-1"},
		"seq":7
	}`))
	if err != nil {
		t.Fatal(err)
	}

	want := PostedEvent{Message: Message{
		ID: "post-1", ChannelID: "channel-1", UserID: "user-1", RootID: "root-1",
		Text: "hello", CorrelationID: "correlation-1", CreatedAt: 10, UpdatedAt: 11,
		EditedAt: 12, DeletedAt: 13, ReplyCount: 4,
	}}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
}

func TestWebSocketUnknownEventIsIgnored(t *testing.T) {
	event, err := decodeWebSocketEvent([]byte(`{"event":"future_event","data":{"token":"must stay private"}}`))
	if err != nil {
		t.Fatal(err)
	}
	if event != nil {
		t.Fatalf("event = %#v, want nil", event)
	}
}

func TestWebSocketMalformedPostedEventReturnsUsefulError(t *testing.T) {
	_, err := decodeWebSocketEvent([]byte(`{"event":"posted","data":{"post":"not-json"}}`))
	if err == nil || !strings.Contains(err.Error(), "posted") {
		t.Fatalf("error = %v, want posted decoding error", err)
	}
}

func TestWebSocketPostedEventRequiresPostIdentity(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "empty post", message: `{"event":"posted","data":{"post":"{}"}}`, want: "ID"},
		{name: "missing ID", message: `{"event":"posted","data":{"post":"{\"channel_id\":\"channel-1\"}"}}`, want: "ID"},
		{name: "missing channel ID", message: `{"event":"posted","data":{"post":"{\"id\":\"post-1\"}"}}`, want: "channel ID"},
		{name: "absent post", message: `{"event":"posted","data":{}}`, want: "post"},
		{name: "wrong post type", message: `{"event":"posted","data":{"post":{"id":"sensitive-post"}}}`, want: "posted event data"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeWebSocketEvent([]byte(tt.message))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contextual error containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "sensitive-post") || strings.Contains(err.Error(), "test-token") {
				t.Fatalf("error leaked sensitive payload: %v", err)
			}
		})
	}
}
