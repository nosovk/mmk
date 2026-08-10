package service

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/mattermost"
)

type fakeMattermostHistoryClient struct {
	page      mattermost.MessagePage
	err       error
	users     []mattermost.User
	userCalls [][]string
	postCalls []mattermost.ChannelPostsOptions
}

func (f *fakeMattermostHistoryClient) ChannelPosts(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	f.postCalls = append(f.postCalls, options)
	return f.page, f.err
}
func (f *fakeMattermostHistoryClient) UsersByIDs(_ context.Context, ids []string) ([]mattermost.User, error) {
	f.userCalls = append(f.userCalls, append([]string(nil), ids...))
	return f.users, nil
}

func TestMattermostHistoryFetchCachesExactPageAndResolvesUnknownAuthorsOnce(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	_ = db.UpsertMattermostUser("s1", cache.MattermostUser{ID: "known", Nickname: "Known"})
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "new", ChannelID: "c1", UserID: "unknown", Text: "new", CreatedAt: 20},
		{ID: "reply", ChannelID: "c1", UserID: "known", RootID: "old", Text: "reply", CreatedAt: 15},
		{ID: "old", ChannelID: "c1", UserID: "unknown", Text: "old", CreatedAt: 10, DeletedAt: 30},
	}}, users: []mattermost.User{{ID: "unknown", Nickname: "Unknown", UpdatedAt: 1}}}
	svc := NewMattermostHistoryService("s1", client, db, 3)
	page, err := svc.FetchRecent(context.Background(), "c1")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := historyIDs(page.Messages), []string{"reply", "new"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if !page.HasMore {
		t.Fatal("full unique page must have HasMore")
	}
	if len(client.userCalls) != 1 || !reflect.DeepEqual(client.userCalls[0], []string{"unknown"}) {
		t.Fatalf("user calls=%v", client.userCalls)
	}
	if page.Messages[0].UserName != "Known" || page.Messages[1].UserName != "Unknown" {
		t.Fatalf("names=%#v", page.Messages)
	}
	for _, id := range []string{"new", "reply", "old"} {
		if _, err := db.GetMattermostPost("s1", id); err != nil {
			t.Fatalf("cached %s: %v", id, err)
		}
	}
}

func TestMattermostHistoryReadCachedNeverUsesNetworkAndIncludesReplies(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	_ = db.UpsertMattermostUser("s1", cache.MattermostUser{ID: "u1", Username: "alice"})
	_ = db.UpsertMattermostPosts("s1", []cache.MattermostPost{{ID: "root", ChannelID: "c1", UserID: "u1", CreatedAt: 10}, {ID: "reply", ChannelID: "c1", UserID: "missing", RootID: "root", CreatedAt: 11}})
	client := &fakeMattermostHistoryClient{}
	svc := NewMattermostHistoryService("s1", client, db, 20)
	page, err := svc.ReadCached("c1", "")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := historyIDs(page.Messages), []string{"root", "reply"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v", got)
	}
	if page.Messages[1].UserName != "missing" || len(client.userCalls) != 0 || len(client.postCalls) != 0 {
		t.Fatalf("network used or fallback wrong: %#v", page)
	}
}

func TestMattermostHistoryOlderUsesBeforeAndShortPageExhausts(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "older", ChannelID: "c1", CreatedAt: 1}}}}
	svc := NewMattermostHistoryService("s1", client, db, 2)
	page, err := svc.FetchOlder(context.Background(), "c1", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if page.HasMore {
		t.Fatal("short page must be exhausted")
	}
	if len(client.postCalls) != 1 || client.postCalls[0].Before != "anchor" || client.postCalls[0].Page != 0 {
		t.Fatalf("calls=%#v", client.postCalls)
	}
}

func TestMattermostHistoryOlderDedupesIncludedAnchor(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "anchor", ChannelID: "c1", CreatedAt: 2},
		{ID: "older", ChannelID: "c1", CreatedAt: 1},
	}}}
	svc := NewMattermostHistoryService("s1", client, db, 2)
	page, err := svc.FetchOlder(context.Background(), "c1", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := historyIDs(page.Messages), []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if !page.HasMore {
		t.Fatal("HasMore must use unique ordered response count before anchor filtering")
	}
}

func TestMattermostHistoryFetchFailureIsDistinctAndDoesNotEraseCache(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	_ = db.UpsertMattermostPost("s1", cache.MattermostPost{ID: "cached", ChannelID: "c1", CreatedAt: 1})
	svc := NewMattermostHistoryService("s1", &fakeMattermostHistoryClient{err: errors.New("offline")}, db, 20)
	if _, err := svc.FetchRecent(context.Background(), "c1"); err == nil {
		t.Fatal("expected fetch error")
	}
	page, err := svc.ReadCached("c1", "")
	if err != nil || !reflect.DeepEqual(historyIDs(page.Messages), []string{"cached"}) {
		t.Fatalf("cached=%#v err=%v", page, err)
	}
}

func setupMattermostHistoryDB(t *testing.T) *cache.DB {
	t.Helper()
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertMattermostServer(cache.MattermostServer{ID: "s1", URL: "https://one.example", CurrentUserID: "self"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostTeam("s1", cache.MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", cache.MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	return db
}

func historyIDs(items []MattermostHistoryMessage) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].Message.ID
	}
	return out
}
