package service

import (
	"context"
	"fmt"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/mattermost"
)

type mattermostHistoryClient interface {
	ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error)
	UsersByIDs(context.Context, []string) ([]mattermost.User, error)
}

type mattermostHistoryStore interface {
	ListMattermostChannelTimeline(string, string, int, string) ([]cache.MattermostPost, error)
	ListMattermostUsers(string) ([]cache.MattermostUser, error)
	UpsertMattermostHistory(string, []cache.MattermostPost, []cache.MattermostUser) error
}

type MattermostHistoryMessage struct {
	Message  mattermost.Message
	UserName string
}

type MattermostHistoryPage struct {
	Messages []MattermostHistoryMessage
	HasMore  bool
}

type MattermostHistoryService struct {
	serverID string
	client   mattermostHistoryClient
	store    mattermostHistoryStore
	perPage  int
}

func NewMattermostHistoryService(serverID string, client mattermostHistoryClient, store mattermostHistoryStore, perPage int) *MattermostHistoryService {
	return &MattermostHistoryService{serverID: serverID, client: client, store: store, perPage: perPage}
}

func (s *MattermostHistoryService) ReadCached(channelID, beforeID string) (MattermostHistoryPage, error) {
	posts, err := s.store.ListMattermostChannelTimeline(s.serverID, channelID, s.perPage, beforeID)
	if err != nil {
		return MattermostHistoryPage{}, err
	}
	users, err := s.store.ListMattermostUsers(s.serverID)
	if err != nil {
		return MattermostHistoryPage{}, err
	}
	names := make(map[string]string, len(users))
	for _, user := range users {
		names[user.ID] = cachedMattermostDisplayName(user)
	}
	return MattermostHistoryPage{Messages: presentCachedMattermostPosts(posts, names), HasMore: len(posts) == s.perPage}, nil
}

func (s *MattermostHistoryService) FetchRecent(ctx context.Context, channelID string) (MattermostHistoryPage, error) {
	return s.fetch(ctx, channelID, "")
}

func (s *MattermostHistoryService) FetchOlder(ctx context.Context, channelID, beforeID string) (MattermostHistoryPage, error) {
	return s.fetch(ctx, channelID, beforeID)
}

func (s *MattermostHistoryService) fetch(ctx context.Context, channelID, beforeID string) (MattermostHistoryPage, error) {
	if s.client == nil {
		return MattermostHistoryPage{}, fmt.Errorf("fetch Mattermost history: client unavailable")
	}
	page, err := s.client.ChannelPosts(ctx, channelID, mattermost.ChannelPostsOptions{Page: 0, PerPage: s.perPage, Before: beforeID})
	if err != nil {
		return MattermostHistoryPage{}, fmt.Errorf("fetch Mattermost history: %w", err)
	}
	orderCount := page.OrderCount
	if orderCount == 0 && len(page.Messages) > 0 {
		orderCount = len(page.Messages)
	}
	cachedUsers, err := s.store.ListMattermostUsers(s.serverID)
	if err != nil {
		return MattermostHistoryPage{}, err
	}
	names := make(map[string]string, len(cachedUsers))
	for _, user := range cachedUsers {
		names[user.ID] = cachedMattermostDisplayName(user)
	}
	unknown := make([]string, 0)
	seenUnknown := map[string]struct{}{}
	posts := make([]cache.MattermostPost, len(page.Messages))
	for i, message := range page.Messages {
		posts[i] = cachePost(message)
		if message.UserID != "" {
			if _, known := names[message.UserID]; !known {
				if _, seen := seenUnknown[message.UserID]; !seen {
					seenUnknown[message.UserID] = struct{}{}
					unknown = append(unknown, message.UserID)
				}
			}
		}
	}
	resolved := []mattermost.User{}
	if len(unknown) > 0 {
		resolved, err = s.client.UsersByIDs(ctx, unknown)
		if err != nil {
			// Author enrichment is best-effort. The post page is authoritative
			// and remains useful with user-ID fallbacks.
			resolved = nil
		}
	}
	cacheUsers := make([]cache.MattermostUser, len(resolved))
	for i, user := range resolved {
		cacheUsers[i] = cacheUserRecord(user)
		names[user.ID] = user.DisplayName()
	}
	if err := s.store.UpsertMattermostHistory(s.serverID, posts, cacheUsers); err != nil {
		return MattermostHistoryPage{}, fmt.Errorf("cache Mattermost history: %w", err)
	}
	presented := make([]MattermostHistoryMessage, 0, len(page.Messages))
	for i := len(page.Messages) - 1; i >= 0; i-- {
		message := page.Messages[i]
		if message.DeletedAt != 0 || beforeID != "" && message.ID == beforeID {
			continue
		}
		name := names[message.UserID]
		if name == "" {
			name = message.UserID
		}
		presented = append(presented, MattermostHistoryMessage{Message: message, UserName: name})
	}
	return MattermostHistoryPage{Messages: presented, HasMore: orderCount == s.perPage}, nil
}

func cachePost(m mattermost.Message) cache.MattermostPost {
	return cache.MattermostPost{ID: m.ID, ChannelID: m.ChannelID, UserID: m.UserID, RootID: m.RootID, Text: m.Text, CreatedAt: m.CreatedAt, UpdatedAt: m.UpdatedAt, EditedAt: m.EditedAt, DeletedAt: m.DeletedAt, ReplyCount: m.ReplyCount}
}

func cacheUserRecord(u mattermost.User) cache.MattermostUser {
	return cache.MattermostUser{ID: u.ID, Username: u.Username, Nickname: u.Nickname, FirstName: u.FirstName, LastName: u.LastName, UpdatedAt: u.UpdatedAt}
}

func cachedMattermostDisplayName(u cache.MattermostUser) string {
	return (mattermost.User{ID: u.ID, Username: u.Username, Nickname: u.Nickname, FirstName: u.FirstName, LastName: u.LastName}).DisplayName()
}

func presentCachedMattermostPosts(posts []cache.MattermostPost, names map[string]string) []MattermostHistoryMessage {
	out := make([]MattermostHistoryMessage, 0, len(posts))
	for _, post := range posts {
		name := names[post.UserID]
		if name == "" {
			name = post.UserID
		}
		out = append(out, MattermostHistoryMessage{Message: mattermost.Message{ID: post.ID, ChannelID: post.ChannelID, UserID: post.UserID, RootID: post.RootID, Text: post.Text, CreatedAt: post.CreatedAt, UpdatedAt: post.UpdatedAt, EditedAt: post.EditedAt, DeletedAt: post.DeletedAt, ReplyCount: post.ReplyCount}, UserName: name})
	}
	return out
}
