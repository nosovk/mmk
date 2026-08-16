package service

import (
	"context"
	"errors"
	"fmt"
	"sort"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/mattermost"
)

type mattermostThreadClient interface {
	PostThread(context.Context, string) (mattermost.MessagePage, error)
	UsersByIDs(context.Context, []string) ([]mattermost.User, error)
}

type mattermostThreadStore interface {
	ListMattermostThreadPosts(string, string, string) ([]cache.MattermostPost, error)
	ListMattermostUsers(string) ([]cache.MattermostUser, error)
	UpsertMattermostHistoryContext(context.Context, string, []cache.MattermostPost, []cache.MattermostUser) error
}

type MattermostThreadService struct {
	serverID string
	client   mattermostThreadClient
	store    mattermostThreadStore
}

func NewMattermostThreadService(serverID string, client mattermostThreadClient, store mattermostThreadStore) *MattermostThreadService {
	return &MattermostThreadService{serverID: serverID, client: client, store: store}
}

func (s *MattermostThreadService) ReadCached(channelID, rootID string) ([]MattermostHistoryMessage, error) {
	posts, err := s.store.ListMattermostThreadPosts(s.serverID, channelID, rootID)
	if err != nil {
		return nil, err
	}
	users, err := s.store.ListMattermostUsers(s.serverID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(users))
	for _, user := range users {
		names[user.ID] = cachedMattermostDisplayName(user)
	}
	return presentMattermostThread(presentCachedMattermostPosts(posts, names), rootID), nil
}

func (s *MattermostThreadService) Fetch(ctx context.Context, channelID, rootID string) ([]MattermostHistoryMessage, error) {
	page, err := s.client.PostThread(ctx, rootID)
	if err != nil {
		return nil, err
	}
	if err := validateMattermostThread(page.Messages, channelID, rootID); err != nil {
		return nil, err
	}
	cachedUsers, err := s.store.ListMattermostUsers(s.serverID)
	if err != nil {
		return nil, err
	}
	names := make(map[string]string, len(cachedUsers))
	for _, user := range cachedUsers {
		names[user.ID] = cachedMattermostDisplayName(user)
	}
	unknown := make([]string, 0)
	seenUnknown := make(map[string]struct{})
	posts := make([]cache.MattermostPost, len(page.Messages))
	for i, message := range page.Messages {
		posts[i] = cachePost(message)
		if message.UserID == "" || names[message.UserID] != "" {
			continue
		}
		if _, seen := seenUnknown[message.UserID]; seen {
			continue
		}
		seenUnknown[message.UserID] = struct{}{}
		unknown = append(unknown, message.UserID)
	}
	resolved := []mattermost.User{}
	if len(unknown) > 0 {
		resolved, err = s.client.UsersByIDs(ctx, unknown)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
				return nil, err
			}
			resolved = nil
		}
	}
	users := make([]cache.MattermostUser, len(resolved))
	for i, user := range resolved {
		users[i] = cacheUserRecord(user)
		names[user.ID] = user.DisplayName()
	}
	if err := s.store.UpsertMattermostHistoryContext(ctx, s.serverID, posts, users); err != nil {
		return nil, err
	}
	presented := make([]MattermostHistoryMessage, 0, len(page.Messages))
	for _, message := range page.Messages {
		if message.DeletedAt != 0 {
			continue
		}
		name := names[message.UserID]
		if name == "" {
			name = message.UserID
		}
		presented = append(presented, MattermostHistoryMessage{Message: message, UserName: name})
	}
	return presentMattermostThread(presented, rootID), nil
}

func validateMattermostThread(messages []mattermost.Message, channelID, rootID string) error {
	foundRoot := false
	for _, message := range messages {
		if message.ID == rootID {
			foundRoot = true
			break
		}
	}
	if !foundRoot {
		return fmt.Errorf("Mattermost thread is missing requested root %q", rootID)
	}
	for _, message := range messages {
		if message.ChannelID != channelID {
			return fmt.Errorf("Mattermost thread post %q belongs to channel %q, expected %q", message.ID, message.ChannelID, channelID)
		}
		if message.ID == rootID {
			if message.RootID != "" {
				return fmt.Errorf("Mattermost thread root %q has non-empty root_id %q", rootID, message.RootID)
			}
			continue
		}
		if message.RootID != rootID {
			return fmt.Errorf("Mattermost thread reply %q has root_id %q, expected %q", message.ID, message.RootID, rootID)
		}
	}
	return nil
}

func presentMattermostThread(messages []MattermostHistoryMessage, rootID string) []MattermostHistoryMessage {
	sort.SliceStable(messages, func(i, j int) bool {
		iRoot := messages[i].Message.ID == rootID
		jRoot := messages[j].Message.ID == rootID
		if iRoot != jRoot {
			return iRoot
		}
		if messages[i].Message.CreatedAt == messages[j].Message.CreatedAt {
			return messages[i].Message.ID < messages[j].Message.ID
		}
		return messages[i].Message.CreatedAt < messages[j].Message.CreatedAt
	})
	return messages
}
