package main

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

func TestMattermostUISendUsesRequestServerAfterServerSwitchAndForwardsCorrelation(t *testing.T) {
	one := &recordingMattermostSendClient{message: mattermost.Message{ID: "post-one", ChannelID: "c1", UserID: "u1", Text: "authoritative"}}
	two := &recordingMattermostSendClient{message: mattermost.Message{ID: "post-two", ChannelID: "c2", UserID: "u2", Text: "wrong server"}}
	startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{
		"s1": {client: one, usable: true, snapshot: service.ServerSnapshot{Users: []mattermost.User{{ID: "u1", Username: "one"}}}},
		"s2": {client: two, usable: true, snapshot: service.ServerSnapshot{Users: []mattermost.User{{ID: "u2", Username: "two"}}}},
	})
	if msg := startup.switchMsg("s2"); msg == nil {
		t.Fatal("second server was not switchable")
	}
	request := ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Generation: 7, Text: "hello", CorrelationID: "corr-1"}

	got := mattermostUISendService{ctx: context.Background(), startup: startup}.Send(context.Background(), request)

	sent, ok := got.(ui.MattermostMessageSentMsg)
	if !ok {
		t.Fatalf("message=%#v", got)
	}
	if !reflect.DeepEqual(sent.Request, request) {
		t.Fatalf("request=%#v want %#v", sent.Request, request)
	}
	if len(one.requests) != 1 || one.requests[0] != (mattermost.CreatePostRequest{ChannelID: "c1", Message: "hello", CorrelationID: "corr-1"}) {
		t.Fatalf("server one requests=%#v", one.requests)
	}
	if len(two.requests) != 0 {
		t.Fatalf("active server received request: %#v", two.requests)
	}
}

func TestMattermostUISendReplyForwardsRootAndCorrelation(t *testing.T) {
	client := &recordingMattermostSendClient{message: mattermost.Message{ID: "reply-1", ChannelID: "c1", RootID: "root-1", CorrelationID: "corr-1"}}
	startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: client, usable: true}})
	request := ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Generation: 7, RootID: "root-1", Text: "exact", CorrelationID: "corr-1"}

	got := (mattermostUISendService{ctx: context.Background(), startup: startup}).Send(context.Background(), request)

	if _, ok := got.(ui.MattermostMessageSentMsg); !ok {
		t.Fatalf("message=%#v", got)
	}
	want := mattermost.CreatePostRequest{ChannelID: "c1", RootID: "root-1", Message: "exact", CorrelationID: "corr-1"}
	if !reflect.DeepEqual(client.requests, []mattermost.CreatePostRequest{want}) {
		t.Fatalf("requests=%#v want %#v", client.requests, []mattermost.CreatePostRequest{want})
	}
}

func TestMattermostUISendCombinesRunAndUISendCancellation(t *testing.T) {
	for _, tc := range []struct {
		name      string
		cancelRun bool
	}{
		{name: "run lifetime", cancelRun: true},
		{name: "UI send"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &recordingMattermostSendClient{started: make(chan struct{})}
			startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: client, usable: true}})
			runCtx, stopRun := context.WithCancel(context.Background())
			defer stopRun()
			uiCtx, stopUI := context.WithCancel(context.Background())
			defer stopUI()
			result := make(chan tea.Msg, 1)
			go func() {
				result <- (mattermostUISendService{ctx: runCtx, startup: startup}).Send(uiCtx, ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Text: "secret text", CorrelationID: "secret-correlation"})
			}()
			<-client.started
			if tc.cancelRun {
				stopRun()
			} else {
				stopUI()
			}
			select {
			case msg := <-result:
				failed, ok := msg.(ui.MattermostMessageSendFailedMsg)
				if !ok || failed.Reason != mattermostSendFailureReason {
					t.Fatalf("message=%#v", msg)
				}
			case <-time.After(time.Second):
				t.Fatal("REST call did not observe cancellation")
			}
		})
	}
}

