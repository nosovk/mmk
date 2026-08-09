package service

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/mattermost"
)

func TestServerBootstrapValidatesInputsWithoutCallingClient(t *testing.T) {
	client := &fakeServerBootstrapClient{}
	for _, tt := range []struct {
		name   string
		client ServerBootstrapClient
		server mattermost.Server
	}{
		{name: "nil client", server: mattermost.Server{ID: "server-1", URL: "https://chat.example.com"}},
		{name: "empty server ID", client: client, server: mattermost.Server{URL: "https://chat.example.com"}},
		{name: "empty server URL", client: client, server: mattermost.Server{ID: "server-1"}},
		{name: "invalid server URL", client: client, server: mattermost.Server{ID: "server-1", URL: "://bad"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := BootstrapServer(context.Background(), tt.client, tt.server); err == nil {
				t.Fatal("BootstrapServer accepted invalid input")
			}
		})
	}
	if client.currentUserCalls != 0 {
		t.Fatalf("CurrentUser calls = %d, want 0", client.currentUserCalls)
	}
}

func TestServerBootstrapRequiresAuthoritativeCurrentUserID(t *testing.T) {
	client := &fakeServerBootstrapClient{currentUser: &mattermost.User{Username: "alice"}}
	_, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err == nil || !strings.Contains(err.Error(), "server-1") || !strings.Contains(err.Error(), "current user ID") {
		t.Fatalf("error = %v, want contextual empty current user ID error", err)
	}
	if client.teamsCalls != 0 || client.channelsCalls != 0 {
		t.Fatal("BootstrapServer continued after an invalid current user")
	}
}

func TestServerBootstrapRejectsConfiguredIdentityMismatch(t *testing.T) {
	client := &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "actual-user"}}
	server := mattermost.Server{ID: "server-1", URL: "https://chat.example.com", UserID: "configured-user"}
	_, err := BootstrapServer(context.Background(), client, server)
	if err == nil || !strings.Contains(err.Error(), "server-1") || !strings.Contains(err.Error(), "configured-user") || !strings.Contains(err.Error(), "actual-user") {
		t.Fatalf("error = %v, want contextual identity mismatch", err)
	}
}

func TestServerBootstrapScopesCopiedSnapshotIdentity(t *testing.T) {
	clientUser := &mattermost.User{ID: "user-1", ServerID: "client-server", Nickname: "Alice"}
	inputServer := mattermost.Server{ID: "server-1", URL: "https://chat.example.com"}
	client := &fakeServerBootstrapClient{
		currentUser: clientUser,
		memberships: map[string][]mattermost.ChannelMembership{},
	}

	snapshot, err := BootstrapServer(context.Background(), client, inputServer)
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if got, want := snapshot.CurrentUser.ServerID, inputServer.ID; got != want {
		t.Fatalf("snapshot current user ServerID = %q, want %q", got, want)
	}
	if got, want := snapshot.Server.UserID, clientUser.ID; got != want {
		t.Fatalf("snapshot server UserID = %q, want %q", got, want)
	}
	if clientUser.ServerID != "client-server" {
		t.Fatalf("client user mutated: %#v", clientUser)
	}
	if inputServer.UserID != "" {
		t.Fatalf("input server mutated: %#v", inputServer)
	}
}

