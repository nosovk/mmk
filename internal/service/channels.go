package service

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/nosovk/mmk/internal/mattermost"
)

type ChannelSectionKind uint8

const (
	ChannelSectionKindUnknown ChannelSectionKind = iota
	ChannelSectionKindDirect
	ChannelSectionKindTeam
)

type ChannelSection struct {
	ID       string
	Name     string
	TeamID   string
	Kind     ChannelSectionKind
	Channels []ChannelEntry
}

type ChannelEntry struct {
	Channel     mattermost.Channel
	DisplayName string
	Membership  *mattermost.ChannelMembership
}

func ChannelHasUnread(entry ChannelEntry) bool {
	return entry.Membership != nil && (entry.Membership.MentionCount > 0 || entry.Channel.TotalMsgCount > entry.Membership.MsgCount)
}

func ChannelWithNewPost(entry ChannelEntry, activelyViewed bool) ChannelEntry {
	if activelyViewed && entry.Membership != nil {
		// This is an optimistic runtime snapshot advance, not an authoritative membership response.
		if entry.Channel.TotalMsgCount < math.MaxInt64 && entry.Membership.MsgCount < math.MaxInt64 {
			membership := *entry.Membership
			entry.Channel.TotalMsgCount++
			membership.MsgCount++
			entry.Membership = &membership
		}
		return entry
	}
	if entry.Channel.TotalMsgCount < math.MaxInt64 {
		entry.Channel.TotalMsgCount++
	}
	return entry
}

func ChannelViewed(entry ChannelEntry) ChannelEntry {
	if entry.Membership == nil {
		return entry
	}
	membership := *entry.Membership
	membership.MsgCount = entry.Channel.TotalMsgCount
	membership.MentionCount = 0
	entry.Membership = &membership
	return entry
}

func buildChannelSections(
	ctx context.Context,
	client ServerBootstrapClient,
	serverID string,
	currentUser mattermost.User,
	teams []mattermost.Team,
	channels []mattermost.Channel,
	memberships map[string]mattermost.ChannelMembership,
) ([]ChannelSection, []mattermost.User, map[string][]string, error) {
	directPeerIDs := make([]string, 0)
	directPeerByChannel := make(map[string]string)
	groupChannelIDs := make([]string, 0)
	seenPeers := make(map[string]struct{})
	for _, channel := range channels {
		switch channel.Kind {
		case mattermost.ChannelKindDirect:
			peerID, ok := directPeerID(channel.Name, currentUser.ID)
			if !ok {
				continue
			}
			directPeerByChannel[channel.ID] = peerID
			if peerID == currentUser.ID {
				continue
			}
			if _, seen := seenPeers[peerID]; !seen {
				seenPeers[peerID] = struct{}{}
				directPeerIDs = append(directPeerIDs, peerID)
			}
		case mattermost.ChannelKindGroup:
			groupChannelIDs = append(groupChannelIDs, channel.ID)
		}
	}

	usersByID := make(map[string]mattermost.User, len(directPeerIDs)+1)
	usersByID[currentUser.ID] = currentUser
	if len(directPeerIDs) > 0 {
		users, err := client.UsersByIDs(ctx, directPeerIDs)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fetch direct-message users for Mattermost server %q: %w", serverID, err)
		}
		for _, user := range users {
			if _, exists := usersByID[user.ID]; !exists {
				usersByID[user.ID] = user
			}
		}
	}

	groupUsers := map[string][]mattermost.User{}
	if len(groupChannelIDs) > 0 {
		var err error
		groupUsers, err = client.UsersByGroupChannelIDs(ctx, groupChannelIDs)
		if err != nil {
			return nil, nil, nil, fmt.Errorf("fetch group-message users for Mattermost server %q: %w", serverID, err)
		}
	}

	allUsers := make(map[string]mattermost.User, len(usersByID))
	for id, user := range usersByID {
		user.ServerID = serverID
		allUsers[id] = user
	}
	channelUsers := make(map[string][]string)
	for channelID, users := range groupUsers {
		seen := map[string]struct{}{}
		for _, user := range users {
			if _, ok := seen[user.ID]; ok || user.ID == "" {
				continue
			}
			seen[user.ID] = struct{}{}
			channelUsers[channelID] = append(channelUsers[channelID], user.ID)
			user.ServerID = serverID
			if _, exists := allUsers[user.ID]; !exists {
				allUsers[user.ID] = user
			}
		}
	}
	for channelID, peerID := range directPeerByChannel {
		channelUsers[channelID] = dedupeIDs([]string{currentUser.ID, peerID})
	}
	sections, err := BuildChannelSections(serverID, currentUser, teams, channels, memberships, allUsers, channelUsers)
	if err != nil {
		return nil, nil, nil, err
	}
	for i := range sections {
		for j := range sections[i].Channels {
			entry := &sections[i].Channels[j]
			if entry.Channel.Kind == mattermost.ChannelKindGroup {
				entry.DisplayName = groupDisplayName(entry.Channel, currentUser.ID, groupUsers[entry.Channel.ID])
			}
		}
	}
	users := make([]mattermost.User, 0, len(allUsers))
	for _, user := range allUsers {
		users = append(users, user)
	}
	sort.Slice(users, func(i, j int) bool { return users[i].ID < users[j].ID })
	return sections, users, channelUsers, nil
}

