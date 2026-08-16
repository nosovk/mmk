package mattermost

import (
	"context"
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

func TestClient_PostThreadRequestsEndpointAndReconstructsOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Errorf("method=%q want %q", got, want)
		}
		if got, want := r.URL.EscapedPath(), "/api/v4/posts/root-1/thread"; got != want {
			t.Errorf("path=%q want %q", got, want)
		}
		_, _ = io.WriteString(w, `{"order":["root-1","reply-1","reply-1"],"posts":{"root-1":{"channel_id":"channel-1","user_id":"user-1","message":"root","create_at":10},"reply-1":{"id":"reply-1","channel_id":"channel-1","user_id":"user-2","root_id":"root-1","message":"reply","create_at":20},"unordered":{"id":"unordered","channel_id":"channel-1","root_id":"root-1","create_at":30}}}`)
	}))
	defer server.Close()

	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.PostThread(context.Background(), "root-1")
	if err != nil {
		t.Fatal(err)
	}
	want := MessagePage{
		Messages: []Message{
			{ID: "root-1", ChannelID: "channel-1", UserID: "user-1", Text: "root", CreatedAt: 10},
			{ID: "reply-1", ChannelID: "channel-1", UserID: "user-2", RootID: "root-1", Text: "reply", CreatedAt: 20},
		},
		OrderCount: 3,
	}
	if !reflect.DeepEqual(page, want) {
		t.Fatalf("page=%#v want %#v", page, want)
	}
}

func TestClient_PostThreadValidatesRootIDWithoutRequest(t *testing.T) {
	for _, rootPostID := range []string{"", "root/1", strings.Repeat("r", 129)} {
		t.Run(rootPostID, func(t *testing.T) {
			var requests atomic.Int32
			client := newCountingMattermostClient(t, &requests)
			if _, err := client.PostThread(context.Background(), rootPostID); err == nil {
				t.Fatal("accepted invalid root post ID")
			}
			if requests.Load() != 0 {
				t.Fatalf("requests=%d want 0", requests.Load())
			}
		})
	}
}

func TestClient_PostThreadRejectsMissingOrderedPost(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["root-1"],"posts":{}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("error=%v want missing post error", err)
	}
}

func TestClient_PostThreadRejectsInvalidOrderedID(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["bad/id"],"posts":{"bad/id":{"channel_id":"channel-1","create_at":1}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "ordered post ID") {
		t.Fatalf("error=%v want ordered post ID error", err)
	}
}

func TestClient_PostThreadRequiresRequestedRoot(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["reply-1"],"posts":{"reply-1":{"id":"reply-1","channel_id":"channel-1","root_id":"root-1","create_at":1}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "root") {
		t.Fatalf("error=%v want missing root error", err)
	}
}

func TestClient_PostThreadRejectsMismatchedWireID(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["root-1"],"posts":{"root-1":{"id":"other","channel_id":"channel-1","create_at":1}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "mismatched") {
		t.Fatalf("error=%v want mismatched ID error", err)
	}
}

func TestClient_PostThreadRejectsCrossChannelPost(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["root-1","reply-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","channel_id":"channel-2","root_id":"root-1","create_at":2}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("error=%v want channel error", err)
	}
}

func TestClient_PostThreadRejectsCrossChannelPostBeforeRoot(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["reply-1","root-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","channel_id":"channel-2","root_id":"root-1","create_at":2}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("error=%v want channel error", err)
	}
}

func TestClient_PostThreadRequiresRootPost(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["root-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","root_id":"other-root","create_at":1}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "root_id") {
		t.Fatalf("error=%v want root_id error", err)
	}
}

func TestClient_PostThreadRejectsMismatchedReplyRoot(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["root-1","reply-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","channel_id":"channel-1","root_id":"other-root","create_at":2}}}`)
	_, err := client.PostThread(context.Background(), "root-1")
	if err == nil || !strings.Contains(err.Error(), "root_id") {
		t.Fatalf("error=%v want root_id error", err)
	}
}

func TestClient_PostThreadRequiresPositiveCreateAt(t *testing.T) {
	for _, createdAt := range []int64{0, -1} {
		t.Run(fmt.Sprint(createdAt), func(t *testing.T) {
			client := newJSONMattermostClient(t, `{"order":["root-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":`+fmt.Sprint(createdAt)+`}}}`)
			_, err := client.PostThread(context.Background(), "root-1")
			if err == nil || !strings.Contains(err.Error(), "create_at") {
				t.Fatalf("error=%v want create_at error", err)
			}
		})
	}
}

func TestClient_PostThreadPreservesContextCancellation(t *testing.T) {
	httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	client, err := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = client.PostThread(ctx, "root-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("error=%v want context.Canceled", err)
	}
}
