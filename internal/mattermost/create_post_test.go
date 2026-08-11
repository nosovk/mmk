package mattermost

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
)

func TestClient_CreatePostSendsPayloadAndReturnsAuthoritativeMessage(t *testing.T) {
	channelID := strings.Repeat("channel/id:", 13)
	postID := strings.Repeat("post/id:", 17)
	const correlationID = "user-id:1723377600123"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodPost; got != want {
			t.Errorf("method = %q, want %q", got, want)
		}
		if got, want := r.URL.Path, "/api/v4/posts"; got != want {
			t.Errorf("path = %q, want %q", got, want)
		}
		if got, want := r.Header.Get("Content-Type"), "application/json"; got != want {
			t.Errorf("Content-Type = %q, want %q", got, want)
		}

		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		wantPayload := map[string]any{
			"channel_id":      channelID,
			"message":         " hello ",
			"pending_post_id": correlationID,
		}
		if !reflect.DeepEqual(payload, wantPayload) {
			t.Fatalf("payload = %#v, want %#v", payload, wantPayload)
		}

		w.WriteHeader(http.StatusCreated)
		_, _ = fmt.Fprintf(w, `{
			"id":%q,
			"channel_id":%q,
			"user_id":"user-1",
			"root_id":"root-1",
			"message":"server-authoritative text",
			"create_at":10,
			"update_at":11,
			"edit_at":12,
			"delete_at":13,
			"reply_count":4,
			"pending_post_id":%q
		}`, postID, channelID, correlationID)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	message, err := client.CreatePost(context.Background(), CreatePostRequest{
		ChannelID:     channelID,
		Message:       " hello ",
		CorrelationID: correlationID,
	})
	if err != nil {
		t.Fatalf("CreatePost returned error: %v", err)
	}
	want := Message{
		ID: postID, ChannelID: channelID, UserID: "user-1", RootID: "root-1",
		Text: "server-authoritative text", CreatedAt: 10, UpdatedAt: 11,
		EditedAt: 12, DeletedAt: 13, ReplyCount: 4, CorrelationID: correlationID,
	}
	if message != want {
		t.Fatalf("message = %#v, want %#v", message, want)
	}
}

func TestClient_CreatePostValidatesInputWithoutRequest(t *testing.T) {
	tests := []struct {
		name    string
		request CreatePostRequest
	}{
		{name: "blank channel", request: CreatePostRequest{ChannelID: " ", Message: "hello", CorrelationID: "correlation-1"}},
		{name: "blank message", request: CreatePostRequest{ChannelID: "channel-1", Message: " \t\n ", CorrelationID: "correlation-1"}},
		{name: "blank correlation", request: CreatePostRequest{ChannelID: "channel-1", Message: "hello", CorrelationID: " "}},
		{name: "oversized correlation", request: CreatePostRequest{ChannelID: "channel-1", Message: "hello", CorrelationID: strings.Repeat("p", 257)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := newCountingMattermostClient(t, &requests)

			if _, err := client.CreatePost(context.Background(), tt.request); err == nil {
				t.Fatal("CreatePost accepted invalid input")
			}
			if got := requests.Load(); got != 0 {
				t.Fatalf("requests = %d, want 0", got)
			}
		})
	}
}

func TestClient_CreatePostRejectsInvalidAuthoritativeIdentity(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "empty post ID", body: `{"channel_id":"channel-1"}`},
		{name: "empty channel ID", body: `{"id":"post-1"}`},
		{name: "mismatched channel ID", body: `{"id":"post-1","channel_id":"channel-2"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newJSONMattermostClient(t, tt.body)
			_, err := client.CreatePost(context.Background(), CreatePostRequest{
				ChannelID:     "channel-1",
				Message:       "hello",
				CorrelationID: "correlation-1",
			})
			if err == nil {
				t.Fatal("CreatePost accepted invalid authoritative identity")
			}
		})
	}
}

func TestClient_CreatePostNormalizesAndValidatesAuthoritativePendingPostID(t *testing.T) {
	const submitted = "mmk-submitted-correlation"
	for _, tc := range []struct {
		name              string
		responsePendingID string
		wantCorrelation   string
		wantErr           bool
	}{
		{name: "omitted", wantCorrelation: submitted},
		{name: "matching", responsePendingID: submitted, wantCorrelation: submitted},
		{name: "mismatched", responsePendingID: "mmk-unrelated-correlation", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := fmt.Sprintf(`{"id":"post-1","channel_id":"channel-1","pending_post_id":%q}`, tc.responsePendingID)
			client := newJSONMattermostClient(t, body)
			message, err := client.CreatePost(context.Background(), CreatePostRequest{ChannelID: "channel-1", Message: "hello", CorrelationID: submitted})
			if tc.wantErr {
				if err == nil {
					t.Fatal("CreatePost accepted mismatched pending_post_id")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if message.CorrelationID != tc.wantCorrelation {
				t.Fatalf("CorrelationID=%q want %q", message.CorrelationID, tc.wantCorrelation)
			}
		})
	}
}

func TestClient_CreatePostMismatchedPendingPostIDRedactsToken(t *testing.T) {
	const token = "pat-super-secret"
	httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusCreated,
			Header:     make(http.Header),
			Body: io.NopCloser(strings.NewReader(
				`{"id":"post-1","channel_id":"channel-1","pending_post_id":"pat-super-secret"}`,
			)),
		}, nil
	})}
	client, err := NewClient("https://chat.example.com", token, WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreatePost(context.Background(), CreatePostRequest{ChannelID: "channel-1", Message: "hello", CorrelationID: "mmk-expected"})
	if err == nil {
		t.Fatal("CreatePost accepted mismatched pending_post_id")
	}
	assertErrorChainDoesNotContain(t, err, token)
	assertStringsDoNotContain(t, token, fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))
}

func TestClient_CreatePostPreservesContextCancellation(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = client.CreatePost(ctx, CreatePostRequest{
		ChannelID:     "channel-1",
		Message:       "hello",
		CorrelationID: "correlation-1",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error = %v, want context.Canceled", err)
	}
}

func TestClient_CreatePostDoesNotExposeTokenInErrors(t *testing.T) {
	const token = "highly-secret-token"
	tests := []struct {
		name       string
		httpClient *http.Client
	}{
		{
			name: "transport error",
			httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, fmt.Errorf("transport leaked %s", token)
			})},
		},
		{
			name: "API error",
			httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusBadRequest,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"message":"rejected highly-secret-token"}`)),
				}, nil
			})},
		},
		{
			name: "response validation",
			httpClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"id":"post-1","channel_id":"highly-secret-token"}`)),
				}, nil
			})},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, err := NewClient("https://chat.example.com", token, WithHTTPClient(tt.httpClient))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.CreatePost(context.Background(), CreatePostRequest{
				ChannelID:     "channel-1",
				Message:       "hello",
				CorrelationID: "correlation-1",
			})
			if err == nil {
				t.Fatal("CreatePost returned nil error")
			}
			assertErrorChainDoesNotContain(t, err, token)
			assertStringsDoNotContain(t, token, fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))
		})
	}
}
