package cache

import (
	"database/sql"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
)

func TestMattermostCRUDScopesRemoteIDsByServerAndCascadesOnlyDeletedServer(t *testing.T) {
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	for _, server := range []MattermostServer{
		{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 10},
		{ID: "s2", Name: "Two", URL: "https://two.example", CurrentUserID: "u1", LastSyncedAt: 20},
	} {
		if err := db.UpsertMattermostServer(server); err != nil {
			t.Fatalf("UpsertMattermostServer(%s): %v", server.ID, err)
		}
		if err := db.UpsertMattermostTeam(server.ID, MattermostTeam{ID: "same", Name: server.ID, DisplayName: server.Name}); err != nil {
			t.Fatalf("UpsertMattermostTeam(%s): %v", server.ID, err)
		}
		if err := db.UpsertMattermostUser(server.ID, MattermostUser{ID: "u1", Username: server.ID, UpdatedAt: 1}); err != nil {
			t.Fatalf("UpsertMattermostUser(%s): %v", server.ID, err)
		}
		if err := db.UpsertMattermostChannel(server.ID, MattermostChannel{ID: "same", TeamID: "same", Name: server.ID, Kind: "public", TotalMsgCount: 1, UpdatedAt: 1}); err != nil {
			t.Fatalf("UpsertMattermostChannel(%s): %v", server.ID, err)
		}
		if err := db.UpsertMattermostChannelMembership(server.ID, MattermostChannelMembership{ChannelID: "same", UserID: "u1", MsgCount: 1}); err != nil {
			t.Fatalf("UpsertMattermostChannelMembership(%s): %v", server.ID, err)
		}
		if err := db.ReplaceMattermostChannelUserIDs(server.ID, "same", []string{"u1", "peer"}); err != nil {
			t.Fatalf("ReplaceMattermostChannelUserIDs(%s): %v", server.ID, err)
		}
		if err := db.UpsertMattermostPost(server.ID, MattermostPost{ID: "same", ChannelID: "same", UserID: "u1", Text: server.ID, CreatedAt: 1}); err != nil {
			t.Fatalf("UpsertMattermostPost(%s): %v", server.ID, err)
		}
	}

	team1, err := db.GetMattermostTeam("s1", "same")
	if err != nil || team1.Name != "s1" {
		t.Fatalf("GetMattermostTeam(s1) = %#v, %v", team1, err)
	}
	team2, err := db.GetMattermostTeam("s2", "same")
	if err != nil || team2.Name != "s2" {
		t.Fatalf("GetMattermostTeam(s2) = %#v, %v", team2, err)
	}
	post1, err := db.GetMattermostPost("s1", "same")
	if err != nil || post1.Text != "s1" {
		t.Fatalf("GetMattermostPost(s1) = %#v, %v", post1, err)
	}

	if err := db.DeleteMattermostServer("s1"); err != nil {
		t.Fatalf("DeleteMattermostServer: %v", err)
	}
	if _, err := db.GetMattermostPost("s1", "same"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted server post error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.GetMattermostPost("s2", "same"); err != nil {
		t.Fatalf("other server post removed: %v", err)
	}
}

func TestMattermostListAPIsAndParticipantReplacement(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t2", Name: "z", DisplayName: "Zulu"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1", Name: "a", DisplayName: "Alpha"}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []MattermostUser{{ID: "u2", Username: "z"}, {ID: "u1", Username: "a"}} {
		if err := db.UpsertMattermostUser("s1", user); err != nil {
			t.Fatal(err)
		}
	}
	for _, channel := range []MattermostChannel{
		{ID: "c2", TeamID: "t2", Name: "z", Kind: "private"},
		{ID: "c1", TeamID: "t1", Name: "a", Kind: "public"},
		{ID: "dm", Name: "u1__u2", Kind: "direct"},
	} {
		if err := db.UpsertMattermostChannel("s1", channel); err != nil {
			t.Fatal(err)
		}
	}
	for _, membership := range []MattermostChannelMembership{
		{ChannelID: "c2", UserID: "u1", MsgCount: 2},
		{ChannelID: "c1", UserID: "u1", MsgCount: 1},
	} {
		if err := db.UpsertMattermostChannelMembership("s1", membership); err != nil {
			t.Fatal(err)
		}
	}
	if err := db.ReplaceMattermostChannelUserIDs("s1", "dm", []string{"u2", "u1", "u2"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceMattermostChannelUserIDs("s1", "dm", []string{"u3", "u1"}); err != nil {
		t.Fatal(err)
	}

	servers, err := db.ListMattermostServers()
	if err != nil || len(servers) != 1 {
		t.Fatalf("ListMattermostServers = %#v, %v", servers, err)
	}
	teams, err := db.ListMattermostTeams("s1")
	if err != nil || !reflect.DeepEqual(teamIDs(teams), []string{"t1", "t2"}) {
		t.Fatalf("ListMattermostTeams = %#v, %v", teams, err)
	}
	users, err := db.ListMattermostUsers("s1")
	if err != nil || !reflect.DeepEqual(userIDs(users), []string{"u1", "u2"}) {
		t.Fatalf("ListMattermostUsers = %#v, %v", users, err)
	}
	channels, err := db.ListMattermostChannels("s1")
	if err != nil || !reflect.DeepEqual(channelIDs(channels), []string{"c1", "dm", "c2"}) {
		t.Fatalf("ListMattermostChannels = %#v, %v", channels, err)
	}
	teamChannels, err := db.ListMattermostTeamChannels("s1", "t1")
	if err != nil || !reflect.DeepEqual(channelIDs(teamChannels), []string{"c1"}) {
		t.Fatalf("ListMattermostTeamChannels = %#v, %v", teamChannels, err)
	}
	memberships, err := db.ListMattermostChannelMemberships("s1", "u1")
	if err != nil || len(memberships) != 2 || memberships[0].ChannelID != "c1" {
		t.Fatalf("ListMattermostChannelMemberships = %#v, %v", memberships, err)
	}
	membership, err := db.GetMattermostChannelMembership("s1", "c2", "u1")
	if err != nil || membership.MsgCount != 2 {
		t.Fatalf("GetMattermostChannelMembership = %#v, %v", membership, err)
	}
	participants, err := db.ListMattermostChannelUserIDs("s1", "dm")
	if err != nil || !reflect.DeepEqual(participants, []string{"u1", "u3"}) {
		t.Fatalf("ListMattermostChannelUserIDs = %#v, %v", participants, err)
	}
}

func TestMattermostSnapshotStoresSuppliedRawUsersMembershipsAndParticipants(t *testing.T) {
	db := setupMattermostDB(t)
	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1"},
		CurrentUser: MattermostUser{ID: "u1", Username: "self"},
		Users:       []MattermostUser{{ID: "u2", Username: "peer"}},
		Teams:       []MattermostTeam{{ID: "t1"}},
		Channels:    []MattermostChannel{{ID: "c1", TeamID: "t1", Kind: "public"}},
		Memberships: []MattermostChannelMembership{{ChannelID: "c1", UserID: "u1", MsgCount: 3}},
		ChannelUsers: map[string][]string{
			"c1": {"u1", "u2"},
		},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatalf("ApplyMattermostBootstrapSnapshot: %v", err)
	}
	if _, err := db.GetMattermostUser("s1", "u2"); err != nil {
		t.Fatalf("GetMattermostUser(u2): %v", err)
	}
	membership, err := db.GetMattermostChannelMembership("s1", "c1", "u1")
	if err != nil || membership.MsgCount != 3 {
		t.Fatalf("membership = %#v, %v", membership, err)
	}
	participants, err := db.ListMattermostChannelUserIDs("s1", "c1")
	if err != nil || !reflect.DeepEqual(participants, []string{"u1", "u2"}) {
		t.Fatalf("participants = %#v, %v", participants, err)
	}
}

func TestLoadMattermostBootstrapSnapshotReadsCohesiveNonDeletedState(t *testing.T) {
	db := setupMattermostDB(t)
	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 9},
		CurrentUser: MattermostUser{ID: "u1", Username: "self", UpdatedAt: 7},
		Users:       []MattermostUser{{ID: "u2", Username: "peer", UpdatedAt: 8}},
		Teams:       []MattermostTeam{{ID: "t1", DisplayName: "Team", UpdatedAt: 6}},
		Channels: []MattermostChannel{
			{ID: "c1", TeamID: "t1", Kind: "public", UpdatedAt: 5},
			{ID: "gone", TeamID: "t1", Kind: "public", DeletedAt: 10},
		},
		Memberships:  []MattermostChannelMembership{{ChannelID: "c1", UserID: "u1", MsgCount: 3, UpdatedAt: 9}},
		ChannelUsers: map[string][]string{"c1": {"u1", "u2"}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	got, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Channels) != 1 || got.Channels[0].ID != "c1" || !reflect.DeepEqual(got.ChannelUsers["c1"], []string{"u1", "u2"}) {
		t.Fatalf("loaded snapshot = %#v", got)
	}
	if _, err := db.LoadMattermostBootstrapSnapshot("missing"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("missing error = %v", err)
	}
}

