package mattermost

import (
	"fmt"
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

func TestWebSocketPostedEventRequiresPositiveCreateAtWithoutPayloadLeak(t *testing.T) {
	for _, createAt := range []int64{0, -1} {
		message := fmt.Sprintf(`{"event":"posted","data":{"post":"{\"id\":\"sensitive-post\",\"channel_id\":\"channel-1\",\"create_at\":%d}"}}`, createAt)
		_, err := decodeWebSocketEvent([]byte(message))
		if err == nil || !strings.Contains(err.Error(), "create_at") {
			t.Fatalf("create_at=%d error=%v", createAt, err)
		}
		if strings.Contains(err.Error(), "sensitive-post") {
			t.Fatalf("error leaked post identity: %v", err)
		}
	}
}

func TestChannelViewedMultipleChannelsDecodesAuthoritativeTimes(t *testing.T) {
	event, err := decodeWebSocketEvent([]byte(`{
		"event":"multiple_channels_viewed",
		"data":{"channel_times":{"channel-2":222,"channel-1":111}},
		"broadcast":{"user_id":"user-1"}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	want := ChannelViewedEvent{
		UserID: "user-1",
		Updates: []ChannelViewUpdate{
			{ChannelID: "channel-1", ViewedAt: 111, HasViewedAt: true},
			{ChannelID: "channel-2", ViewedAt: 222, HasViewedAt: true},
		},
	}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
}

func TestChannelViewedLegacyDecodesWithoutInventingTimestamp(t *testing.T) {
	event, err := decodeWebSocketEvent([]byte(`{
		"event":"channel_viewed",
		"data":{"channel_id":"channel-1"},
		"broadcast":{"user_id":"user-1"}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	want := ChannelViewedEvent{
		UserID:  "user-1",
		Updates: []ChannelViewUpdate{{ChannelID: "channel-1"}},
	}
	if !reflect.DeepEqual(event, want) {
		t.Fatalf("event = %#v, want %#v", event, want)
	}
}

func TestChannelViewedRejectsInvalidPayloadsWithoutLeaks(t *testing.T) {
	tests := []struct {
		name    string
		message string
		want    string
	}{
		{name: "modern missing user", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{"channel-1":1}},"broadcast":{}}`, want: "user ID"},
		{name: "modern invalid user", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{"channel-1":1}},"broadcast":{"user_id":"unsafe/user"}}`, want: "user ID"},
		{name: "modern empty updates", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{}},"broadcast":{"user_id":"user-1"}}`, want: "channel update"},
		{name: "modern invalid channel", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{"sentinel/private":1}},"broadcast":{"user_id":"user-1"}}`, want: "channel ID"},
		{name: "modern negative time", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{"channel-1":-1}},"broadcast":{"user_id":"user-1"}}`, want: "timestamp"},
		{name: "modern null time", message: `{"event":"multiple_channels_viewed","data":{"channel_times":{"channel-1":null}},"broadcast":{"user_id":"user-1"}}`, want: "null timestamp"},
		{name: "modern wrong times type", message: `{"event":"multiple_channels_viewed","data":{"channel_times":"sentinel-private-times"},"broadcast":{"user_id":"user-1"}}`, want: "event data"},
		{name: "legacy missing channel", message: `{"event":"channel_viewed","data":{},"broadcast":{"user_id":"user-1"}}`, want: "channel ID"},
		{name: "legacy invalid channel", message: `{"event":"channel_viewed","data":{"channel_id":"sentinel/private"},"broadcast":{"user_id":"user-1"}}`, want: "channel ID"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := decodeWebSocketEvent([]byte(tt.message))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contextual error containing %q", err, tt.want)
			}
			if strings.Contains(err.Error(), "sentinel") || strings.Contains(err.Error(), "unsafe/user") {
				t.Fatalf("error leaked payload: %v", err)
			}
		})
	}
}

func TestChannelViewedValidationIsDeterministic(t *testing.T) {
	message := []byte(`{
		"event":"multiple_channels_viewed",
		"data":{"channel_times":{"z-invalid/channel":1,"a-negative":-1}},
		"broadcast":{"user_id":"user-1"}
	}`)

	var first string
	for range 100 {
		_, err := decodeWebSocketEvent(message)
		if err == nil {
			t.Fatal("decode accepted competing malformed channel updates")
		}
		if strings.Contains(err.Error(), "z-invalid") || strings.Contains(err.Error(), "a-negative") {
			t.Fatalf("error leaked channel ID: %v", err)
		}
		if first == "" {
			first = err.Error()
			if !strings.Contains(first, "timestamp") {
				t.Fatalf("first error = %q, want sorted first-channel timestamp error", first)
			}
			continue
		}
		if err.Error() != first {
			t.Fatalf("error = %q, want deterministic %q", err, first)
		}
	}
}
