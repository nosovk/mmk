package service

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/mattermost"
)

func TestServerBootstrapBuildsDirectThenStableTeamSections(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me", Nickname: "My Nick"},
		teams: []mattermost.Team{
			{ID: "team-2", Name: "beta", DisplayName: "Beta"},
			{ID: "team-1", Name: "alpha", DisplayName: "Alpha"},
			{ID: "team-3"},
		},
		channels: []mattermost.Channel{
			{ID: "ordinary-z", TeamID: "team-1", Name: "zeta", Kind: mattermost.ChannelKindPublic},
			{ID: "direct-z", TeamID: "team-1", Name: "me__user-z", Kind: mattermost.ChannelKindDirect},
			{ID: "group-1", TeamID: "team-2", Name: "api-group-name", Kind: mattermost.ChannelKindGroup},
			{ID: "ordinary-a", TeamID: "team-1", Name: "alpha", DisplayName: "Alpha Channel", Kind: mattermost.ChannelKindPrivate},
			{ID: "direct-a", Name: "user-a__me", Kind: mattermost.ChannelKindDirect},
		},
		memberships: map[string][]mattermost.ChannelMembership{
			"team-1": {{ChannelID: "ordinary-a", MentionCount: 3}},
		},
		users: []mattermost.User{
			{ID: "user-z", Username: "zed"},
			{ID: "user-a", Nickname: "Amy", FirstName: "Ignored"},
		},
		groupUsers: map[string][]mattermost.User{
			"group-1": {
				{ID: "user-z", Nickname: "zoe"},
				{ID: "me", Nickname: "My Nick"},
				{ID: "user-a", Nickname: "Amy"},
			},
		},
	}

	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if got, want := []string{snapshot.Sections[0].ID, snapshot.Sections[1].ID, snapshot.Sections[2].ID, snapshot.Sections[3].ID}, []string{"server:server-1:direct", "team:team-3", "team:team-1", "team:team-2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("section IDs = %#v, want %#v", got, want)
	}
	if direct := snapshot.Sections[0]; direct.Name != "Direct Messages" || direct.Kind != ChannelSectionKindDirect || direct.TeamID != "" {
		t.Fatalf("direct section = %#v", direct)
	}
	if got, want := entryNames(snapshot.Sections[0]), []string{"Amy", "Amy, zoe", "zed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("direct names = %#v, want %#v", got, want)
	}
	if got, want := channelIDs(snapshot.Sections[2]), []string{"ordinary-a", "ordinary-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("team channel IDs = %#v, want %#v", got, want)
	}
	if snapshot.Sections[2].Channels[0].Membership == nil || snapshot.Sections[2].Channels[1].Membership != nil {
		t.Fatalf("team memberships = %#v", snapshot.Sections[2].Channels)
	}
	if section := snapshot.Sections[1]; section.Name != "team-3" || len(section.Channels) != 0 || section.Kind != ChannelSectionKindTeam {
		t.Fatalf("empty fallback team section = %#v", section)
	}
	if got, want := client.userIDRequests, [][]string{{"user-z", "user-a"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user ID requests = %#v, want %#v", got, want)
	}
	if got, want := client.groupIDRequests, [][]string{{"group-1"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group ID requests = %#v, want %#v", got, want)
	}
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			if entry.Channel.DisplayName != clientChannelByID(client.channels, entry.Channel.ID).DisplayName || entry.Channel.Name != clientChannelByID(client.channels, entry.Channel.ID).Name {
				t.Fatalf("derived name mutated channel fields: %#v", entry)
			}
		}
	}
	if got, want := userIDsFromSnapshot(snapshot.Users), []string{"me", "user-a", "user-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("snapshot users = %v, want %v", got, want)
	}
	if got, want := snapshot.ChannelUsers["direct-a"], []string{"me", "user-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("direct participants = %v, want %v", got, want)
	}
	if got, want := snapshot.ChannelUsers["group-1"], []string{"user-z", "me", "user-a"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("group participants = %v, want fetched order with duplicates removed", got)
	}
}