func TestMattermostSnapshotParticipantUpsertsDoNotDeleteOmittedRows(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceMattermostChannelUserIDs("s1", "c1", []string{"u1", "u2"}); err != nil {
		t.Fatal(err)
	}
	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 1},
		CurrentUser: MattermostUser{ID: "u1", UpdatedAt: 1},
		ChannelUsers: map[string][]string{
			"c1": {"u1", "u3"},
		},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	participants, err := db.ListMattermostChannelUserIDs("s1", "c1")
	if err != nil || !reflect.DeepEqual(participants, []string{"u1", "u2", "u3"}) {
		t.Fatalf("participants = %v, %v", participants, err)
	}
}

func TestReplaceMattermostBootstrapSnapshotRetiresAbsentRowsAndPreservesPosts(t *testing.T) {
	db := setupMattermostDB(t)
	initial := MattermostBootstrapSnapshot{
		Server: MattermostServer{ID: "s1", URL: "https://one.example", CurrentUserID: "u1"}, CurrentUser: MattermostUser{ID: "u1"},
		Teams:        []MattermostTeam{{ID: "t1"}, {ID: "gone-team"}},
		Channels:     []MattermostChannel{{ID: "c1", TeamID: "t1", Kind: "public"}, {ID: "gone", TeamID: "gone-team", Kind: "public"}},
		Memberships:  []MattermostChannelMembership{{ChannelID: "c1", UserID: "u1"}, {ChannelID: "gone", UserID: "u1"}},
		ChannelUsers: map[string][]string{"c1": {"u1", "peer"}, "gone": {"u1", "departed"}},
	}
	if err := db.ReplaceMattermostBootstrapSnapshot(initial); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p1", ChannelID: "gone", Text: "retained"}); err != nil {
		t.Fatal(err)
	}
	next := MattermostBootstrapSnapshot{
		Server: initial.Server, CurrentUser: initial.CurrentUser,
		Teams: []MattermostTeam{{ID: "t1"}}, Channels: []MattermostChannel{{ID: "c1", TeamID: "t1", Kind: "public"}},
		Memberships: []MattermostChannelMembership{{ChannelID: "c1", UserID: "u1"}}, ChannelUsers: map[string][]string{"c1": {"u1"}},
	}
	if err := db.ReplaceMattermostBootstrapSnapshot(next); err != nil {
		t.Fatal(err)
	}
	loaded, err := db.LoadMattermostBootstrapSnapshot("s1")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(teamIDs(loaded.Teams), []string{"t1"}) || !reflect.DeepEqual(channelIDs(loaded.Channels), []string{"c1"}) {
		t.Fatalf("active snapshot teams=%v channels=%v", teamIDs(loaded.Teams), channelIDs(loaded.Channels))
	}
	if len(loaded.Memberships) != 1 || !reflect.DeepEqual(loaded.ChannelUsers["c1"], []string{"u1"}) {
		t.Fatalf("replacement memberships=%#v users=%#v", loaded.Memberships, loaded.ChannelUsers)
	}
	if post, err := db.GetMattermostPost("s1", "p1"); err != nil || post.Text != "retained" {
		t.Fatalf("retained post=%#v err=%v", post, err)
	}
}

