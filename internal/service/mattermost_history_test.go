package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/mattermost"
)

type fakeMattermostHistoryClient struct {
	page      mattermost.MessagePage
	err       error
	users     []mattermost.User
	userErr   error
	userCalls [][]string
	postCalls []mattermost.ChannelPostsOptions
	usersFunc func(context.Context, []string) ([]mattermost.User, error)
}

func (f *fakeMattermostHistoryClient) ChannelPosts(_ context.Context, _ string, options mattermost.ChannelPostsOptions) (mattermost.MessagePage, error) {
	f.postCalls = append(f.postCalls, options)
	return f.page, f.err
}
func (f *fakeMattermostHistoryClient) UsersByIDs(ctx context.Context, ids []string) ([]mattermost.User, error) {
	f.userCalls = append(f.userCalls, append([]string(nil), ids...))
	if f.usersFunc != nil {
		return f.usersFunc(ctx, ids)
	}
	return f.users, f.userErr
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
	if got, want := page.AuthoritativeIDs, []string{"new", "reply", "old"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("authoritative ids=%v want %v", got, want)
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

func TestMattermostHistorySeparatesInclusiveAnchorFromDeletedIDs(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{OrderCount: 3, Messages: []mattermost.Message{{ID: "anchor", ChannelID: "c1"}, {ID: "deleted", ChannelID: "c1", DeletedAt: 2}, {ID: "older", ChannelID: "c1"}, {ID: "deleted", ChannelID: "c1", DeletedAt: 2}}}}
	page, err := NewMattermostHistoryService("s1", client, db, 3).FetchOlder(context.Background(), "c1", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := page.AuthoritativeIDs, []string{"anchor", "deleted", "older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if got, want := page.DeletedIDs, []string{"deleted"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted ids=%v want %v", got, want)
	}
	if got, want := historyIDs(page.Messages), []string{"older"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("presented=%v", got)
	}
	post, err := db.GetMattermostPost("s1", "deleted")
	if err != nil || post.DeletedAt != 2 {
		t.Fatalf("tombstone=%#v err=%v", post, err)
	}
}

func TestMattermostHistoryDeletedInclusiveAnchorIsTombstone(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "anchor", ChannelID: "c1", DeletedAt: 9}, {ID: "older", ChannelID: "c1"}}}}
	page, err := NewMattermostHistoryService("s1", client, db, 20).FetchOlder(context.Background(), "c1", "anchor")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := page.DeletedIDs, []string{"anchor"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deleted=%v", got)
	}
	post, err := db.GetMattermostPost("s1", "anchor")
	if err != nil || post.DeletedAt != 9 {
		t.Fatalf("anchor tombstone=%#v err=%v", post, err)
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
	page, err := svc.FetchRecent(context.Background(), "c1")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err=%v want live fetch error", err)
	}
	if !reflect.DeepEqual(historyIDs(page.Messages), []string{"cached"}) {
		t.Fatalf("cached fallback=%#v", page)
	}
}

func TestMattermostHistoryFetchFailureWithoutCacheReturnsEmptyPageAndError(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	page, err := NewMattermostHistoryService("s1", &fakeMattermostHistoryClient{err: errors.New("offline")}, db, 20).FetchRecent(context.Background(), "c1")
	if err == nil || !strings.Contains(err.Error(), "offline") {
		t.Fatalf("err=%v want live fetch error", err)
	}
	if len(page.Messages) != 0 {
		t.Fatalf("messages=%#v want empty fallback", page.Messages)
	}
}

func TestMattermostHistoryAuthorLookupFailureStillCachesAndPresentsPosts(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostHistoryClient{
		page:    mattermost.MessagePage{OrderCount: 1, Messages: []mattermost.Message{{ID: "p1", ChannelID: "c1", UserID: "unknown", Text: "body", CreatedAt: 1}}},
		userErr: errors.New("profile lookup failed secret-token"),
	}
	svc := NewMattermostHistoryService("s1", client, db, 1)
	page, err := svc.FetchRecent(context.Background(), "c1")
	if err != nil {
		t.Fatalf("FetchRecent=%v", err)
	}
	if len(page.Messages) != 1 || page.Messages[0].UserName != "unknown" || !page.HasMore {
		t.Fatalf("page=%#v", page)
	}
	if _, err := db.GetMattermostPost("s1", "p1"); err != nil {
		t.Fatalf("post not cached: %v", err)
	}
}

func TestMattermostHistoryAuthorLookupContextErrorAbortsWithoutCachingPage(t *testing.T) {
	for _, lookupErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			db := setupMattermostHistoryDB(t)
			client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "p1", ChannelID: "c1", UserID: "u1"}}}, userErr: lookupErr}
			_, err := NewMattermostHistoryService("s1", client, db, 20).FetchRecent(context.Background(), "c1")
			if !errors.Is(err, lookupErr) {
				t.Fatalf("err=%v", err)
			}
			if _, err := db.GetMattermostPost("s1", "p1"); err == nil {
				t.Fatal("canceled page was cached")
			}
		})
	}
}

func TestMattermostHistoryCancellationDuringAuthorLookupAbortsWithoutCachingPage(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	entered := make(chan struct{})
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "p1", ChannelID: "c1", UserID: "u1"}}}, usersFunc: func(ctx context.Context, _ []string) ([]mattermost.User, error) {
		close(entered)
		<-ctx.Done()
		return nil, ctx.Err()
	}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewMattermostHistoryService("s1", client, db, 20).FetchRecent(ctx, "c1")
		done <- err
	}()
	<-entered
	cancel()
	err := <-done
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
	if _, err := db.GetMattermostPost("s1", "p1"); err == nil {
		t.Fatal("canceled page was cached")
	}
}

func TestMattermostHistoryPassesContextToCacheWrite(t *testing.T) {
	store := &contextRecordingHistoryStore{writeEntered: make(chan struct{})}
	client := &fakeMattermostHistoryClient{page: mattermost.MessagePage{Messages: []mattermost.Message{{ID: "p1", ChannelID: "c1"}}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewMattermostHistoryService("s1", client, store, 20).FetchRecent(ctx, "c1")
		done <- err
	}()
	<-store.writeEntered
	cancel()
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("err=%v", err)
	}
}

type contextRecordingHistoryStore struct {
	writeEntered chan struct{}
}

func (*contextRecordingHistoryStore) ListMattermostChannelTimeline(string, string, int, string) ([]cache.MattermostPost, error) {
	return nil, nil
}
func (*contextRecordingHistoryStore) ListMattermostUsers(string) ([]cache.MattermostUser, error) {
	return nil, nil
}
func (s *contextRecordingHistoryStore) UpsertMattermostHistoryContext(ctx context.Context, _ string, _ []cache.MattermostPost, _ []cache.MattermostUser) error {
	close(s.writeEntered)
	<-ctx.Done()
	return ctx.Err()
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
