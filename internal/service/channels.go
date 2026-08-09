package service

import (
	"context"
	"fmt"
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

func buildChannelSections(
	ctx context.Context,
	client ServerBootstrapClient,
	serverID string,
	currentUser mattermost.User,
	teams []mattermost.Team,
	channels []mattermost.Channel,
	memberships map[string]mattermost.ChannelMembership,
) ([]ChannelSection, error) {
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
			return nil, fmt.Errorf("fetch direct-message users for Mattermost server %q: %w", serverID, err)
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
			return nil, fmt.Errorf("fetch group-message users for Mattermost server %q: %w", serverID, err)
		}
	}

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
			entry.DisplayName = directDisplayName(channel, directPeerByChannel[channel.ID], usersByID)
			directEntries = append(directEntries, entry)
		case mattermost.ChannelKindGroup:
			entry.DisplayName = groupDisplayName(channel, currentUser.ID, groupUsers[channel.ID])
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

func directPeerID(name, currentUserID string) (string, bool) {
	parts := strings.Split(name, "__")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
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