func TestMattermostRevisionAwareUpsertsRejectDelayedSnapshotRows(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostUser("s1", MattermostUser{ID: "u1", Username: "new-user", UpdatedAt: 20}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Name: "new-channel", Kind: "public", UpdatedAt: 20, DeletedAt: 25}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannelMembership("s1", MattermostChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: 20, MentionCount: 2, LastViewedAt: 20, UpdatedAt: 20}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostServer(MattermostServer{ID: "s1", Name: "new-server", URL: "https://new.example", CurrentUserID: "u1", LastSyncedAt: 20}); err != nil {
		t.Fatal(err)
	}

	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "old-server", URL: "https://old.example", CurrentUserID: "u1", LastSyncedAt: 10},
		CurrentUser: MattermostUser{ID: "u1", Username: "old-user", UpdatedAt: 10},
		Channels:    []MattermostChannel{{ID: "c1", TeamID: "t1", Name: "old-channel", Kind: "public", UpdatedAt: 10}},
		Memberships: []MattermostChannelMembership{{ChannelID: "c1", UserID: "u1", MsgCount: 1, UpdatedAt: 10}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	server, _ := db.GetMattermostServer("s1")
	user, _ := db.GetMattermostUser("s1", "u1")
	channel, _ := db.GetMattermostChannel("s1", "c1")
	membership, _ := db.GetMattermostChannelMembership("s1", "c1", "u1")
	if server.Name != "new-server" || server.LastSyncedAt != 20 {
		t.Errorf("server regressed: %#v", server)
	}
	if user.Username != "new-user" || user.UpdatedAt != 20 {
		t.Errorf("user regressed: %#v", user)
	}
	if channel.Name != "new-channel" || channel.DeletedAt != 25 {
		t.Errorf("channel regressed: %#v", channel)
	}
	if membership.MsgCount != 20 || membership.UpdatedAt != 20 {
		t.Errorf("membership regressed: %#v", membership)
	}
}

