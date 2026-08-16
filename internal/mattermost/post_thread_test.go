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
		_, _ = io.WriteString(w, `{"order":["root-1","reply-1","reply-2","reply-1"],"posts":{"reply-2":{"id":"reply-2","channel_id":"channel-1","user_id":"user-3","root_id":"root-1","message":"second reply","create_at":30},"unordered":{"id":"unordered","channel_id":"channel-1","root_id":"root-1","create_at":40},"root-1":{"channel_id":"channel-1","user_id":"user-1","message":"root","create_at":10},"reply-1":{"id":"reply-1","channel_id":"channel-1","user_id":"user-2","root_id":"root-1","message":"first reply","create_at":20}}}`)
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
			{ID: "reply-1", ChannelID: "channel-1", UserID: "user-2", RootID: "root-1", Text: "first reply", CreatedAt: 20},
			{ID: "reply-2", ChannelID: "channel-1", UserID: "user-3", RootID: "root-1", Text: "second reply", CreatedAt: 30},
		},
		OrderCount: 4,
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

func TestClient_PostThreadRejectsBlankChannelID(t *testing.T) {
	tests := []struct {
		name, body string
	}{
		{"root", `{"order":["root-1"],"posts":{"root-1":{"id":"root-1","create_at":1}}}`},
		{"reply", `{"order":["root-1","reply-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","root_id":"root-1","create_at":2}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newJSONMattermostClient(t, tt.body)
			_, err := client.PostThread(context.Background(), "root-1")
			if err == nil || !strings.Contains(err.Error(), "channel_id must not be blank") {
				t.Fatalf("error=%v want blank channel_id error", err)
			}
		})
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

func TestClient_PostThreadResponseValidationRedactsToken(t *testing.T) {
	const token = "pat-super-secret"
	tests := []struct {
		name, body string
	}{
		{"ordered ID", `{"order":["pat-super-secret"],"posts":{}}`},
		{"wire ID", `{"order":["root-1"],"posts":{"root-1":{"id":"pat-super-secret","channel_id":"channel-1","create_at":1}}}`},
		{"root ID", `{"order":["root-1","reply-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","channel_id":"channel-1","root_id":"pat-super-secret","create_at":2}}}`},
		{"channel ID", `{"order":["root-1","reply-1"],"posts":{"root-1":{"id":"root-1","channel_id":"channel-1","create_at":1},"reply-1":{"id":"reply-1","channel_id":"pat-super-secret","root_id":"root-1","create_at":2}}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			httpClient := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(tt.body)),
				}, nil
			})}
			client, err := NewClient("https://chat.example.com", token, WithHTTPClient(httpClient))
			if err != nil {
				t.Fatal(err)
			}
			_, err = client.PostThread(context.Background(), "root-1")
			if err == nil {
				t.Fatal("PostThread accepted invalid response")
			}
			assertErrorChainDoesNotContain(t, err, token)
			assertStringsDoNotContain(t, token, fmt.Sprintf("%v", err), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", err))
		})
	}
}
