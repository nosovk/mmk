package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/mattermost"
)

type fakeMattermostThreadClient struct {
	page       mattermost.MessagePage
	postErr    error
	userErr    error
	users      []mattermost.User
	postCalls  int
	postRootID string
	userCalls  [][]string
}

type fakeMattermostThreadStore struct {
	posts    []cache.MattermostPost
	postErr  error
	users    []cache.MattermostUser
	userErr  error
	writeErr error
}

type contextRecordingThreadStore struct {
	writeEntered chan struct{}
}

func (*contextRecordingThreadStore) ListMattermostThreadPosts(string, string, string) ([]cache.MattermostPost, error) {
	return nil, nil
}

func (*contextRecordingThreadStore) ListMattermostUsers(string) ([]cache.MattermostUser, error) {
	return nil, nil
}

func (s *contextRecordingThreadStore) UpsertMattermostHistoryContext(ctx context.Context, _ string, _ []cache.MattermostPost, _ []cache.MattermostUser) error {
	close(s.writeEntered)
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(time.Second):
		return errors.New("timed out waiting for caller context cancellation")
	}
}

func (f *fakeMattermostThreadClient) PostThread(_ context.Context, rootID string) (mattermost.MessagePage, error) {
	f.postCalls++
	f.postRootID = rootID
	return f.page, f.postErr
}

func (f *fakeMattermostThreadClient) UsersByIDs(_ context.Context, ids []string) ([]mattermost.User, error) {
	f.userCalls = append(f.userCalls, append([]string(nil), ids...))
	return f.users, f.userErr
}

func (s *fakeMattermostThreadStore) ListMattermostThreadPosts(string, string, string) ([]cache.MattermostPost, error) {
	return s.posts, s.postErr
}

func (s *fakeMattermostThreadStore) ListMattermostUsers(string) ([]cache.MattermostUser, error) {
	return s.users, s.userErr
}

func (s *fakeMattermostThreadStore) UpsertMattermostHistoryContext(context.Context, string, []cache.MattermostPost, []cache.MattermostUser) error {
	return s.writeErr
}