func TestMattermostDelayedSnapshotDoesNotRegressTeam(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1", Name: "new", DisplayName: "New", UpdatedAt: 100}); err != nil {
		t.Fatal(err)
	}
	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 1},
		CurrentUser: MattermostUser{ID: "u1", UpdatedAt: 1},
		Teams:       []MattermostTeam{{ID: "t1", Name: "old", DisplayName: "Old", UpdatedAt: 50}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	team, err := db.GetMattermostTeam("s1", "t1")
	if err != nil || team.Name != "new" || team.DisplayName != "New" || team.UpdatedAt != 100 {
		t.Fatalf("team = %#v, %v", team, err)
	}
}

func TestMattermostTeamEqualRevisionMergeIsOrderIndependent(t *testing.T) {
	run := func(t *testing.T, reverse bool) MattermostTeam {
		db := setupMattermostDB(t)
		teams := []MattermostTeam{
			{ID: "t1", Name: "alpha", UpdatedAt: 10},
			{ID: "t1", Name: "zulu", DisplayName: "Zulu", UpdatedAt: 10},
		}
		if reverse {
			teams[0], teams[1] = teams[1], teams[0]
		}
		for _, team := range teams {
			if err := db.UpsertMattermostTeam("s1", team); err != nil {
				t.Fatal(err)
			}
		}
		team, err := db.GetMattermostTeam("s1", "t1")
		if err != nil {
			t.Fatal(err)
		}
		return team
	}
	forward := run(t, false)
	reverse := run(t, true)
	if !reflect.DeepEqual(forward, reverse) || forward.Name != "zulu" || forward.DisplayName != "Zulu" {
		t.Fatalf("forward=%#v reverse=%#v", forward, reverse)
	}
}

func TestMattermostOlderChannelTombstoneChangesNothing(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	want := MattermostChannel{ID: "c1", TeamID: "t1", Name: "new", DisplayName: "New", Kind: "public", TotalMsgCount: 9, UpdatedAt: 100}
	if err := db.UpsertMattermostChannel("s1", want); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", Kind: "direct", TotalMsgCount: 1, DeletedAt: 50}); err != nil {
		t.Fatal(err)
	}
	got, err := db.GetMattermostChannel("s1", "c1")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("channel = %#v, %v; want %#v", got, err, want)
	}
}

func TestMattermostNewerChannelCountCanCorrectDownward(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public", TotalMsgCount: 9, UpdatedAt: 10}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public", TotalMsgCount: 3, UpdatedAt: 20}); err != nil {
		t.Fatal(err)
	}
	channel, err := db.GetMattermostChannel("s1", "c1")
	if err != nil || channel.TotalMsgCount != 3 {
		t.Fatalf("channel = %#v, %v", channel, err)
	}
}