func TestMattermostUISendConvertsAuthoritativeMessageAndCapturedUser(t *testing.T) {
	message := mattermost.Message{ID: "opaque-post-id", ChannelID: "c1", UserID: "u1", RootID: "root-1", Text: "server text", CreatedAt: 1234, EditedAt: 1250, ReplyCount: 9, CorrelationID: "opaque-correlation/id:1"}
	client := &recordingMattermostSendClient{message: message}
	startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{
		"s1": {client: client, usable: true, snapshot: service.ServerSnapshot{Users: []mattermost.User{{ID: "u1", Nickname: "Captured Name"}}}},
	})
	request := ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Generation: 2, Text: "client text", CorrelationID: "corr"}

	got := (mattermostUISendService{ctx: context.Background(), startup: startup}).Send(context.Background(), request)

	want := messages.MessageItem{ID: "opaque-post-id", CorrelationID: "opaque-correlation/id:1", CreatedAt: 1234, RootID: "root-1", Format: messages.FormatMattermostPlain, UserID: "u1", UserName: "Captured Name", Text: "server text", ReplyCount: 9, IsEdited: true}
	sent, ok := got.(ui.MattermostMessageSentMsg)
	if !ok || !reflect.DeepEqual(sent.Request, request) || !reflect.DeepEqual(sent.Message, want) {
		t.Fatalf("message=%#v want request=%#v item=%#v", got, request, want)
	}
}

func TestMattermostUISendFallsBackToUserID(t *testing.T) {
	client := &recordingMattermostSendClient{message: mattermost.Message{ID: "p1", ChannelID: "c1", UserID: "unknown"}}
	startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: client, usable: true}})
	got := (mattermostUISendService{ctx: context.Background(), startup: startup}).Send(context.Background(), ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Text: "hello", CorrelationID: "corr"})
	sent, ok := got.(ui.MattermostMessageSentMsg)
	if !ok || sent.Message.UserName != "unknown" {
		t.Fatalf("message=%#v", got)
	}
}

func TestMattermostUISendReturnsSafeFailureForClientErrorAndUnavailableServer(t *testing.T) {
	secret := "pat-secret request-secret correlation-secret"
	request := ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Generation: 3, Text: "request-secret", CorrelationID: "correlation-secret"}
	for _, tc := range []struct {
		name    string
		startup *mattermostStartup
	}{
		{name: "client error", startup: mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: &recordingMattermostSendClient{err: errors.New(secret)}, usable: true}})},
		{name: "missing server", startup: mattermostSendStartup(nil)},
		{name: "missing client", startup: mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {usable: true}})},
		{name: "unusable client", startup: mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: &recordingMattermostSendClient{}, usable: false}})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := (mattermostUISendService{ctx: context.Background(), startup: tc.startup}).Send(context.Background(), request)
			failed, ok := got.(ui.MattermostMessageSendFailedMsg)
			if !ok || !reflect.DeepEqual(failed.Request, request) {
				t.Fatalf("message=%#v", got)
			}
			if failed.Reason != mattermostSendFailureReason || strings.Contains(failed.Reason, "secret") || strings.Contains(failed.Reason, request.Text) || strings.Contains(failed.Reason, request.CorrelationID) {
				t.Fatalf("unsafe reason=%q", failed.Reason)
			}
		})
	}
}

