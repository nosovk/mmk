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

func TestClient_ChannelPostsRequestsPageBeforeAndReconstructsOrder(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got, want := r.Method, http.MethodGet; got != want {
			t.Errorf("method=%q want %q", got, want)
		}
		if got, want := r.URL.EscapedPath(), "/api/v4/channels/channel-1/posts"; got != want {
			t.Errorf("path=%q want %q", got, want)
		}
		if got, want := r.URL.Query().Get("page"), "0"; got != want {
			t.Errorf("page=%q want %q", got, want)
		}
		if got, want := r.URL.Query().Get("per_page"), "2"; got != want {
			t.Errorf("per_page=%q want %q", got, want)
		}
		if got, want := r.URL.Query().Get("before"), "before-1"; got != want {
			t.Errorf("before=%q want %q", got, want)
		}
		if _, ok := r.URL.Query()["include_deleted"]; ok {
			t.Error("include_deleted must be absent")
		}
		_, _ = io.WriteString(w, `{"order":["new","old"],"posts":{"old":{"id":"old","channel_id":"channel-1","user_id":"u1","root_id":"root","message":"older","create_at":10,"update_at":11,"edit_at":12,"delete_at":13,"reply_count":4},"new":{"id":"new","channel_id":"channel-1","user_id":"u2","message":"newer","create_at":20,"pending_post_id":"opaque-correlation/id:1"}}}`)
	}))
	defer server.Close()
	client, err := NewClient(server.URL, "secret", WithHTTPClient(server.Client()))
	if err != nil {
		t.Fatal(err)
	}
	page, err := client.ChannelPosts(context.Background(), "channel-1", ChannelPostsOptions{Page: 0, PerPage: 2, Before: "before-1"})
	if err != nil {
		t.Fatal(err)
	}
	want := []Message{
		{ID: "new", ChannelID: "channel-1", UserID: "u2", Text: "newer", CreatedAt: 20, CorrelationID: "opaque-correlation/id:1"},
		{ID: "old", ChannelID: "channel-1", UserID: "u1", RootID: "root", Text: "older", CreatedAt: 10, UpdatedAt: 11, EditedAt: 12, DeletedAt: 13, ReplyCount: 4},
	}
	if !reflect.DeepEqual(page.Messages, want) {
		t.Fatalf("messages=%#v want %#v", page.Messages, want)
	}
}

func TestClient_ChannelPostsValidatesInputWithoutRequest(t *testing.T) {
	tests := []struct {
		name, channel string
		options       ChannelPostsOptions
	}{
		{"blank channel", " ", ChannelPostsOptions{PerPage: 1}},
		{"unsafe channel", "channel/1", ChannelPostsOptions{PerPage: 1}},
		{"long channel", strings.Repeat("c", 129), ChannelPostsOptions{PerPage: 1}},
		{"negative page", "c1", ChannelPostsOptions{Page: -1, PerPage: 1}},
		{"zero per page", "c1", ChannelPostsOptions{}},
		{"large per page", "c1", ChannelPostsOptions{PerPage: 201}},
		{"unsafe before", "c1", ChannelPostsOptions{PerPage: 1, Before: "bad/id"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var requests atomic.Int32
			client := newCountingMattermostClient(t, &requests)
			if _, err := client.ChannelPosts(context.Background(), tt.channel, tt.options); err == nil {
				t.Fatal("accepted invalid input")
			}
			if requests.Load() != 0 {
				t.Fatalf("requests=%d want 0", requests.Load())
			}
		})
	}
}