func TestMattermostEqualRevisionMergesAreOrderIndependent(t *testing.T) {
	type result struct {
		server     MattermostServer
		user       MattermostUser
		channel    MattermostChannel
		membership MattermostChannelMembership
	}
	run := func(t *testing.T, reverse bool) result {
		db := setupMattermostDB(t)
		if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
			t.Fatal(err)
		}
		servers := []MattermostServer{
			{ID: "s1", Name: "Alpha", URL: "https://a.example", CurrentUserID: "u1", LastSyncedAt: 10},
			{ID: "s1", Name: "Zulu", URL: "https://z.example", CurrentUserID: "u1", LastSyncedAt: 10},
		}
		users := []MattermostUser{
			{ID: "u1", Username: "alpha", FirstName: "Ada", UpdatedAt: 10},
			{ID: "u1", Username: "zulu", LastName: "Lovelace", UpdatedAt: 10},
		}
		channels := []MattermostChannel{
			{ID: "c1", TeamID: "t1", Name: "alpha", Kind: "public", TotalMsgCount: 2, UpdatedAt: 10},
			{ID: "c1", TeamID: "t1", Name: "zulu", Kind: "public", TotalMsgCount: 5, UpdatedAt: 10, DeletedAt: 10},
		}
		memberships := []MattermostChannelMembership{
			{ChannelID: "c1", UserID: "u1", MsgCount: 2, MentionCount: 1, LastViewedAt: 3, UpdatedAt: 10},
			{ChannelID: "c1", UserID: "u1", MsgCount: 5, MentionCount: 2, LastViewedAt: 7, UpdatedAt: 10},
		}
		if reverse {
			servers[0], servers[1] = servers[1], servers[0]
			users[0], users[1] = users[1], users[0]
			channels[0], channels[1] = channels[1], channels[0]
			memberships[0], memberships[1] = memberships[1], memberships[0]
		}
		for i := range 2 {
			if err := db.UpsertMattermostServer(servers[i]); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertMattermostUser("s1", users[i]); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertMattermostChannel("s1", channels[i]); err != nil {
				t.Fatal(err)
			}
			if err := db.UpsertMattermostChannelMembership("s1", memberships[i]); err != nil {
				t.Fatal(err)
			}
		}
		server, _ := db.GetMattermostServer("s1")
		user, _ := db.GetMattermostUser("s1", "u1")
		channel, _ := db.GetMattermostChannel("s1", "c1")
		membership, _ := db.GetMattermostChannelMembership("s1", "c1", "u1")
		return result{server, user, channel, membership}
	}
	forward := run(t, false)
	reverse := run(t, true)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("equal-revision results differ:\nforward=%#v\nreverse=%#v", forward, reverse)
	}
	if forward.channel.DeletedAt != 10 || forward.user.FirstName != "Ada" || forward.user.LastName != "Lovelace" || forward.membership.MsgCount != 5 {
		t.Fatalf("equal-revision enrichment missing: %#v", forward)
	}
}

func TestMattermostSnapshotCurrentUserMergesDuplicateUsersEntryAtEqualRevision(t *testing.T) {
	db := setupMattermostDB(t)
	snapshot := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 1},
		CurrentUser: MattermostUser{ID: "u1", Username: "authoritative", FirstName: "Ada", UpdatedAt: 1},
		Users:       []MattermostUser{{ID: "u1", Username: "stale-list-entry", LastName: "Lovelace", UpdatedAt: 1}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(snapshot); err != nil {
		t.Fatalf("ApplyMattermostBootstrapSnapshot: %v", err)
	}
	user, err := db.GetMattermostUser("s1", "u1")
	if err != nil {
		t.Fatalf("GetMattermostUser: %v", err)
	}
	if user.Username != "stale-list-entry" || user.FirstName != "Ada" || user.LastName != "Lovelace" {
		t.Fatalf("equal-revision merged user = %#v", user)
	}
}