func TestServerBootstrapReturnsSortedSnapshotWithoutMutatingClientValues(t *testing.T) {
	teams := []mattermost.Team{
		{ID: "team-z", Name: "zeta", DisplayName: "beta"},
		{ID: "team-a", Name: "alpha", DisplayName: "Alpha"},
		{ID: "team-b", Name: "beta", DisplayName: "alpha"},
	}
	channels := []mattermost.Channel{
		{ID: "channel-z", TeamID: "team-z", Name: "zeta", DisplayName: "Zeta", Kind: mattermost.ChannelKindPublic},
		{ID: "channel-a", TeamID: "team-a", Name: "alpha", DisplayName: "Alpha", Kind: mattermost.ChannelKindPrivate},
	}
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "user-1", Nickname: "Alice"},
		teams:       teams,
		channels:    channels,
		memberships: map[string][]mattermost.ChannelMembership{
			"team-a": {{ChannelID: "channel-a", UserID: "user-1", MentionCount: 2}},
		},
	}
	server := mattermost.Server{ID: "server-1", Name: "Example", URL: "https://chat.example.com", UserID: "user-1"}

	snapshot, err := BootstrapServer(context.Background(), client, server)
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if snapshot.Server != server || snapshot.CurrentUser.ID != "user-1" {
		t.Fatalf("snapshot identity = %#v / %#v", snapshot.Server, snapshot.CurrentUser)
	}
	if got, want := teamIDs(snapshot.Teams), []string{"team-a", "team-b", "team-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("team IDs = %#v, want %#v", got, want)
	}
	for _, team := range snapshot.Teams {
		if team.ServerID != server.ID {
			t.Fatalf("team %q ServerID = %q, want %q", team.ID, team.ServerID, server.ID)
		}
	}
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			if entry.Channel.ServerID != server.ID {
				t.Fatalf("channel %q ServerID = %q, want %q", entry.Channel.ID, entry.Channel.ServerID, server.ID)
			}
		}
	}
	if teams[0].ServerID != "" || channels[0].ServerID != "" {
		t.Fatal("BootstrapServer mutated client-owned input slices")
	}
	gotMembershipTeams := append([]string(nil), client.membershipTeamIDs...)
	sort.Strings(gotMembershipTeams)
	if got, want := gotMembershipTeams, []string{"team-a", "team-b", "team-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("membership team calls = %#v, want %#v", got, want)
	}
	if got := snapshot.Sections[0].Channels[0].Membership; got == nil || got.MentionCount != 2 {
		t.Fatalf("channel membership = %#v, want copied membership", got)
	}
	client.memberships["team-a"][0].MentionCount = 99
	if got := snapshot.Sections[0].Channels[0].Membership.MentionCount; got != 2 {
		t.Fatalf("snapshot membership changed through client alias: %d", got)
	}
}

func TestServerBootstrapWrapsRequiredCallErrorsWithServerAndTeamContext(t *testing.T) {
	sentinel := errors.New("sentinel")
	for _, tt := range []struct {
		name   string
		client *fakeServerBootstrapClient
		want   []string
	}{
		{name: "current user", client: &fakeServerBootstrapClient{currentUserErr: sentinel}, want: []string{"server-1", "current user"}},
		{name: "teams", client: &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "user-1"}, teamsErr: sentinel}, want: []string{"server-1", "teams"}},
		{name: "channels", client: &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "user-1"}, channelsErr: sentinel}, want: []string{"server-1", "channels"}},
		{name: "memberships", client: &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "user-1"}, teams: []mattermost.Team{{ID: "team-1", Name: "engineering"}}, membershipErr: map[string]error{"team-1": sentinel}}, want: []string{"server-1", "team-1", "engineering", "memberships"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BootstrapServer(context.Background(), tt.client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
			if !errors.Is(err, sentinel) {
				t.Fatalf("error = %v, want sentinel", err)
			}
			for _, part := range tt.want {
				if !strings.Contains(err.Error(), part) {
					t.Fatalf("error = %q, want %q", err, part)
				}
			}
		})
	}
}

func TestServerBootstrapCallsAreStatelessAndConcurrent(t *testing.T) {
	makeClient := func(userID, teamID string) *fakeServerBootstrapClient {
		return &fakeServerBootstrapClient{
			currentUser: &mattermost.User{ID: userID},
			teams:       []mattermost.Team{{ID: teamID, DisplayName: teamID}},
			memberships: map[string][]mattermost.ChannelMembership{},
		}
	}
	type result struct {
		snapshot ServerSnapshot
		err      error
	}
	results := make(chan result, 2)
	var wg sync.WaitGroup
	for _, tt := range []struct {
		server mattermost.Server
		client *fakeServerBootstrapClient
	}{
		{server: mattermost.Server{ID: "server-a", URL: "https://a.example.com"}, client: makeClient("user-a", "team-a")},
		{server: mattermost.Server{ID: "server-b", URL: "https://b.example.com"}, client: makeClient("user-b", "team-b")},
	} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			snapshot, err := BootstrapServer(context.Background(), tt.client, tt.server)
			results <- result{snapshot: snapshot, err: err}
		}()
	}
	wg.Wait()
	close(results)

	seen := map[string]ServerSnapshot{}
	for result := range results {
		if result.err != nil {
			t.Fatalf("BootstrapServer returned error: %v", result.err)
		}
		seen[result.snapshot.Server.ID] = result.snapshot
	}
	for serverID, wantTeamID := range map[string]string{"server-a": "team-a", "server-b": "team-b"} {
		snapshot := seen[serverID]
		if got := snapshot.Teams[0]; got.ID != wantTeamID || got.ServerID != serverID {
			t.Fatalf("snapshot %q team = %#v", serverID, got)
		}
	}
}