func userIDsFromSnapshot(users []mattermost.User) []string {
	ids := make([]string, len(users))
	for i := range users {
		ids[i] = users[i].ID
	}
	return ids
}

func TestServerBootstrapSupportsSelfDMWithoutBulkLookup(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me", Nickname: "Myself"},
		channels:    []mattermost.Channel{{ID: "self", Name: "me__me", Kind: mattermost.ChannelKindDirect}},
		memberships: map[string][]mattermost.ChannelMembership{},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if got, want := snapshot.Sections[0].Channels[0].DisplayName, "Myself"; got != want {
		t.Fatalf("self-DM name = %q, want %q", got, want)
	}
	if len(client.userIDRequests) != 0 {
		t.Fatalf("self-DM made user lookup requests: %#v", client.userIDRequests)
	}
}

func TestServerBootstrapUsesDeterministicDirectMessageFallbacks(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		channels: []mattermost.Channel{
			{ID: "malformed-display", Name: "bad", DisplayName: "Existing", Kind: mattermost.ChannelKindDirect},
			{ID: "missing-user", Name: "me__peer-id", Kind: mattermost.ChannelKindDirect},
			{ID: "malformed-name", Name: "one__two__three", Kind: mattermost.ChannelKindDirect},
			{ID: "id-only", Kind: mattermost.ChannelKindDirect},
		},
		memberships: map[string][]mattermost.ChannelMembership{},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	want := map[string]string{
		"malformed-display": "Existing",
		"missing-user":      "peer-id",
		"malformed-name":    "one__two__three",
		"id-only":           "id-only",
	}
	for _, entry := range snapshot.Sections[0].Channels {
		if got := entry.DisplayName; got != want[entry.Channel.ID] {
			t.Fatalf("channel %q name = %q, want %q", entry.Channel.ID, got, want[entry.Channel.ID])
		}
	}
	if got, want := client.userIDRequests, [][]string{{"peer-id"}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("user ID requests = %#v, want %#v", got, want)
	}
}

func TestServerBootstrapDoesNotLookupUnsafeDirectMessagePeerIDs(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		channels: []mattermost.Channel{
			{ID: "slash", Name: "me__peer/id", DisplayName: "Slash Fallback", Kind: mattermost.ChannelKindDirect},
			{ID: "long", Name: "me__" + strings.Repeat("a", 129), DisplayName: "Long Fallback", Kind: mattermost.ChannelKindDirect},
		},
		memberships: map[string][]mattermost.ChannelMembership{},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if len(client.userIDRequests) != 0 {
		t.Fatalf("unsafe direct IDs were requested: %#v", client.userIDRequests)
	}
	if got, want := entryNames(snapshot.Sections[0]), []string{"Long Fallback", "Slash Fallback"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("fallback names = %#v, want %#v", got, want)
	}
}

func TestServerBootstrapUsesGroupFallbackWhenParticipantsDoNotYieldNames(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		channels: []mattermost.Channel{
			{ID: "display", DisplayName: "Existing", Name: "name", Kind: mattermost.ChannelKindGroup},
			{ID: "name", Name: "API Name", Kind: mattermost.ChannelKindGroup},
			{ID: "id", Kind: mattermost.ChannelKindGroup},
		},
		memberships: map[string][]mattermost.ChannelMembership{},
		groupUsers: map[string][]mattermost.User{
			"display": {{ID: "me"}},
			"name":    {{ID: "me"}},
			"id":      {{ID: "me"}},
		},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	want := map[string]string{"display": "Existing", "name": "API Name", "id": "id"}
	for _, entry := range snapshot.Sections[0].Channels {
		if got := entry.DisplayName; got != want[entry.Channel.ID] {
			t.Fatalf("channel %q name = %q, want %q", entry.Channel.ID, got, want[entry.Channel.ID])
		}
	}
}