func TestMattermostSnapshotIsAtomicUpsertOnly(t *testing.T) {
	db := setupMattermostDB(t)
	old := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "Old", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 1},
		CurrentUser: MattermostUser{ID: "u1", Username: "old", UpdatedAt: 1},
		Teams:       []MattermostTeam{{ID: "old-team", Name: "old"}},
		Channels:    []MattermostChannel{{ID: "old-channel", TeamID: "old-team", Kind: "public"}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(old); err != nil {
		t.Fatalf("Apply old snapshot: %v", err)
	}

	invalid := MattermostBootstrapSnapshot{
		Server:      MattermostServer{ID: "s1", Name: "New", URL: "https://one.example", CurrentUserID: "u1", LastSyncedAt: 2},
		CurrentUser: MattermostUser{ID: "u1", Username: "new", UpdatedAt: 2},
		Teams:       []MattermostTeam{{ID: "new-team", Name: "new"}},
		Channels:    []MattermostChannel{{ID: "broken", TeamID: "missing", Kind: "public"}},
	}
	if err := db.ApplyMattermostBootstrapSnapshot(invalid); err == nil {
		t.Fatal("invalid snapshot returned nil error")
	}
	server, _ := db.GetMattermostServer("s1")
	user, _ := db.GetMattermostUser("s1", "u1")
	if server.Name != "Old" || user.Username != "old" {
		t.Fatalf("failed snapshot changed prior rows: server=%#v user=%#v", server, user)
	}
	if _, err := db.GetMattermostTeam("s1", "new-team"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("new-team error = %v, want sql.ErrNoRows", err)
	}

	updated := old
	updated.Server.Name = "Updated"
	updated.Server.LastSyncedAt = 3
	updated.CurrentUser.UpdatedAt = 3
	updated.Teams = []MattermostTeam{{ID: "new-team", Name: "new"}}
	updated.Channels = nil
	if err := db.ApplyMattermostBootstrapSnapshot(updated); err != nil {
		t.Fatalf("Apply updated snapshot: %v", err)
	}
	if _, err := db.GetMattermostTeam("s1", "old-team"); err != nil {
		t.Fatalf("upsert-only snapshot pruned old team: %v", err)
	}
}

func TestMattermostConcurrentIndependentServerSnapshots(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, id := range []string{"s1", "s2"} {
		id := id
		wg.Add(1)
		go func() {
			defer wg.Done()
			errs <- db.ApplyMattermostBootstrapSnapshot(MattermostBootstrapSnapshot{
				Server:      MattermostServer{ID: id, Name: id, URL: "https://" + id + ".example", CurrentUserID: "u1"},
				CurrentUser: MattermostUser{ID: "u1", Username: id},
				Teams:       []MattermostTeam{{ID: "same", Name: id}},
				Channels:    []MattermostChannel{{ID: "same", TeamID: "same", Kind: "public"}},
			})
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("Apply snapshot: %v", err)
		}
	}
	for _, id := range []string{"s1", "s2"} {
		if _, err := db.GetMattermostChannel(id, "same"); err != nil {
			t.Fatalf("GetMattermostChannel(%s): %v", id, err)
		}
	}
}

func TestMattermostPostsUseRevisionConflictPolicyAndWindows(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	for _, channelID := range []string{"c1", "c2"} {
		if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: channelID, TeamID: "t1", Kind: "public"}); err != nil {
			t.Fatal(err)
		}
	}
	posts := []MattermostPost{
		{ID: "p1", ChannelID: "c1", Text: "one", CreatedAt: 10},
		{ID: "p2", ChannelID: "c1", Text: "two", CreatedAt: 20},
		{ID: "p3", ChannelID: "c1", Text: "three", CreatedAt: 30},
		{ID: "r1", ChannelID: "c1", RootID: "p1", Text: "reply", CreatedAt: 15},
	}
	if err := db.UpsertMattermostPosts("s1", posts); err != nil {
		t.Fatalf("UpsertMattermostPosts: %v", err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p2", ChannelID: "c1", Text: "edited", CreatedAt: 20, UpdatedAt: 40}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p2", ChannelID: "c1", Text: "stale", CreatedAt: 20, UpdatedAt: 30}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMattermostPostDeleted("s1", "p2", 50); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p2", ChannelID: "c1", Text: "resurrect", CreatedAt: 20, UpdatedAt: 45}); err != nil {
		t.Fatal(err)
	}
	tombstone, err := db.GetMattermostPost("s1", "p2")
	if err != nil || tombstone.Text != "edited" || tombstone.DeletedAt != 50 {
		t.Fatalf("tombstone = %#v, %v", tombstone, err)
	}

	main, err := db.ListMattermostChannelPosts("s1", "c1", 2, "")
	if err != nil || !reflect.DeepEqual(postIDs(main), []string{"p1", "p3"}) {
		t.Fatalf("main posts = %#v, %v", main, err)
	}
	before, err := db.ListMattermostChannelPosts("s1", "c1", 10, "p3")
	if err != nil || !reflect.DeepEqual(postIDs(before), []string{"p1"}) {
		t.Fatalf("before posts = %#v, %v", before, err)
	}
	thread, err := db.ListMattermostThreadPosts("s1", "c1", "p1")
	if err != nil || !reflect.DeepEqual(postIDs(thread), []string{"p1", "r1"}) {
		t.Fatalf("thread posts = %#v, %v", thread, err)
	}
	if _, err := db.ListMattermostChannelPosts("s1", "c2", 10, "p3"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("cross-channel anchor error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.ListMattermostChannelPosts("s1", "c1", 10, "r1"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("reply anchor error = %v, want sql.ErrNoRows", err)
	}
	if _, err := db.ListMattermostChannelPosts("s1", "c1", 10, "p2"); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("deleted anchor error = %v, want sql.ErrNoRows", err)
	}
	if err := db.MarkMattermostPostDeleted("s1", "missing", 60); !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("unknown delete error = %v, want sql.ErrNoRows", err)
	}
}