type fakeServerBootstrapClient struct {
	mu                sync.Mutex
	currentUser       *mattermost.User
	currentUserErr    error
	teams             []mattermost.Team
	teamsErr          error
	channels          []mattermost.Channel
	channelsErr       error
	memberships       map[string][]mattermost.ChannelMembership
	membershipErr     map[string]error
	users             []mattermost.User
	usersErr          error
	groupUsers        map[string][]mattermost.User
	groupUsersErr     error
	currentUserCalls  int
	teamsCalls        int
	channelsCalls     int
	membershipTeamIDs []string
	userIDRequests    [][]string
	groupIDRequests   [][]string
	membershipHook    func(context.Context, string) ([]mattermost.ChannelMembership, error)
}

func (f *fakeServerBootstrapClient) CurrentUser(context.Context) (*mattermost.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentUserCalls++
	return f.currentUser, f.currentUserErr
}

func (f *fakeServerBootstrapClient) TeamsForUser(context.Context, string) ([]mattermost.Team, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.teamsCalls++
	return f.teams, f.teamsErr
}

func (f *fakeServerBootstrapClient) ChannelsForUser(context.Context, string) ([]mattermost.Channel, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.channelsCalls++
	return f.channels, f.channelsErr
}

func (f *fakeServerBootstrapClient) ChannelMembershipsForUser(ctx context.Context, _ string, teamID string) ([]mattermost.ChannelMembership, error) {
	f.mu.Lock()
	f.membershipTeamIDs = append(f.membershipTeamIDs, teamID)
	hook := f.membershipHook
	f.mu.Unlock()
	if hook != nil {
		return hook(ctx, teamID)
	}
	return f.memberships[teamID], f.membershipErr[teamID]
}

func (f *fakeServerBootstrapClient) UsersByIDs(_ context.Context, ids []string) ([]mattermost.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.userIDRequests = append(f.userIDRequests, append([]string(nil), ids...))
	return f.users, f.usersErr
}

func (f *fakeServerBootstrapClient) UsersByGroupChannelIDs(_ context.Context, ids []string) (map[string][]mattermost.User, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.groupIDRequests = append(f.groupIDRequests, append([]string(nil), ids...))
	return f.groupUsers, f.groupUsersErr
}