func TestWireMattermostRuntimeSetsOnlyMattermostSendBoundary(t *testing.T) {
	app := &recordingMattermostRuntimeApp{}
	client := &recordingMattermostSendClient{
		message: mattermost.Message{ID: "p1", ChannelID: "c1"},
		thread: mattermost.MessagePage{Messages: []mattermost.Message{
			{ID: "root-1", ChannelID: "c1", UserID: "u1", Text: "root", CreatedAt: 1},
			{ID: "reply-1", ChannelID: "c1", UserID: "u1", RootID: "root-1", Text: "reply", CreatedAt: 2},
		}},
	}
	startup := mattermostSendStartup(map[ids.ServerID]mattermostServerContext{"s1": {client: client, usable: true}})
	wireMattermostRuntime(app, context.Background(), startup, newMattermostEventDB(t, "s1", "c1"))

	request := ui.MattermostSendRequest{ServerID: "s1", ChannelID: "c1", Text: "hello", CorrelationID: "corr"}
	if app.send == nil {
		t.Fatal("Mattermost send boundary was not wired")
	}
	if app.read == nil {
		t.Fatal("Mattermost read boundary was not wired")
	}
	if app.threads == nil {
		t.Fatal("Mattermost thread boundary was not wired")
	}
	threadRequest := ui.HistoryRequest{ServerID: "s1", ChannelID: "c1", Generation: 7}
	loaded, ok := app.threads.FetchScoped(context.Background(), threadRequest, "root-1").(ui.ThreadRepliesLoadedMsg)
	if !ok || loaded.Request != threadRequest || len(loaded.Replies) != 1 || loaded.Replies[0].ID != "reply-1" {
		t.Fatalf("thread fetch=%#v", loaded)
	}
	if cached := app.threads.CacheReadScoped(threadRequest, "root-1"); len(cached) != 2 || cached[0].ID != "root-1" || cached[1].ID != "reply-1" {
		t.Fatalf("thread cache=%#v", cached)
	}
	if _, ok := app.send.Send(context.Background(), request).(ui.MattermostMessageSentMsg); !ok {
		t.Fatal("Mattermost send boundary was not wired")
	}
	if app.slackSetCalls != 0 {
		t.Fatalf("Mattermost runtime changed Slack wiring %d times", app.slackSetCalls)
	}
}

type recordingMattermostRuntimeApp struct {
	history       ui.MattermostHistoryService
	send          ui.MattermostSendService
	read          ui.MattermostReadService
	threads       ui.ThreadService
	slackSetCalls int
}

func (a *recordingMattermostRuntimeApp) SetMattermostHistoryService(history ui.MattermostHistoryService) {
	a.history = history
}

func (a *recordingMattermostRuntimeApp) SetMattermostSendService(send ui.MattermostSendService) {
	a.send = send
}

func (a *recordingMattermostRuntimeApp) SetMattermostReadService(read ui.MattermostReadService) {
	a.read = read
}

func (a *recordingMattermostRuntimeApp) SetThreadService(threads ui.ThreadService) {
	a.threads = threads
}

func (a *recordingMattermostRuntimeApp) SetMessageService(ui.MessageService) {
	a.slackSetCalls++
}

type recordingMattermostSendClient struct {
	requests []mattermost.CreatePostRequest
	message  mattermost.Message
	err      error
	started  chan struct{}
	thread   mattermost.MessagePage
}

func (c *recordingMattermostSendClient) CreatePost(ctx context.Context, request mattermost.CreatePostRequest) (mattermost.Message, error) {
	c.requests = append(c.requests, request)
	if c.started != nil {
		close(c.started)
		<-ctx.Done()
		return mattermost.Message{}, ctx.Err()
	}
	return c.message, c.err
}

func (c *recordingMattermostSendClient) PostThread(context.Context, string) (mattermost.MessagePage, error) {
	return c.thread, c.err
}

func (c *recordingMattermostSendClient) ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error) {
	return mattermost.ViewChannelResult{}, errors.New("unused view")
}

func (*recordingMattermostSendClient) CurrentUser(context.Context) (*mattermost.User, error) {
	return nil, errors.New("unused bootstrap")
}
func (*recordingMattermostSendClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	return nil, nil
}
func (*recordingMattermostSendClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	return nil, nil
}
func (*recordingMattermostSendClient) ChannelMembershipsForUser(context.Context, string, string) ([]mattermost.ChannelMembership, error) {
	return nil, nil
}
func (*recordingMattermostSendClient) UsersByIDs(context.Context, []string) ([]mattermost.User, error) {
	return nil, nil
}
func (*recordingMattermostSendClient) UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error) {
	return nil, nil
}
func (*recordingMattermostSendClient) ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	return mattermost.MessagePage{}, errors.New("unused history")
}
func (*recordingMattermostSendClient) RunWebSocket(ctx context.Context, _ func(), _ func(mattermost.Event), _ func(error)) error {
	<-ctx.Done()
	return ctx.Err()
}

func mattermostSendStartup(contexts map[ids.ServerID]mattermostServerContext) *mattermostStartup {
	if contexts == nil {
		contexts = map[ids.ServerID]mattermostServerContext{}
	}
	return &mattermostStartup{contexts: contexts}
}