func BuildChannelSections(serverID string, currentUser mattermost.User, teams []mattermost.Team, channels []mattermost.Channel, memberships map[string]mattermost.ChannelMembership, usersByID map[string]mattermost.User, channelUsers map[string][]string) ([]ChannelSection, error) {
	teamSections := make(map[string]*ChannelSection, len(teams))
	sections := make([]ChannelSection, 0, len(teams)+1)
	for _, team := range teams {
		sections = append(sections, ChannelSection{
			ID:       "team:" + team.ID,
			Name:     teamLabel(team),
			TeamID:   team.ID,
			Kind:     ChannelSectionKindTeam,
			Channels: []ChannelEntry{},
		})
		teamSections[team.ID] = &sections[len(sections)-1]
	}
	directEntries := make([]ChannelEntry, 0)

	for _, channel := range channels {
		entry := ChannelEntry{Channel: channel}
		if membership, ok := memberships[channel.ID]; ok {
			membershipCopy := membership
			entry.Membership = &membershipCopy
		}

		switch channel.Kind {
		case mattermost.ChannelKindDirect:
			peerID := ""
			for _, userID := range channelUsers[channel.ID] {
				if userID != currentUser.ID || len(channelUsers[channel.ID]) == 1 {
					peerID = userID
					break
				}
			}
			entry.DisplayName = directDisplayName(channel, peerID, usersByID)
			directEntries = append(directEntries, entry)
		case mattermost.ChannelKindGroup:
			participants := make([]mattermost.User, 0, len(channelUsers[channel.ID]))
			for _, userID := range channelUsers[channel.ID] {
				if user, ok := usersByID[userID]; ok {
					participants = append(participants, user)
				}
			}
			entry.DisplayName = groupDisplayName(channel, currentUser.ID, participants)
			directEntries = append(directEntries, entry)
		case mattermost.ChannelKindPublic, mattermost.ChannelKindPrivate:
			section := teamSections[channel.TeamID]
			if section == nil {
				return nil, fmt.Errorf("Mattermost server %q channel %q (%s) references unknown team %q", serverID, channel.ID, channelLabel(channel), channel.TeamID)
			}
			entry.DisplayName = channelLabel(channel)
			section.Channels = append(section.Channels, entry)
		default:
			return nil, fmt.Errorf("Mattermost server %q channel %q has unsupported kind %s", serverID, channel.ID, channel.Kind)
		}
	}

	for i := range sections {
		sortChannelEntries(sections[i].Channels)
	}
	if len(directEntries) > 0 {
		sortChannelEntries(directEntries)
		directSection := ChannelSection{
			ID:       "server:" + serverID + ":direct",
			Name:     "Direct Messages",
			Kind:     ChannelSectionKindDirect,
			Channels: directEntries,
		}
		sections = append([]ChannelSection{directSection}, sections...)
	}
	return sections, nil
}

func dedupeIDs(ids []string) []string {
	seen := map[string]struct{}{}
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		if id == "" {
			continue
		}
		if _, ok := seen[id]; ok {
			continue
		}
		seen[id] = struct{}{}
		out = append(out, id)
	}
	return out
}

func directPeerID(name, currentUserID string) (string, bool) {
	parts := strings.Split(name, "__")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", false
	}
	if !safeLookupID(parts[0]) || !safeLookupID(parts[1]) {
		return "", false
	}
	if parts[0] == currentUserID {
		return parts[1], true
	}
	if parts[1] == currentUserID {
		return parts[0], true
	}
	return "", false
}

func safeLookupID(id string) bool {
	if len(id) == 0 || len(id) > 128 {
		return false
	}
	for _, char := range []byte(id) {
		if char >= 'a' && char <= 'z' || char >= 'A' && char <= 'Z' || char >= '0' && char <= '9' || char == '_' || char == '-' {
			continue
		}
		return false
	}
	return true
}

func directDisplayName(channel mattermost.Channel, peerID string, usersByID map[string]mattermost.User) string {
	if user, ok := usersByID[peerID]; ok {
		if name := strings.TrimSpace(user.DisplayName()); name != "" {
			return name
		}
	}
	if channel.DisplayName != "" {
		return channel.DisplayName
	}
	if peerID != "" {
		return peerID
	}
	return channelLabel(channel)
}

func groupDisplayName(channel mattermost.Channel, currentUserID string, users []mattermost.User) string {
	participants := make([]mattermost.User, 0, len(users))
	seen := make(map[string]struct{}, len(users))
	for _, user := range users {
		if user.ID == currentUserID {
			continue
		}
		if _, exists := seen[user.ID]; exists {
			continue
		}
		seen[user.ID] = struct{}{}
		participants = append(participants, user)
	}
	sort.SliceStable(participants, func(i, j int) bool {
		return compareFoldedFields(
			[]string{participants[i].DisplayName(), participants[i].ID},
			[]string{participants[j].DisplayName(), participants[j].ID},
		) < 0
	})
	names := make([]string, 0, len(participants))
	for _, participant := range participants {
		if name := strings.TrimSpace(participant.DisplayName()); name != "" {
			names = append(names, name)
		}
	}
	if len(names) > 0 {
		return strings.Join(names, ", ")
	}
	return channelLabel(channel)
}

func channelLabel(channel mattermost.Channel) string {
	if channel.DisplayName != "" {
		return channel.DisplayName
	}
	if channel.Name != "" {
		return channel.Name
	}
	return channel.ID
}

func sortChannelEntries(entries []ChannelEntry) {
	sort.SliceStable(entries, func(i, j int) bool {
		return compareFoldedFields(
			[]string{entries[i].DisplayName, entries[i].Channel.Name, entries[i].Channel.ID},
			[]string{entries[j].DisplayName, entries[j].Channel.Name, entries[j].Channel.ID},
		) < 0
	})
}