func TestMattermostPostEqualRevisionMergeIsOrderIndependent(t *testing.T) {
	run := func(t *testing.T, reverse bool) MattermostPost {
		db := setupMattermostDB(t)
		if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
			t.Fatal(err)
		}
		if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
			t.Fatal(err)
		}
		posts := []MattermostPost{
			{ID: "p1", ChannelID: "c1", UserID: "u1", Text: "body", CreatedAt: 10, UpdatedAt: 20, ReplyCount: 2},
			{ID: "p1", ChannelID: "c1", RootID: "root", CreatedAt: 10, DeletedAt: 20, ReplyCount: 5},
		}
		if reverse {
			posts[0], posts[1] = posts[1], posts[0]
		}
		for _, post := range posts {
			if err := db.UpsertMattermostPost("s1", post); err != nil {
				t.Fatal(err)
			}
		}
		post, err := db.GetMattermostPost("s1", "p1")
		if err != nil {
			t.Fatal(err)
		}
		return post
	}
	forward := run(t, false)
	reverse := run(t, true)
	if !reflect.DeepEqual(forward, reverse) {
		t.Fatalf("post merge differs: forward=%#v reverse=%#v", forward, reverse)
	}
	if forward.DeletedAt != 20 || forward.Text != "body" || forward.UserID != "u1" || forward.RootID != "root" || forward.ReplyCount != 5 {
		t.Fatalf("post merge lost data: %#v", forward)
	}
}

func TestMattermostNewerPostReplyCountCanCorrectDownward(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p1", ChannelID: "c1", CreatedAt: 10, UpdatedAt: 10, ReplyCount: 9}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p1", ChannelID: "c1", CreatedAt: 10, UpdatedAt: 20, ReplyCount: 3}); err != nil {
		t.Fatal(err)
	}
	post, err := db.GetMattermostPost("s1", "p1")
	if err != nil || post.ReplyCount != 3 {
		t.Fatalf("post = %#v, %v", post, err)
	}
}

func TestMattermostMarkDeletedWinsAtEqualRevision(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p1", ChannelID: "c1", Text: "edited", CreatedAt: 10, UpdatedAt: 20}); err != nil {
		t.Fatal(err)
	}
	if err := db.MarkMattermostPostDeleted("s1", "p1", 20); err != nil {
		t.Fatal(err)
	}
	post, err := db.GetMattermostPost("s1", "p1")
	if err != nil || post.DeletedAt != 20 || post.Text != "edited" {
		t.Fatalf("post = %#v, %v", post, err)
	}
}

func TestMattermostNewerChannelTombstonePreservesOmittedMetadata(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Name: "town-square", DisplayName: "Town Square", Kind: "public", TotalMsgCount: 9, UpdatedAt: 10}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", Kind: "direct", DeletedAt: 20}); err != nil {
		t.Fatal(err)
	}
	channel, err := db.GetMattermostChannel("s1", "c1")
	if err != nil {
		t.Fatal(err)
	}
	if channel.TeamID != "t1" || channel.Name != "town-square" || channel.DisplayName != "Town Square" || channel.Kind != "public" || channel.TotalMsgCount != 0 || channel.DeletedAt != 20 {
		t.Fatalf("channel = %#v", channel)
	}
}