func TestMattermostThreadReadCachedReturnsRootFirstChronologicalMessages(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	if err := db.UpsertMattermostHistory("s1", []cache.MattermostPost{
		{ID: "reply-late", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "late", CreatedAt: 30},
		{ID: "root", ChannelID: "c1", UserID: "u1", Text: "root", CreatedAt: 20},
		{ID: "reply-early", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "early", CreatedAt: 10},
		{ID: "deleted", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "deleted", CreatedAt: 40, DeletedAt: 50},
	}, []cache.MattermostUser{
		{ID: "u1", Nickname: "Root Author"},
		{ID: "u2", FirstName: "Reply", LastName: "Author"},
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMattermostThreadClient{}

	messages, err := NewMattermostThreadService("s1", client, db).ReadCached("c1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := historyIDs(messages), []string{"root", "reply-early", "reply-late"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if got, want := []string{messages[0].UserName, messages[1].UserName, messages[2].UserName}, []string{"Root Author", "Reply Author", "Reply Author"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names=%v want %v", got, want)
	}
	if client.postCalls != 0 || len(client.userCalls) != 0 {
		t.Fatalf("network calls: posts=%d users=%d", client.postCalls, len(client.userCalls))
	}
}

func TestMattermostThreadFetchPersistsThreadAndNewUsersAndReturnsEnrichedMessages(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostThreadClient{
		page: mattermost.MessagePage{Messages: []mattermost.Message{
			{ID: "reply-2", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "second", CreatedAt: 30},
			{ID: "root", ChannelID: "c1", UserID: "u1", Text: "root", CreatedAt: 10},
			{ID: "reply-1", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "first", CreatedAt: 20},
		}},
		users: []mattermost.User{
			{ID: "u1", Nickname: "Root Author", UpdatedAt: 1},
			{ID: "u2", Username: "reply-author", UpdatedAt: 2},
		},
	}

	messages, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if client.postCalls != 1 || client.postRootID != "root" {
		t.Fatalf("post calls=%d root=%q", client.postCalls, client.postRootID)
	}
	if got, want := client.userCalls, [][]string{{"u2", "u1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user calls=%v want %v", got, want)
	}
	if got, want := historyIDs(messages), []string{"root", "reply-1", "reply-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if got, want := []string{messages[0].UserName, messages[1].UserName, messages[2].UserName}, []string{"Root Author", "reply-author", "reply-author"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("names=%v want %v", got, want)
	}
	for _, id := range []string{"root", "reply-1", "reply-2"} {
		if _, err := db.GetMattermostPost("s1", id); err != nil {
			t.Fatalf("cached post %q: %v", id, err)
		}
	}
	for _, id := range []string{"u1", "u2"} {
		if _, err := db.GetMattermostUser("s1", id); err != nil {
			t.Fatalf("cached user %q: %v", id, err)
		}
	}
}

func TestMattermostThreadFetchPersistsTombstonesAndOmitsThemFromPresentation(t *testing.T) {
	tests := []struct {
		name     string
		messages []mattermost.Message
		wantIDs  []string
	}{
		{
			name: "deleted reply",
			messages: []mattermost.Message{
				{ID: "root", ChannelID: "c1", UserID: "u1", CreatedAt: 10},
				{ID: "deleted-reply", ChannelID: "c1", UserID: "u2", RootID: "root", CreatedAt: 20, DeletedAt: 40},
				{ID: "reply", ChannelID: "c1", UserID: "u2", RootID: "root", CreatedAt: 30},
			},
			wantIDs: []string{"root", "reply"},
		},
		{
			name: "deleted root",
			messages: []mattermost.Message{
				{ID: "root", ChannelID: "c1", UserID: "u1", CreatedAt: 10, DeletedAt: 40},
				{ID: "reply", ChannelID: "c1", UserID: "u2", RootID: "root", CreatedAt: 20},
			},
			wantIDs: []string{"reply"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupMattermostHistoryDB(t)
			client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: test.messages}}
			svc := NewMattermostThreadService("s1", client, db)

			messages, err := svc.Fetch(context.Background(), "c1", "root")
			if err != nil {
				t.Fatal(err)
			}
			if got := historyIDs(messages); !reflect.DeepEqual(got, test.wantIDs) {
				t.Fatalf("live ids=%v want %v", got, test.wantIDs)
			}
			for _, message := range test.messages {
				post, err := db.GetMattermostPost("s1", message.ID)
				if err != nil {
					t.Fatalf("cached post %q: %v", message.ID, err)
				}
				if post.DeletedAt != message.DeletedAt {
					t.Fatalf("cached post %q deleted_at=%d want %d", message.ID, post.DeletedAt, message.DeletedAt)
				}
			}
			cached, err := svc.ReadCached("c1", "root")
			if err != nil {
				t.Fatal(err)
			}
			if got := historyIDs(cached); !reflect.DeepEqual(got, test.wantIDs) {
				t.Fatalf("cached ids=%v want %v", got, test.wantIDs)
			}
		})
	}
}

func TestMattermostThreadFetchReturnsMergedCacheStateAfterPersistence(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	if err := db.UpsertMattermostHistory("s1", []cache.MattermostPost{
		{ID: "root", ChannelID: "c1", UserID: "u1", Text: "newer cached edit", CreatedAt: 10, UpdatedAt: 50, EditedAt: 50},
		{ID: "reply", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "deleted concurrently", CreatedAt: 20, DeletedAt: 60},
	}, []cache.MattermostUser{
		{ID: "u1", Nickname: "Cached Root"},
		{ID: "u2", Nickname: "Cached Reply"},
	}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "reply", ChannelID: "c1", UserID: "u2", RootID: "root", Text: "stale live reply", CreatedAt: 20, UpdatedAt: 30},
		{ID: "root", ChannelID: "c1", UserID: "u1", Text: "stale live root", CreatedAt: 10, UpdatedAt: 30},
	}}}

	messages, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if got, want := historyIDs(messages), []string{"root"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ids=%v want %v", got, want)
	}
	if messages[0].Message.Text != "newer cached edit" || messages[0].Message.UpdatedAt != 50 || messages[0].UserName != "Cached Root" {
		t.Fatalf("root=%#v", messages[0])
	}
}

func TestMattermostThreadFetchRejectsBlankChannelBeforeClientCall(t *testing.T) {
	for _, channelID := range []string{"", " \t\n "} {
		t.Run(strings.ReplaceAll(channelID, " ", "space"), func(t *testing.T) {
			client := &fakeMattermostThreadClient{}

			_, err := NewMattermostThreadService("s1", client, setupMattermostHistoryDB(t)).Fetch(context.Background(), channelID, "root")
			if err == nil || !strings.Contains(err.Error(), "channel") {
				t.Fatalf("err=%v want channel validation error", err)
			}
			if client.postCalls != 0 {
				t.Fatalf("post calls=%d want 0", client.postCalls)
			}
		})
	}
}

