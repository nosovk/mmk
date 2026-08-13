package main

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

type contextHistoryClient struct{ seen context.Context }

func (c *contextHistoryClient) ChannelPosts(ctx context.Context, _ string, _ mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	c.seen = ctx
	<-ctx.Done()
	return mattermost.MessagePage{}, ctx.Err()
}

func (c *contextHistoryClient) RunWebSocket(ctx context.Context, _ func(), _ func(mattermost.Event), _ func(error)) error {
	<-ctx.Done()
	return ctx.Err()
}

func TestMattermostHistoryItemsCarryTransportCorrelationWithoutPersistence(t *testing.T) {
	source := []service.MattermostHistoryMessage{{Message: mattermost.Message{ID: "opaque/post:id", CorrelationID: "opaque/correlation:id", Text: "body"}, UserName: "alice"}}
	want := []messages.MessageItem{{ID: "opaque/post:id", CorrelationID: "opaque/correlation:id", Format: messages.FormatMattermostPlain, UserName: "alice", Text: "body"}}
	if got := mattermostHistoryItems(source); !reflect.DeepEqual(got, want) {
		t.Fatalf("items=%#v want %#v", got, want)
	}
}
func (c *contextHistoryClient) CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error) {
	return mattermost.Message{}, errors.New("unused")
}
func (c *contextHistoryClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}
func (c *contextHistoryClient) CurrentUser(context.Context) (*mattermost.User, error) {
	return nil, errors.New("unused")
}
func (c *contextHistoryClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return nil, nil
}
func (c *contextHistoryClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return nil, nil
}
func (c *contextHistoryClient) ChannelMembershipsForUser(context.Context, string, string) ([]mattermost.ChannelMembership, error) {
	return nil, nil
}
func (c *contextHistoryClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}

func TestMattermostUIHistoryUsesSuppliedGenerationContext(t *testing.T) {
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	client := &contextHistoryClient{}
	startup := &mattermostStartup{contexts: map[ids.ServerID]mattermostServerContext{"s1": {client: client}}}
	runCtx, stop := context.WithCancel(context.Background())
	defer stop()
	svc := mattermostUIHistoryService{ctx: runCtx, startup: startup, cache: db}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	msg := svc.FetchRecent(ctx, ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 1})
	if !errors.Is(msg.Err, context.Canceled) {
		t.Fatalf("err=%v", msg.Err)
	}
	if client.seen == nil || !errors.Is(client.seen.Err(), context.Canceled) {
		t.Fatal("client context did not inherit supplied generation cancellation")
	}
}