func TestMattermostConcurrentEqualRevisionPostUpsertsMergeDeterministically(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if err := db.UpsertMattermostServer(MattermostServer{ID: "s1", URL: "https://one.example", CurrentUserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	posts := []MattermostPost{
		{ID: "p1", ChannelID: "c1", UserID: "u1", Text: "body", CreatedAt: 10, UpdatedAt: 20, ReplyCount: 2},
		{ID: "p1", ChannelID: "c1", RootID: "root", CreatedAt: 10, DeletedAt: 20, ReplyCount: 5},
	}
	start := make(chan struct{})
	errs := make(chan error, 2)
	var wg sync.WaitGroup
	for _, post := range posts {
		post := post
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errs <- db.UpsertMattermostPost("s1", post)
		}()
	}
	close(start)
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	post, err := db.GetMattermostPost("s1", "p1")
	if err != nil {
		t.Fatal(err)
	}
	if post.DeletedAt != 20 || post.Text != "body" || post.UserID != "u1" || post.RootID != "root" || post.ReplyCount != 5 {
		t.Fatalf("concurrent merge = %#v", post)
	}
}

func TestMattermostServerDeleteCascadesAllChildCategories(t *testing.T) {
	db := setupMattermostDB(t)
	if err := db.UpsertMattermostTeam("s1", MattermostTeam{ID: "t1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostUser("s1", MattermostUser{ID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", TeamID: "t1", Kind: "public"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostChannelMembership("s1", MattermostChannelMembership{ChannelID: "c1", UserID: "u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.ReplaceMattermostChannelUserIDs("s1", "c1", []string{"u1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertMattermostPost("s1", MattermostPost{ID: "p1", ChannelID: "c1"}); err != nil {
		t.Fatal(err)
	}
	if err := db.DeleteMattermostServer("s1"); err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"mattermost_teams", "mattermost_users", "mattermost_channels", "mattermost_channel_memberships", "mattermost_channel_users", "mattermost_posts"} {
		var count int
		if err := db.conn.QueryRow(`SELECT COUNT(*) FROM ` + table + ` WHERE server_id = 's1'`).Scan(&count); err != nil {
			t.Fatalf("count %s: %v", table, err)
		}
		if count != 0 {
			t.Errorf("%s retained %d rows", table, count)
		}
	}
}

func TestMattermostValidationRejectsInvalidRecords(t *testing.T) {
	db := setupMattermostDB(t)
	tests := []struct {
		name string
		err  error
	}{
		{"empty team ID", db.UpsertMattermostTeam("s1", MattermostTeam{})},
		{"invalid channel kind", db.UpsertMattermostChannel("s1", MattermostChannel{ID: "c1", Kind: "other"})},
		{"negative user timestamp", db.UpsertMattermostUser("s1", MattermostUser{ID: "u1", UpdatedAt: -1})},
		{"negative membership count", db.UpsertMattermostChannelMembership("s1", MattermostChannelMembership{ChannelID: "c1", UserID: "u1", MsgCount: -1})},
		{"zero post limit", func() error { _, err := db.ListMattermostChannelPosts("s1", "c1", 0, ""); return err }()},
	}
	for _, test := range tests {
		if test.err == nil {
			t.Errorf("%s returned nil error", test.name)
		}
	}
}

func TestMattermostForeignKeysApplyOnEveryPooledFileConnection(t *testing.T) {
	db, err := New(filepath.Join(t.TempDir(), "cache.db"))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer db.Close()

	const connections = 4
	var wg sync.WaitGroup
	errs := make(chan error, connections)
	for i := range connections {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := db.conn.Exec(`INSERT INTO mattermost_teams (server_id, id, name, display_name) VALUES ('missing', ?, '', '')`, fmt.Sprintf("t%d", i))
			errs <- err
		}(i)
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err == nil {
			t.Fatal("pooled connection accepted missing server foreign key")
		}
	}
}

func setupMattermostDB(t *testing.T) *DB {
	t.Helper()
	db, err := New(":memory:")
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	if err := db.UpsertMattermostServer(MattermostServer{ID: "s1", Name: "One", URL: "https://one.example", CurrentUserID: "u1"}); err != nil {
		t.Fatalf("UpsertMattermostServer: %v", err)
	}
	return db
}

func teamIDs(items []MattermostTeam) []string {
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}

func userIDs(items []MattermostUser) []string {
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}

func channelIDs(items []MattermostChannel) []string {
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}

func postIDs(items []MattermostPost) []string {
	ids := make([]string, len(items))
	for i := range items {
		ids[i] = items[i].ID
	}
	return ids
}
