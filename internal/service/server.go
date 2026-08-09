package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"

	"github.com/nosovk/mmk/internal/mattermost"
)

const membershipConcurrency = 4

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
	snapshotServer := server
	snapshotServer.UserID = currentUser.ID
	snapshotUser := *currentUser
	snapshotUser.ServerID = server.ID

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

	memberships, err := fetchTeamMemberships(ctx, client, server.ID, currentUser.ID, teams)
	if err != nil {
		return ServerSnapshot{}, err
	}

	channels = append([]mattermost.Channel(nil), channels...)
	for i := range channels {
		channels[i].ServerID = server.ID
	}
	sections, err := buildChannelSections(ctx, client, server.ID, snapshotUser, teams, channels, memberships)
	if err != nil {
		return ServerSnapshot{}, err
	}

	return ServerSnapshot{
		Server:      snapshotServer,
		CurrentUser: snapshotUser,
		Teams:       teams,
		Sections:    sections,
	}, nil
}

func fetchTeamMemberships(ctx context.Context, client ServerBootstrapClient, serverID, userID string, teams []mattermost.Team) (map[string]mattermost.ChannelMembership, error) {
	type result struct {
		index       int
		memberships []mattermost.ChannelMembership
		err         error
	}
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	jobs := make(chan int)
	results := make(chan result, min(len(teams), membershipConcurrency))
	var workers sync.WaitGroup
	var firstErr error
	var firstErrOnce sync.Once
	for range min(len(teams), membershipConcurrency) {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for index := range jobs {
				team := teams[index]
				memberships, err := client.ChannelMembershipsForUser(workerCtx, userID, team.ID)
				if err != nil {
					err = fmt.Errorf("fetch channel memberships for Mattermost server %q team %q (%s): %w", serverID, team.ID, teamLabel(team), err)
					firstErrOnce.Do(func() { firstErr = err })
					cancel()
				}
				results <- result{index: index, memberships: memberships, err: err}
			}
		}()
	}
	go func() {
		defer close(jobs)
		for index := range teams {
			select {
			case jobs <- index:
			case <-workerCtx.Done():
				return
			}
		}
	}()
	go func() {
		workers.Wait()
		close(results)
	}()

	byTeam := make([][]mattermost.ChannelMembership, len(teams))
	for result := range results {
		if result.err != nil {
			continue
		}
		byTeam[result.index] = result.memberships
	}
	if firstErr != nil {
		return nil, firstErr
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	memberships := make(map[string]mattermost.ChannelMembership)
	for _, teamMemberships := range byTeam {
		for _, membership := range teamMemberships {
			memberships[membership.ChannelID] = membership
		}
	}
	return memberships, nil
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
