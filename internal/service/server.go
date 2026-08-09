package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/nosovk/mmk/internal/mattermost"
)

type ServerBootstrapClient interface {
	CurrentUser(context.Context) (*mattermost.User, error)
	TeamsForUser(context.Context, string) ([]mattermost.Team, error)
	ChannelsForUser(context.Context, string) ([]mattermost.Channel, error)
	ChannelMembershipsForUser(context.Context, string, string) ([]mattermost.ChannelMembership, error)
	UsersByIDs(context.Context, []string) ([]mattermost.User, error)
	UsersByGroupChannelIDs(context.Context, []string) (map[string][]mattermost.User, error)
}

var _ ServerBootstrapClient = (*mattermost.Client)(nil)

type ServerSnapshot struct {
	Server      mattermost.Server
	CurrentUser mattermost.User
	Teams       []mattermost.Team
	Sections    []ChannelSection
}

func BootstrapServer(ctx context.Context, client ServerBootstrapClient, server mattermost.Server) (ServerSnapshot, error) {
	if isNilInterface(client) {
		return ServerSnapshot{}, errors.New("Mattermost bootstrap client must not be nil")
	}
	if strings.TrimSpace(server.ID) == "" {
		return ServerSnapshot{}, errors.New("Mattermost server ID must not be empty")
	}
	if strings.TrimSpace(server.URL) == "" {
		return ServerSnapshot{}, fmt.Errorf("Mattermost server %q URL must not be empty", server.ID)
	}
	if _, err := mattermost.CanonicalServerRoot(server.URL); err != nil {
		return ServerSnapshot{}, fmt.Errorf("validate Mattermost server %q URL: %w", server.ID, err)
	}

	currentUser, err := client.CurrentUser(ctx)
	if err != nil {
		return ServerSnapshot{}, fmt.Errorf("fetch current user for Mattermost server %q: %w", server.ID, err)
	}
	if currentUser == nil || strings.TrimSpace(currentUser.ID) == "" {
		return ServerSnapshot{}, fmt.Errorf("Mattermost server %q returned an empty current user ID", server.ID)
	}
	if server.UserID != "" && server.UserID != currentUser.ID {
		return ServerSnapshot{}, fmt.Errorf("Mattermost server %q configured user %q does not match authenticated user %q", server.ID, server.UserID, currentUser.ID)
	}

	teams, err := client.TeamsForUser(ctx, currentUser.ID)
	if err != nil {
		return ServerSnapshot{}, fmt.Errorf("fetch teams for Mattermost server %q user %q: %w", server.ID, currentUser.ID, err)
	}
	channels, err := client.ChannelsForUser(ctx, currentUser.ID)
	if err != nil {
		return ServerSnapshot{}, fmt.Errorf("fetch channels for Mattermost server %q user %q: %w", server.ID, currentUser.ID, err)
	}

	teams = append([]mattermost.Team(nil), teams...)
	for i := range teams {
		teams[i].ServerID = server.ID
	}
	sort.SliceStable(teams, func(i, j int) bool {
		return compareFoldedFields(
			[]string{teams[i].DisplayName, teams[i].Name, teams[i].ID},
			[]string{teams[j].DisplayName, teams[j].Name, teams[j].ID},
		) < 0
	})

	memberships := make(map[string]mattermost.ChannelMembership)
	for _, team := range teams {
		teamMemberships, err := client.ChannelMembershipsForUser(ctx, currentUser.ID, team.ID)
		if err != nil {
			return ServerSnapshot{}, fmt.Errorf("fetch channel memberships for Mattermost server %q team %q (%s): %w", server.ID, team.ID, teamLabel(team), err)
		}
		for _, membership := range teamMemberships {
			memberships[membership.ChannelID] = membership
		}
	}

	channels = append([]mattermost.Channel(nil), channels...)
	for i := range channels {
		channels[i].ServerID = server.ID
	}
	sections, err := buildChannelSections(ctx, client, server.ID, *currentUser, teams, channels, memberships)
	if err != nil {
		return ServerSnapshot{}, err
	}

	return ServerSnapshot{
		Server:      server,
		CurrentUser: *currentUser,
		Teams:       teams,
		Sections:    sections,
	}, nil
}

func isNilInterface(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}

func compareFoldedFields(left, right []string) int {
	for i := range left {
		leftFolded := strings.ToLower(left[i])
		rightFolded := strings.ToLower(right[i])
		if leftFolded < rightFolded {
			return -1
		}
		if leftFolded > rightFolded {
			return 1
		}
	}
	return 0
}

func teamLabel(team mattermost.Team) string {
	if team.DisplayName != "" {
		return team.DisplayName
	}
	if team.Name != "" {
		return team.Name
	}
	return team.ID
}