func TestMattermostThreadFetchReturnsControlledErrorWhenClientUnavailable(t *testing.T) {
	_, err := NewMattermostThreadService("s1", nil, setupMattermostHistoryDB(t)).Fetch(context.Background(), "c1", "root")
	if err == nil || !strings.Contains(err.Error(), "client unavailable") {
		t.Fatalf("err=%v want client unavailable", err)
	}
}

func TestMattermostThreadDependencyErrorsIncludeOperationContext(t *testing.T) {
	sentinel := errors.New("sentinel")
	rootPage := mattermost.MessagePage{Messages: []mattermost.Message{{ID: "root", ChannelID: "c1", CreatedAt: 10}}}
	tests := []struct {
		name     string
		run      func() error
		wantText string
		wantErr  error
	}{
		{
			name: "read cached posts",
			run: func() error {
				_, err := NewMattermostThreadService("s1", nil, &fakeMattermostThreadStore{postErr: sentinel}).ReadCached("c1", "root")
				return err
			},
			wantText: "list cached Mattermost thread posts",
			wantErr:  sentinel,
		},
		{
			name: "read cached users",
			run: func() error {
				_, err := NewMattermostThreadService("s1", nil, &fakeMattermostThreadStore{userErr: sentinel}).ReadCached("c1", "root")
				return err
			},
			wantText: "list cached Mattermost thread users",
			wantErr:  sentinel,
		},
		{
			name: "fetch thread",
			run: func() error {
				_, err := NewMattermostThreadService("s1", &fakeMattermostThreadClient{postErr: sentinel}, &fakeMattermostThreadStore{}).Fetch(context.Background(), "c1", "root")
				return err
			},
			wantText: "fetch Mattermost thread",
			wantErr:  sentinel,
		},
		{
			name: "list users before enrichment",
			run: func() error {
				_, err := NewMattermostThreadService("s1", &fakeMattermostThreadClient{page: rootPage}, &fakeMattermostThreadStore{userErr: sentinel}).Fetch(context.Background(), "c1", "root")
				return err
			},
			wantText: "list cached Mattermost thread users",
			wantErr:  sentinel,
		},
		{
			name: "author lookup cancellation",
			run: func() error {
				page := mattermost.MessagePage{Messages: []mattermost.Message{{ID: "root", ChannelID: "c1", UserID: "u1", CreatedAt: 10}}}
				_, err := NewMattermostThreadService("s1", &fakeMattermostThreadClient{page: page, userErr: context.Canceled}, &fakeMattermostThreadStore{}).Fetch(context.Background(), "c1", "root")
				return err
			},
			wantText: "resolve Mattermost thread authors",
			wantErr:  context.Canceled,
		},
		{
			name: "persist thread",
			run: func() error {
				_, err := NewMattermostThreadService("s1", &fakeMattermostThreadClient{page: rootPage}, &fakeMattermostThreadStore{writeErr: sentinel}).Fetch(context.Background(), "c1", "root")
				return err
			},
			wantText: "cache Mattermost thread",
			wantErr:  sentinel,
		},
		{
			name: "read merged thread",
			run: func() error {
				store := &fakeMattermostThreadStore{}
				store.posts = nil
				client := &fakeMattermostThreadClient{page: rootPage}
				store.postErr = sentinel
				_, err := NewMattermostThreadService("s1", client, store).Fetch(context.Background(), "c1", "root")
				return err
			},
			wantText: "read merged Mattermost thread",
			wantErr:  sentinel,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			if !errors.Is(err, test.wantErr) || !strings.Contains(err.Error(), test.wantText) {
				t.Fatalf("err=%v want wrapping %v with %q", err, test.wantErr, test.wantText)
			}
		})
	}
}