func TestServerBootstrapFetchesMembershipsWithBoundedConcurrency(t *testing.T) {
	const teamCount = 8
	teams := make([]mattermost.Team, teamCount)
	for i := range teams {
		teams[i] = mattermost.Team{ID: fmt.Sprintf("team-%d", i), DisplayName: fmt.Sprintf("Team %d", i)}
	}
	started := make(chan struct{}, teamCount)
	release := make(chan struct{})
	var mu sync.Mutex
	active := 0
	maximum := 0
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "user-1"},
		teams:       teams,
		memberships: map[string][]mattermost.ChannelMembership{},
		membershipHook: func(ctx context.Context, _ string) ([]mattermost.ChannelMembership, error) {
			mu.Lock()
			active++
			maximum = max(maximum, active)
			mu.Unlock()
			started <- struct{}{}
			select {
			case <-release:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			mu.Lock()
			active--
			mu.Unlock()
			return nil, nil
		},
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		_, err := BootstrapServer(ctx, client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
		done <- err
	}()
	for range 4 {
		select {
		case <-started:
		case <-time.After(time.Second):
			cancel()
			close(release)
			<-done
			t.Fatal("fewer than four membership calls ran concurrently")
		}
	}
	select {
	case <-started:
		t.Fatal("membership concurrency exceeded four")
	default:
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if maximum != 4 {
		t.Fatalf("maximum membership concurrency = %d, want 4", maximum)
	}
	if active != 0 {
		t.Fatalf("active membership calls after return = %d, want 0", active)
	}
}

func TestServerBootstrapMembershipResultsAreDeterministicAcrossCompletionOrder(t *testing.T) {
	teams := []mattermost.Team{{ID: "team-a"}, {ID: "team-b"}, {ID: "team-c"}, {ID: "team-d"}}
	channels := make([]mattermost.Channel, len(teams))
	gates := make(map[string]chan struct{}, len(teams))
	started := make(chan string, len(teams))
	for i, team := range teams {
		channels[i] = mattermost.Channel{ID: "channel-" + team.ID, TeamID: team.ID, Kind: mattermost.ChannelKindPublic}
		gates[team.ID] = make(chan struct{})
	}
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "user-1"},
		teams:       teams,
		channels:    channels,
		memberships: map[string][]mattermost.ChannelMembership{},
		membershipHook: func(_ context.Context, teamID string) ([]mattermost.ChannelMembership, error) {
			started <- teamID
			<-gates[teamID]
			return []mattermost.ChannelMembership{{ChannelID: "channel-" + teamID, MentionCount: int64(teamID[len(teamID)-1])}}, nil
		},
	}
	result := make(chan struct {
		snapshot ServerSnapshot
		err      error
	}, 1)
	go func() {
		snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
		result <- struct {
			snapshot ServerSnapshot
			err      error
		}{snapshot: snapshot, err: err}
	}()
	for range teams {
		<-started
	}
	for i := len(teams) - 1; i >= 0; i-- {
		close(gates[teams[i].ID])
	}
	got := <-result
	if got.err != nil {
		t.Fatalf("BootstrapServer returned error: %v", got.err)
	}
	for _, section := range got.snapshot.Sections {
		entry := section.Channels[0]
		if entry.Membership == nil || entry.Membership.ChannelID != entry.Channel.ID {
			t.Fatalf("section %q membership = %#v for channel %#v", section.ID, entry.Membership, entry.Channel)
		}
	}
}

func TestServerBootstrapCancelsMembershipWorkersOnFirstError(t *testing.T) {
	teams := make([]mattermost.Team, 20)
	for i := range teams {
		teams[i] = mattermost.Team{ID: fmt.Sprintf("team-%02d", i), Name: fmt.Sprintf("name-%02d", i)}
	}
	started := make(chan string, len(teams))
	fail := make(chan struct{})
	var mu sync.Mutex
	active := 0
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "user-1"},
		teams:       teams,
		memberships: map[string][]mattermost.ChannelMembership{},
		membershipHook: func(ctx context.Context, teamID string) ([]mattermost.ChannelMembership, error) {
			mu.Lock()
			active++
			mu.Unlock()
			defer func() {
				mu.Lock()
				active--
				mu.Unlock()
			}()
			started <- teamID
			if teamID == "team-00" {
				<-fail
				return nil, errors.New("membership failure")
			}
			<-ctx.Done()
			return nil, ctx.Err()
		},
	}
	done := make(chan error, 1)
	go func() {
		_, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
		done <- err
	}()
	for range 4 {
		<-started
	}
	close(fail)
	err := <-done
	if err == nil || !strings.Contains(err.Error(), "team-00") || !strings.Contains(err.Error(), "name-00") {
		t.Fatalf("error = %v, want first team context", err)
	}
	if got := len(started); got != 0 {
		t.Fatalf("started %d additional membership calls after cancellation", got)
	}
	mu.Lock()
	defer mu.Unlock()
	if active != 0 {
		t.Fatalf("active membership calls after return = %d, want 0", active)
	}
}

func teamIDs(teams []mattermost.Team) []string {
	ids := make([]string, len(teams))
	for i := range teams {
		ids[i] = teams[i].ID
	}
	return ids
}
