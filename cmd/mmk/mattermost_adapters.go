package main

import (
	"fmt"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
)

func mattermostServerFromRegistry(server config.MattermostServer) mattermost.Server {
	name := strings.TrimSpace(server.DisplayName)
	if name == "" {
		if parsed, err := url.Parse(server.URL); err == nil {
			name = parsed.Hostname()
		}
	}
	if name == "" {
		name = server.ID
	}
	return mattermost.Server{ID: server.ID, Name: name, URL: server.URL, UserID: server.UserID}
}

func mattermostCacheSnapshot(snapshot service.ServerSnapshot, observedAt time.Time) cache.MattermostBootstrapSnapshot {
	raw := cache.MattermostBootstrapSnapshot{
		Server:      cache.MattermostServer{ID: snapshot.Server.ID, Name: snapshot.Server.Name, URL: snapshot.Server.URL, CurrentUserID: snapshot.CurrentUser.ID, LastSyncedAt: observedAt.UnixMilli()},
		CurrentUser: cacheUser(snapshot.CurrentUser), ChannelUsers: cloneChannelUsers(snapshot.ChannelUsers),
	}
	seenUsers := map[string]struct{}{snapshot.CurrentUser.ID: {}}
	for _, user := range snapshot.Users {
		if _, ok := seenUsers[user.ID]; ok {
			continue
		}
		seenUsers[user.ID] = struct{}{}
		raw.Users = append(raw.Users, cacheUser(user))
	}
	for _, team := range snapshot.Teams {
		raw.Teams = append(raw.Teams, cache.MattermostTeam{ID: team.ID, Name: team.Name, DisplayName: team.DisplayName, UpdatedAt: team.UpdatedAt})
	}
	seenChannels := map[string]struct{}{}
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			channel := entry.Channel
			if _, ok := seenChannels[channel.ID]; ok {
				continue
			}
			seenChannels[channel.ID] = struct{}{}
			raw.Channels = append(raw.Channels, cache.MattermostChannel{ID: channel.ID, TeamID: channel.TeamID, Name: channel.Name, DisplayName: channel.DisplayName, Kind: channel.Kind.String(), TotalMsgCount: channel.TotalMsgCount, LastPostAt: channel.LastPostAt, UpdatedAt: channel.UpdatedAt, DeletedAt: channel.DeletedAt})
			if entry.Membership != nil {
				membership := entry.Membership
				raw.Memberships = append(raw.Memberships, cache.MattermostChannelMembership{ChannelID: membership.ChannelID, UserID: membership.UserID, MsgCount: membership.MsgCount, MentionCount: membership.MentionCount, LastViewedAt: membership.LastViewedAt, UpdatedAt: membership.UpdatedAt})
			}
		}
	}
	return raw
}

func mattermostServiceSnapshot(raw cache.MattermostBootstrapSnapshot) (service.ServerSnapshot, error) {
	server := mattermost.Server{ID: raw.Server.ID, Name: raw.Server.Name, URL: raw.Server.URL, UserID: raw.Server.CurrentUserID}
	currentUser := domainUser(server.ID, raw.CurrentUser)
	users := make([]mattermost.User, 0, len(raw.Users)+1)
	usersByID := map[string]mattermost.User{currentUser.ID: currentUser}
	for _, rawUser := range raw.Users {
		user := domainUser(server.ID, rawUser)
		usersByID[user.ID] = user
	}
	for _, user := range usersByID {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	teams := make([]mattermost.Team, len(raw.Teams))
	for i, team := range raw.Teams {
		teams[i] = mattermost.Team{ID: team.ID, ServerID: server.ID, Name: team.Name, DisplayName: team.DisplayName, UpdatedAt: team.UpdatedAt}
	}
	channels := make([]mattermost.Channel, len(raw.Channels))
	for i, channel := range raw.Channels {
		kind, err := cacheChannelKind(channel.Kind)
		if err != nil {
			return service.ServerSnapshot{}, err
		}
		channels[i] = mattermost.Channel{ID: channel.ID, ServerID: server.ID, TeamID: channel.TeamID, Name: channel.Name, DisplayName: channel.DisplayName, Kind: kind, TotalMsgCount: channel.TotalMsgCount, LastPostAt: channel.LastPostAt, UpdatedAt: channel.UpdatedAt, DeletedAt: channel.DeletedAt}
	}
	memberships := make(map[string]mattermost.ChannelMembership, len(raw.Memberships))
	for _, membership := range raw.Memberships {
		memberships[membership.ChannelID] = mattermost.ChannelMembership{ChannelID: membership.ChannelID, UserID: membership.UserID, MsgCount: membership.MsgCount, MentionCount: membership.MentionCount, LastViewedAt: membership.LastViewedAt, UpdatedAt: membership.UpdatedAt}
	}
	sections, err := service.BuildChannelSections(server.ID, currentUser, teams, channels, memberships, usersByID, raw.ChannelUsers)
	if err != nil {
		return service.ServerSnapshot{}, err
	}
	return service.ServerSnapshot{Server: server, CurrentUser: currentUser, Users: users, Teams: teams, Sections: sections, ChannelUsers: cloneChannelUsers(raw.ChannelUsers)}, nil
}

func cacheUser(user mattermost.User) cache.MattermostUser {
	return cache.MattermostUser{ID: user.ID, Username: user.Username, Nickname: user.Nickname, FirstName: user.FirstName, LastName: user.LastName, UpdatedAt: user.UpdatedAt}
}

func domainUser(serverID string, user cache.MattermostUser) mattermost.User {
	return mattermost.User{ID: user.ID, ServerID: serverID, Username: user.Username, Nickname: user.Nickname, FirstName: user.FirstName, LastName: user.LastName, UpdatedAt: user.UpdatedAt}
}

func cacheChannelKind(kind string) (mattermost.ChannelKind, error) {
	switch kind {
	case "public":
		return mattermost.ChannelKindPublic, nil
	case "private":
		return mattermost.ChannelKindPrivate, nil
	case "direct":
		return mattermost.ChannelKindDirect, nil
	case "group":
		return mattermost.ChannelKindGroup, nil
	default:
		return mattermost.ChannelKindUnknown, fmt.Errorf("unknown cached Mattermost channel kind %q", kind)
	}
}

func cloneChannelUsers(source map[string][]string) map[string][]string {
	out := make(map[string][]string, len(source))
	for channelID, userIDs := range source {
		out[channelID] = append([]string(nil), userIDs...)
	}
	return out
}