func TestMattermostThreadFetchUsesCachedUsersWithoutLookup(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	if err := db.UpsertMattermostUser("s1", cache.MattermostUser{ID: "u1", Nickname: "Cached Author"}); err != nil {
		t.Fatal(err)
	}
	client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "root", ChannelID: "c1", UserID: "u1", CreatedAt: 10},
		{ID: "reply", ChannelID: "c1", UserID: "u1", RootID: "root", CreatedAt: 20},
	}}}

	messages, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if len(client.userCalls) != 0 {
		t.Fatalf("user calls=%v want none", client.userCalls)
	}
	if messages[0].UserName != "Cached Author" || messages[1].UserName != "Cached Author" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestMattermostThreadFetchFallsBackToUserIDAndPersistsOnLookupFailure(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostThreadClient{
		page: mattermost.MessagePage{Messages: []mattermost.Message{
			{ID: "root", ChannelID: "c1", UserID: "unknown", CreatedAt: 10},
			{ID: "reply", ChannelID: "c1", UserID: "unknown", RootID: "root", CreatedAt: 20},
		}},
		userErr: errors.New("profile lookup failed"),
	}

	messages, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
	if err != nil {
		t.Fatalf("Fetch=%v", err)
	}
	if got, want := client.userCalls, [][]string{{"unknown"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user calls=%v want %v", got, want)
	}
	if messages[0].UserName != "unknown" || messages[1].UserName != "unknown" {
		t.Fatalf("messages=%#v", messages)
	}
	for _, id := range []string{"root", "reply"} {
		if _, err := db.GetMattermostPost("s1", id); err != nil {
			t.Fatalf("cached post %q: %v", id, err)
		}
	}
}

func TestMattermostThreadFetchLookupContextErrorsAbortBeforePersistence(t *testing.T) {
	for _, lookupErr := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(lookupErr.Error(), func(t *testing.T) {
			db := setupMattermostHistoryDB(t)
			client := &fakeMattermostThreadClient{
				page: mattermost.MessagePage{Messages: []mattermost.Message{
					{ID: "root", ChannelID: "c1", UserID: "unknown", CreatedAt: 10},
				}},
				userErr: lookupErr,
			}

			_, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
			if !errors.Is(err, lookupErr) {
				t.Fatalf("err=%v want %v", err, lookupErr)
			}
			if _, err := db.GetMattermostPost("s1", "root"); err == nil {
				t.Fatal("post persisted after lookup context error")
			}
		})
	}
}

func TestMattermostThreadFetchRootOnlyReturnsRoot(t *testing.T) {
	db := setupMattermostHistoryDB(t)
	client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "root", ChannelID: "c1", UserID: "u1", Text: "root", CreatedAt: 10},
	}}}

	messages, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
	if err != nil {
		t.Fatal(err)
	}
	if messages == nil || len(messages) != 1 || messages[0].Message.ID != "root" {
		t.Fatalf("messages=%#v", messages)
	}
}

func TestMattermostThreadFetchRejectsInvalidThreadBeforePersistence(t *testing.T) {
	tests := []struct {
		name     string
		messages []mattermost.Message
		wantErr  string
	}{
		{name: "missing root", messages: []mattermost.Message{{ID: "reply", ChannelID: "c1", RootID: "root", CreatedAt: 20}}, wantErr: "missing"},
		{name: "wrong root identity", messages: []mattermost.Message{{ID: "other", ChannelID: "c1", CreatedAt: 10}, {ID: "reply", ChannelID: "c1", RootID: "root", CreatedAt: 20}}, wantErr: "missing"},
		{name: "root has root ID", messages: []mattermost.Message{{ID: "root", ChannelID: "c1", RootID: "other", CreatedAt: 10}}, wantErr: "root_id"},
		{name: "cross channel", messages: []mattermost.Message{{ID: "root", ChannelID: "c2", CreatedAt: 10}}, wantErr: "channel"},
		{name: "bad reply root", messages: []mattermost.Message{{ID: "root", ChannelID: "c1", CreatedAt: 10}, {ID: "reply", ChannelID: "c1", RootID: "other", CreatedAt: 20}}, wantErr: "root_id"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			db := setupMattermostHistoryDB(t)
			client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: test.messages}}

			_, err := NewMattermostThreadService("s1", client, db).Fetch(context.Background(), "c1", "root")
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("err=%v want containing %q", err, test.wantErr)
			}
			for _, message := range test.messages {
				if _, err := db.GetMattermostPost("s1", message.ID); err == nil {
					t.Fatalf("invalid post %q persisted", message.ID)
				}
			}
		})
	}
}

func TestMattermostThreadFetchPassesCallerContextToStoreWrite(t *testing.T) {
	store := &contextRecordingThreadStore{writeEntered: make(chan struct{})}
	client := &fakeMattermostThreadClient{page: mattermost.MessagePage{Messages: []mattermost.Message{
		{ID: "root", ChannelID: "c1", CreatedAt: 10},
	}}}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := NewMattermostThreadService("s1", client, store).Fetch(ctx, "c1", "root")
		done <- err
	}()
	select {
	case <-store.writeEntered:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for store write")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v want context canceled", err)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for Fetch completion")
	}
}