func TestServerBootstrapRejectsOrdinaryChannelForUnknownTeam(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		channels:    []mattermost.Channel{{ID: "channel-1", TeamID: "missing-team", Name: "town-square", Kind: mattermost.ChannelKindPublic}},
		memberships: map[string][]mattermost.ChannelMembership{},
	}
	_, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err == nil || !strings.Contains(err.Error(), "server-1") || !strings.Contains(err.Error(), "channel-1") || !strings.Contains(err.Error(), "missing-team") {
		t.Fatalf("error = %v, want contextual unknown-team error", err)
	}
}

func TestServerBootstrapPropagatesDirectAndGroupLookupErrors(t *testing.T) {
	sentinel := errors.New("lookup failed")
	for _, tt := range []struct {
		name   string
		client *fakeServerBootstrapClient
		want   string
	}{
		{name: "direct", client: &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "me"}, channels: []mattermost.Channel{{ID: "direct", Name: "me__peer", Kind: mattermost.ChannelKindDirect}}, memberships: map[string][]mattermost.ChannelMembership{}, usersErr: sentinel}, want: "direct-message users"},
		{name: "group", client: &fakeServerBootstrapClient{currentUser: &mattermost.User{ID: "me"}, channels: []mattermost.Channel{{ID: "group", Kind: mattermost.ChannelKindGroup}}, memberships: map[string][]mattermost.ChannelMembership{}, groupUsersErr: sentinel}, want: "group-message users"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BootstrapServer(context.Background(), tt.client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
			if !errors.Is(err, sentinel) || !strings.Contains(err.Error(), "server-1") || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v, want contextual lookup error", err)
			}
		})
	}
}

func TestServerBootstrapOmitsEmptyDirectSection(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		teams:       []mattermost.Team{{ID: "team-1", Name: "team"}},
		memberships: map[string][]mattermost.ChannelMembership{},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if got, want := len(snapshot.Sections), 1; got != want || snapshot.Sections[0].ID != "team:team-1" {
		t.Fatalf("sections = %#v, want one team section", snapshot.Sections)
	}
}

func TestServerBootstrapCaseFoldedTiesUseTheNextRequiredSortKey(t *testing.T) {
	client := &fakeServerBootstrapClient{
		currentUser: &mattermost.User{ID: "me"},
		teams: []mattermost.Team{
			{ID: "team-z", Name: "zeta", DisplayName: "Alpha"},
			{ID: "team-a", Name: "alpha", DisplayName: "alpha"},
		},
		channels:    []mattermost.Channel{{ID: "group", Kind: mattermost.ChannelKindGroup}},
		memberships: map[string][]mattermost.ChannelMembership{},
		groupUsers: map[string][]mattermost.User{
			"group": {
				{ID: "user-z", Nickname: "Sam"},
				{ID: "user-a", Nickname: "sam"},
			},
		},
	}
	snapshot, err := BootstrapServer(context.Background(), client, mattermost.Server{ID: "server-1", URL: "https://chat.example.com"})
	if err != nil {
		t.Fatalf("BootstrapServer returned error: %v", err)
	}
	if got, want := teamIDs(snapshot.Teams), []string{"team-a", "team-z"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("team IDs = %#v, want folded display-name tie resolved by Name", got)
	}
	if got, want := snapshot.Sections[0].Channels[0].DisplayName, "sam, Sam"; got != want {
		t.Fatalf("group name = %q, want %q with folded name tie resolved by ID", got, want)
	}
}

func entryNames(section ChannelSection) []string {
	names := make([]string, len(section.Channels))
	for i := range section.Channels {
		names[i] = section.Channels[i].DisplayName
	}
	return names
}

func channelIDs(section ChannelSection) []string {
	ids := make([]string, len(section.Channels))
	for i := range section.Channels {
		ids[i] = section.Channels[i].Channel.ID
	}
	return ids
}

func clientChannelByID(channels []mattermost.Channel, id string) mattermost.Channel {
	for _, channel := range channels {
		if channel.ID == id {
			return channel
		}
	}
	return mattermost.Channel{}
}
