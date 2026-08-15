package main

import (
	"reflect"
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
)

func TestMattermostSnapshotAdaptersPreserveRawIdentityAndRebuildNames(t *testing.T) {
	observedAt := time.UnixMilli(1234)
	registry := config.MattermostServer{ID: "s1", URL: "https://chat.example"}
	server := mattermostServerFromRegistry(registry)
	if server.Name != "chat.example" {
		t.Fatalf("fallback name = %q", server.Name)
	}
	snapshot := service.ServerSnapshot{
		Server: server, CurrentUser: mattermost.User{ID: "me", ServerID: "s1", Username: "self", UpdatedAt: 10},
		Users:        []mattermost.User{{ID: "peer", ServerID: "s1", Nickname: "Alice", UpdatedAt: 11}},
		Teams:        []mattermost.Team{{ID: "t1", ServerID: "s1", DisplayName: "Team", UpdatedAt: 12}},
		Sections:     []service.ChannelSection{{ID: "server:s1:direct", Kind: service.ChannelSectionKindDirect, Channels: []service.ChannelEntry{{Channel: mattermost.Channel{ID: "dm", ServerID: "s1", Name: "me__peer", DisplayName: "Direct display", Kind: mattermost.ChannelKindDirect, LastPostAt: 123, UpdatedAt: 13}, Membership: &mattermost.ChannelMembership{ChannelID: "dm", UserID: "me", LastViewedAt: 77, UpdatedAt: 88, MsgCount: 3}, DisplayName: "Alice"}}}},
		ChannelUsers: map[string][]string{"dm": {"me", "peer"}},
	}
	raw := mattermostCacheSnapshot(snapshot, observedAt)
	if raw.Server.LastSyncedAt != 1234 || raw.Channels[0].DisplayName != "Direct display" || raw.Channels[0].LastPostAt != 123 || raw.Memberships[0].UpdatedAt != 88 || !reflect.DeepEqual(raw.ChannelUsers["dm"], []string{"me", "peer"}) {
		t.Fatalf("raw snapshot = %#v", raw)
	}
	rebuilt, err := mattermostServiceSnapshot(raw)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt.Sections[0].Channels[0].DisplayName != "Alice" {
		t.Fatalf("rebuilt name = %#v", rebuilt.Sections)
	}
	if rebuilt.Sections[0].Channels[0].Channel.DisplayName != "Direct display" {
		t.Fatalf("channel display name lost: %#v", rebuilt.Sections)
	}
	if rebuilt.Sections[0].Channels[0].Channel.LastPostAt != 123 {
		t.Fatalf("channel last post boundary lost: %#v", rebuilt.Sections)
	}
	if rebuilt.Sections[0].Channels[0].Membership.UpdatedAt != 88 {
		t.Fatalf("membership revision lost: %#v", rebuilt.Sections)
	}
}
