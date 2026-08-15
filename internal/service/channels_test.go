package service

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/mattermost"
)

func TestUnreadDerivedFromMembership(t *testing.T) {
	for _, tt := range []struct {
		name       string
		channel    mattermost.Channel
		membership *mattermost.ChannelMembership
		want       bool
	}{
		{
			name:       "mentions",
			channel:    mattermost.Channel{TotalMsgCount: 5},
			membership: &mattermost.ChannelMembership{MsgCount: 5, MentionCount: 1},
			want:       true,
		},
		{
			name:       "message count divergence",
			channel:    mattermost.Channel{TotalMsgCount: 6},
			membership: &mattermost.ChannelMembership{MsgCount: 5},
			want:       true,
		},
		{
			name:       "equal counts and zero mentions",
			channel:    mattermost.Channel{TotalMsgCount: 5},
			membership: &mattermost.ChannelMembership{MsgCount: 5},
			want:       false,
		},
		{
			name:    "absent membership",
			channel: mattermost.Channel{TotalMsgCount: 5},
			want:    false,
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := ChannelEntry{Channel: tt.channel, Membership: tt.membership}
			if got := ChannelHasUnread(entry); got != tt.want {
				t.Fatalf("ChannelHasUnread() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestUnreadPostInInactiveChannelBecomesUnread(t *testing.T) {
	entry := ChannelEntry{
		Channel:    mattermost.Channel{ID: "channel-1", TotalMsgCount: 5},
		Membership: &mattermost.ChannelMembership{ChannelID: "channel-1", UserID: "user-1", MsgCount: 5},
	}

	got := ChannelWithNewPost(entry, false)

	if !ChannelHasUnread(got) {
		t.Fatal("ChannelHasUnread() = false, want true")
	}
	if got.Channel.TotalMsgCount != 6 || got.Membership.MsgCount != 5 {
		t.Fatalf("post counts = channel %d, membership %d; want 6, 5", got.Channel.TotalMsgCount, got.Membership.MsgCount)
	}
}

func TestUnreadPostInActivelyViewedChannelRemainsRead(t *testing.T) {
	entry := ChannelEntry{
		Channel:    mattermost.Channel{ID: "channel-1", TotalMsgCount: 5},
		Membership: &mattermost.ChannelMembership{ChannelID: "channel-1", UserID: "user-1", MsgCount: 5},
	}

	got := ChannelWithNewPost(entry, true)

	if ChannelHasUnread(got) {
		t.Fatal("ChannelHasUnread() = true, want false")
	}
	if got.Channel.TotalMsgCount != 6 || got.Membership.MsgCount != 6 {
		t.Fatalf("post counts = channel %d, membership %d; want 6, 6", got.Channel.TotalMsgCount, got.Membership.MsgCount)
	}
}

func TestUnreadPostInActivelyViewedChannelPreservesExistingMentions(t *testing.T) {
	entry := ChannelEntry{
		Channel: mattermost.Channel{ID: "channel-1", ServerID: "server-1", TotalMsgCount: 5, UpdatedAt: 100},
		Membership: &mattermost.ChannelMembership{
			ChannelID:    "channel-1",
			UserID:       "user-1",
			MsgCount:     5,
			MentionCount: 2,
			LastViewedAt: 80,
			UpdatedAt:    90,
		},
	}
	wantOriginal := entry
	wantMembership := *entry.Membership
	wantMembership.MsgCount++

	got := ChannelWithNewPost(entry, true)

	if got.Channel.TotalMsgCount != 6 {
		t.Fatalf("channel total = %d, want 6", got.Channel.TotalMsgCount)
	}
	if got.Membership == nil || !reflect.DeepEqual(*got.Membership, wantMembership) {
		t.Fatalf("membership = %#v, want %#v", got.Membership, wantMembership)
	}
	if !reflect.DeepEqual(entry, wantOriginal) {
		t.Fatalf("ChannelWithNewPost mutated input: %#v", entry)
	}
}

func TestUnreadPostCountersSaturateAtMaxInt64(t *testing.T) {
	t.Run("inactive channel stays unread", func(t *testing.T) {
		entry := ChannelEntry{
			Channel:    mattermost.Channel{TotalMsgCount: math.MaxInt64},
			Membership: &mattermost.ChannelMembership{MsgCount: math.MaxInt64 - 1},
		}

		got := ChannelWithNewPost(entry, false)

		if got.Channel.TotalMsgCount != math.MaxInt64 {
			t.Fatalf("channel total = %d, want %d", got.Channel.TotalMsgCount, int64(math.MaxInt64))
		}
		if !ChannelHasUnread(got) {
			t.Fatal("ChannelHasUnread() = false, want existing unread state preserved")
		}
	})

	t.Run("active channel stays read", func(t *testing.T) {
		entry := ChannelEntry{
			Channel:    mattermost.Channel{TotalMsgCount: math.MaxInt64},
			Membership: &mattermost.ChannelMembership{MsgCount: math.MaxInt64},
		}

		got := ChannelWithNewPost(entry, true)

		if got.Channel.TotalMsgCount != math.MaxInt64 || got.Membership.MsgCount != math.MaxInt64 {
			t.Fatalf("post counts = channel %d, membership %d; want both %d", got.Channel.TotalMsgCount, got.Membership.MsgCount, int64(math.MaxInt64))
		}
		if ChannelHasUnread(got) {
			t.Fatal("ChannelHasUnread() = true, want false")
		}
	})
}

func TestUnreadPostAtActiveCounterBoundaryPreservesDivergence(t *testing.T) {
	entry := ChannelEntry{
		Channel: mattermost.Channel{ID: "channel-1", TotalMsgCount: math.MaxInt64},
		Membership: &mattermost.ChannelMembership{
			ChannelID:    "channel-1",
			UserID:       "user-1",
			MsgCount:     math.MaxInt64 - 1,
			MentionCount: 2,
			LastViewedAt: 80,
			UpdatedAt:    90,
		},
	}

	got := ChannelWithNewPost(entry, true)

	if !reflect.DeepEqual(got, entry) {
		t.Fatalf("post entry = %#v, want unchanged %#v", got, entry)
	}
	if !ChannelHasUnread(got) {
		t.Fatal("ChannelHasUnread() = false, want existing divergence preserved")
	}
}

func TestUnreadPostAtActiveMembershipCounterBoundaryPreservesDivergence(t *testing.T) {
	entry := ChannelEntry{
		Channel: mattermost.Channel{
			ID:            "channel-1",
			ServerID:      "server-1",
			TotalMsgCount: math.MaxInt64 - 1,
			UpdatedAt:     100,
		},
		DisplayName: "Town Square",
		Membership: &mattermost.ChannelMembership{
			ChannelID:    "channel-1",
			UserID:       "user-1",
			MsgCount:     math.MaxInt64,
			MentionCount: 2,
			LastViewedAt: 80,
			UpdatedAt:    90,
		},
	}

	got := ChannelWithNewPost(entry, true)

	if !reflect.DeepEqual(got, entry) {
		t.Fatalf("post entry = %#v, want unchanged %#v", got, entry)
	}
}

func TestUnreadPostInActivelyViewedChannelPreservesExistingCountDivergence(t *testing.T) {
	entry := ChannelEntry{
		Channel: mattermost.Channel{TotalMsgCount: 8},
		Membership: &mattermost.ChannelMembership{
			MsgCount:     5,
			MentionCount: 2,
		},
	}

	got := ChannelWithNewPost(entry, true)

	if got.Channel.TotalMsgCount != 9 || got.Membership.MsgCount != 6 {
		t.Fatalf("post counts = channel %d, membership %d; want 9, 6", got.Channel.TotalMsgCount, got.Membership.MsgCount)
	}
	if got.Membership.MentionCount != 2 {
		t.Fatalf("mention count = %d, want 2", got.Membership.MentionCount)
	}
	if !ChannelHasUnread(got) {
		t.Fatal("ChannelHasUnread() = false, want pre-existing divergence preserved")
	}
}

func TestUnreadPostWithoutMembershipOnlyAdvancesChannel(t *testing.T) {
	for _, tt := range []struct {
		name  string
		count int64
		want  int64
	}{
		{name: "increments", count: 5, want: 6},
		{name: "saturates", count: math.MaxInt64, want: math.MaxInt64},
	} {
		t.Run(tt.name, func(t *testing.T) {
			entry := ChannelEntry{Channel: mattermost.Channel{ID: "channel-1", TotalMsgCount: tt.count}}

			got := ChannelWithNewPost(entry, true)

			if got.Channel.TotalMsgCount != tt.want || got.Membership != nil {
				t.Fatalf("post entry = %#v, want channel total %d and nil membership", got, tt.want)
			}
		})
	}
}

func TestUnreadViewedWithoutMembershipIsNoOp(t *testing.T) {
	entry := ChannelEntry{
		Channel:     mattermost.Channel{ID: "channel-1", TotalMsgCount: 5},
		DisplayName: "Town Square",
	}

	if got := ChannelViewed(entry); !reflect.DeepEqual(got, entry) {
		t.Fatalf("ChannelViewed() = %#v, want unchanged %#v", got, entry)
	}
}

func TestUnreadViewedClearsUnreadAndMentionsWithoutMutatingUnrelatedData(t *testing.T) {
	entry := ChannelEntry{
		Channel: mattermost.Channel{
			ID:            "channel-1",
			ServerID:      "server-1",
			TeamID:        "team-1",
			Name:          "town-square",
			DisplayName:   "Town Square",
			Kind:          mattermost.ChannelKindPublic,
			TotalMsgCount: 8,
			UpdatedAt:     100,
		},
		DisplayName: "Derived Town Square",
		Membership: &mattermost.ChannelMembership{
			ChannelID:    "channel-1",
			UserID:       "user-1",
			MsgCount:     5,
			MentionCount: 2,
			LastViewedAt: 80,
			UpdatedAt:    90,
		},
	}
	wantOriginal := entry
	wantMembership := *entry.Membership
	wantMembership.MsgCount = entry.Channel.TotalMsgCount
	wantMembership.MentionCount = 0

	got := ChannelViewed(entry)

	if ChannelHasUnread(got) {
		t.Fatal("ChannelHasUnread() = true, want false")
	}
	if !reflect.DeepEqual(got.Channel, entry.Channel) || got.DisplayName != entry.DisplayName {
		t.Fatalf("viewed entry mutated unrelated channel data: got %#v, input %#v", got, entry)
	}
	if got.Membership == nil || !reflect.DeepEqual(*got.Membership, wantMembership) {
		t.Fatalf("viewed membership = %#v, want %#v", got.Membership, wantMembership)
	}
	if !reflect.DeepEqual(entry, wantOriginal) || entry.Membership.MsgCount != 5 || entry.Membership.MentionCount != 2 {
		t.Fatalf("ChannelViewed mutated input: %#v", entry)
	}
}

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