func TestClient_ChannelPostsHandlesOrderAnomalies(t *testing.T) {
	tests := []struct {
		name, body string
		wantIDs    []string
		wantErr    string
	}{
		{"duplicate first wins", `{"order":["p1","p1"],"posts":{"p1":{"id":"p1","channel_id":"c1","create_at":1}}}`, []string{"p1"}, ""},
		{"unexpected map ignored", `{"order":["p1"],"posts":{"p1":{"id":"p1","channel_id":"c1","create_at":1},"extra":{"id":"extra","channel_id":"c1"}}}`, []string{"p1"}, ""},
		{"empty id filled from key", `{"order":["p1"],"posts":{"p1":{"channel_id":"c1","create_at":1}}}`, []string{"p1"}, ""},
		{"missing ordered post", `{"order":["missing"],"posts":{}}`, nil, "missing"},
		{"mismatched id", `{"order":["p1"],"posts":{"p1":{"id":"other","channel_id":"c1"}}}`, nil, "mismatched"},
		{"mismatched channel", `{"order":["p1"],"posts":{"p1":{"id":"p1","channel_id":"c2"}}}`, nil, "channel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := newJSONMattermostClient(t, tt.body)
			page, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 20})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(strings.ToLower(err.Error()), tt.wantErr) {
					t.Fatalf("error=%v want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			got := make([]string, len(page.Messages))
			for i := range page.Messages {
				got[i] = page.Messages[i].ID
			}
			if !reflect.DeepEqual(got, tt.wantIDs) {
				t.Fatalf("ids=%v want %v", got, tt.wantIDs)
			}
		})
	}
}

func TestClient_ChannelPostsReturnsValidNonNilEmptyPage(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":[],"posts":{}}`)
	page, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 20})
	if err != nil {
		t.Fatal(err)
	}
	if page.Messages == nil || len(page.Messages) != 0 {
		t.Fatalf("messages=%#v want non-nil empty", page.Messages)
	}
}

func TestClient_ChannelPostsRequiresPositiveCreateAt(t *testing.T) {
	for _, createdAt := range []int64{0, -1} {
		t.Run(fmt.Sprintf("create_at_%d", createdAt), func(t *testing.T) {
			client := newJSONMattermostClient(t, fmt.Sprintf(`{"order":["p1"],"posts":{"p1":{"id":"p1","channel_id":"c1","create_at":%d}}}`, createdAt))
			_, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 20})
			if err == nil || !strings.Contains(err.Error(), "create_at") {
				t.Fatalf("error=%v", err)
			}
		})
	}
}

func TestClient_ChannelPostsReportsRawOrderCountForFullDuplicatePage(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["p1","p1"],"posts":{"p1":{"id":"p1","channel_id":"c1","create_at":1}}}`)
	page, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 2})
	if err != nil {
		t.Fatal(err)
	}
	if page.OrderCount != 2 || len(page.Messages) != 1 {
		t.Fatalf("page=%#v", page)
	}
}

func TestClient_ChannelPostsRejectsRawOrderLargerThanRequestedPage(t *testing.T) {
	client := newJSONMattermostClient(t, `{"order":["p1","p2","p3"],"posts":{"p1":{"id":"p1","channel_id":"c1"},"p2":{"id":"p2","channel_id":"c1"},"p3":{"id":"p3","channel_id":"c1"}}}`)
	_, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 2})
	if err == nil || !strings.Contains(err.Error(), "per_page") {
		t.Fatalf("error=%v", err)
	}
}

func TestClient_ChannelPostsPreservesContextAndBoundsResponse(t *testing.T) {
	t.Run("context", func(t *testing.T) {
		httpClient := &http.Client{Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) { return nil, req.Context().Err() })}
		client, _ := NewClient("https://chat.example.com", "secret", WithHTTPClient(httpClient))
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, err := client.ChannelPosts(ctx, "c1", ChannelPostsOptions{PerPage: 20})
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("error=%v want context.Canceled", err)
		}
	})
	t.Run("oversized", func(t *testing.T) {
		client := newJSONMattermostClient(t, fmt.Sprintf(`{"order":[],"posts":{},"padding":%q}`, strings.Repeat("x", (10<<20)+1)))
		_, err := client.ChannelPosts(context.Background(), "c1", ChannelPostsOptions{PerPage: 20})
		if err == nil || !strings.Contains(err.Error(), "exceeds") {
			t.Fatalf("error=%v want size error", err)
		}
	})
}
