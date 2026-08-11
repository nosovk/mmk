package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/mattermost"
)

type fakeMattermostSendClient struct {
	requests []mattermost.CreatePostRequest
	contexts []context.Context
	results  []mattermost.Message
	errors   []error
}

func (f *fakeMattermostSendClient) CreatePost(ctx context.Context, request mattermost.CreatePostRequest) (mattermost.Message, error) {
	f.contexts = append(f.contexts, ctx)
	f.requests = append(f.requests, request)
	index := len(f.requests) - 1
	if index < len(f.errors) && f.errors[index] != nil {
		return mattermost.Message{}, f.errors[index]
	}
	if index < len(f.results) {
		return f.results[index], nil
	}
	return mattermost.Message{}, nil
}

func TestMattermostSendForwardsExactRequestAndReturnsAuthoritativeMessage(t *testing.T) {
	ctx := context.WithValue(context.Background(), mattermostSendContextKey{}, "request-context")
	want := mattermost.Message{
		ID:         "post-authoritative",
		ServerID:   "server-authoritative",
		ChannelID:  "channel-authoritative",
		UserID:     "user-authoritative",
		RootID:     "root-authoritative",
		Text:       "server normalized text",
		CreatedAt:  101,
		UpdatedAt:  102,
		EditedAt:   103,
		DeletedAt:  104,
		ReplyCount: 7,
	}
	client := &fakeMattermostSendClient{results: []mattermost.Message{want}}

	got, err := NewMattermostSendService(client).Send(ctx, "channel-input", " input text ", "correlation-1")
	if err != nil {
		t.Fatalf("Send returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("message=%#v want %#v", got, want)
	}
	if len(client.contexts) != 1 || client.contexts[0] != ctx {
		t.Fatalf("contexts=%#v want original context", client.contexts)
	}
	wantRequest := mattermost.CreatePostRequest{ChannelID: "channel-input", Message: " input text ", CorrelationID: "correlation-1"}
	if !reflect.DeepEqual(client.requests, []mattermost.CreatePostRequest{wantRequest}) {
		t.Fatalf("requests=%#v want %#v", client.requests, []mattermost.CreatePostRequest{wantRequest})
	}
}

func TestMattermostSendPreservesCorrelationAcrossExplicitRetryCalls(t *testing.T) {
	retryable := errors.New("temporary failure")
	want := mattermost.Message{ID: "post-1", ChannelID: "channel-1", Text: "hello"}
	client := &fakeMattermostSendClient{
		errors:  []error{retryable, nil},
		results: []mattermost.Message{{}, want},
	}
	service := NewMattermostSendService(client)

	if _, err := service.Send(context.Background(), "channel-1", "hello", "correlation-stable"); !errors.Is(err, retryable) {
		t.Fatalf("first Send error=%v want wrapped retryable error", err)
	}
	got, err := service.Send(context.Background(), "channel-1", "hello", "correlation-stable")
	if err != nil {
		t.Fatalf("retry Send returned error: %v", err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("retry message=%#v want %#v", got, want)
	}
	wantRequest := mattermost.CreatePostRequest{ChannelID: "channel-1", Message: "hello", CorrelationID: "correlation-stable"}
	if !reflect.DeepEqual(client.requests, []mattermost.CreatePostRequest{wantRequest, wantRequest}) {
		t.Fatalf("requests=%#v want identical explicit retries", client.requests)
	}
}

func TestMattermostSendPropagatesContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	client := mattermostSendClientFunc(func(callCtx context.Context, _ mattermost.CreatePostRequest) (mattermost.Message, error) {
		return mattermost.Message{}, callCtx.Err()
	})

	_, err := NewMattermostSendService(client).Send(ctx, "channel-1", "hello", "correlation-1")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Send error=%v want context.Canceled", err)
	}
	if err == nil || !strings.Contains(err.Error(), "send Mattermost message") {
		t.Fatalf("Send error=%v lacks operation context", err)
	}
}

func TestMattermostSendRejectsUnavailableClient(t *testing.T) {
	var typedNil *fakeMattermostSendClient
	for _, client := range []mattermostSendClient{nil, typedNil} {
		_, err := NewMattermostSendService(client).Send(context.Background(), "channel-secret", "credential-secret", "correlation-secret")
		if err == nil || err.Error() != "send Mattermost message: client unavailable" {
			t.Fatalf("Send error=%v want unavailable client error", err)
		}
		for _, secret := range []string{"channel-secret", "credential-secret", "correlation-secret"} {
			if strings.Contains(err.Error(), secret) {
				t.Fatalf("Send error leaked request data %q: %v", secret, err)
			}
		}
	}
}

type mattermostSendClientFunc func(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error)

func (f mattermostSendClientFunc) CreatePost(ctx context.Context, request mattermost.CreatePostRequest) (mattermost.Message, error) {
	return f(ctx, request)
}

type mattermostSendContextKey struct{}
