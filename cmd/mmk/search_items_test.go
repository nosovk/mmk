// cmd/mmk/search_items_test.go
//
// Tests for searchResultItems: the pure conversion from slack-go
// search.messages matches to searchresults.Item (mrkdwn flattening,
// DM channel-name resolution, thread-TS extraction).
package main

import (
	"fmt"
	"testing"
	"time"

	"github.com/slack-go/slack"
)

func searchMatch(chID, chName, user, text string) slack.SearchMessage {
	m := slack.SearchMessage{}
	m.Channel.ID = chID
	m.Channel.Name = chName
	m.Username = user
	m.Timestamp = "1700000001.000100"
	m.Text = text
	return m
}

func testResolveUser(id string) (string, bool) {
	switch id {
	case "U0AC7857QKC":
		return "ayush", true
	case "U081FJDPAHF":
		return "ray", true
	}
	return "", false
}

func testResolveChannel(id string) (string, bool) {
	if id == "C42" {
		return "ops", true
	}
	return "", false
}

// testNow is the fixed "current time" used by conversion tests so
// today/this-year/prior-year date formatting is deterministic.
var testNow = time.Date(2026, time.June, 11, 12, 0, 0, 0, time.Local)

func convert(matches ...slack.SearchMessage) []searchItemsOut {
	items := searchResultItems(matches, "15:04", testNow, testResolveUser, testResolveChannel, nil)
	out := make([]searchItemsOut, len(items))
	for i, it := range items {
		out[i] = searchItemsOut{it.ChannelName, it.Text, it.IsDM}
	}
	return out
}

type searchItemsOut struct {
	ChannelName string
	Text        string
	IsDM        bool
}

func TestSearchResultItemsFlattensMrkdwn(t *testing.T) {
	got := convert(searchMatch("C1", "general", "grant",
		"cc <@U081FJDPAHF> see <https://linear.app/x|ABC-1> in <#C42>"))
	want := "cc @ray see ABC-1 in #ops"
	if got[0].Text != want {
		t.Errorf("Text = %q, want %q", got[0].Text, want)
	}
	if got[0].IsDM {
		t.Error("regular channel must not be flagged IsDM")
	}
}

func TestSearchResultItemsFlattensUsergroupMentions(t *testing.T) {
	m := searchMatch("C1", "general", "grant", "cc <!subteam^S0TESTGRP01>")
	items := searchResultItems(
		[]slack.SearchMessage{m},
		"15:04",
		testNow,
		testResolveUser,
		testResolveChannel,
		map[string]string{"S0TESTGRP01": "platform-team"},
	)
	if items[0].Text != "cc @platform-team" {
		t.Errorf("Text = %q, want %q", items[0].Text, "cc @platform-team")
	}
}

func TestSearchResultItemsUnknownMentionFallsBack(t *testing.T) {
	got := convert(searchMatch("C1", "general", "grant", "hi <@U0ZZZZZZZZZ>"))
	if want := "hi @U0ZZZZZZZZZ"; got[0].Text != want {
		t.Errorf("Text = %q, want %q", got[0].Text, want)
	}
}

func TestSearchResultItemsResolvesDMChannelName(t *testing.T) {
	// DM hits carry the counterpart's raw user ID as the channel name
	// (screenshot: "#U0AC7857QKC").
	got := convert(searchMatch("D9XYZ", "U0AC7857QKC", "ayush", "hello"))
	if got[0].ChannelName != "ayush" {
		t.Errorf("ChannelName = %q, want %q", got[0].ChannelName, "ayush")
	}
	if !got[0].IsDM {
		t.Error("DM result must be flagged IsDM")
	}
}

func TestSearchResultItemsUnresolvedDMKeepsID(t *testing.T) {
	got := convert(searchMatch("D9XYZ", "U0ZZZZZZZZZ", "who", "hello"))
	if got[0].ChannelName != "U0ZZZZZZZZZ" {
		t.Errorf("ChannelName = %q, want raw ID kept", got[0].ChannelName)
	}
	if !got[0].IsDM {
		t.Error("unresolved DM (D-prefixed channel ID) must still be IsDM")
	}
}

func TestSearchResultItemsUserIDNameWithoutDMChannelID(t *testing.T) {
	// Some responses carry a C-prefixed (or empty) channel ID but a
	// user-ID-shaped name; detection must work off the name shape too.
	got := convert(searchMatch("", "U0AC7857QKC", "ayush", "hello"))
	if got[0].ChannelName != "ayush" || !got[0].IsDM {
		t.Errorf("got %+v, want resolved DM name", got[0])
	}
}

func TestSearchResultItemsRegularChannelNameNotTreatedAsUser(t *testing.T) {
	// A channel literally named like uppercase text must not trip the
	// user-ID heuristic unless it actually resolves as a user.
	got := convert(searchMatch("C7", "UPDATES", "grant", "hello"))
	if got[0].ChannelName != "UPDATES" || got[0].IsDM {
		t.Errorf("got %+v, want untouched channel name, IsDM=false", got[0])
	}
}

func TestSearchResultItemsThreadTSFromPermalink(t *testing.T) {
	m := searchMatch("C1", "general", "grant", "reply")
	m.Permalink = "https://x.slack.com/archives/C1/p1700000002000200?thread_ts=1700000001.000100&cid=C1"
	items := searchResultItems([]slack.SearchMessage{m}, "15:04", testNow, testResolveUser, testResolveChannel, nil)
	if items[0].ThreadTS != "1700000001.000100" {
		t.Errorf("ThreadTS = %q, want %q", items[0].ThreadTS, "1700000001.000100")
	}
}

func TestSearchResultItemsTimestampIncludesDate(t *testing.T) {
	slackTS := func(tm time.Time) string {
		return fmt.Sprintf("%d.000100", tm.Unix())
	}
	cases := []struct {
		name string
		when time.Time
		want string
	}{
		{
			name: "today shows time only",
			when: time.Date(2026, time.June, 11, 20, 1, 0, 0, time.Local),
			want: "20:01",
		},
		{
			name: "same year shows month and day",
			when: time.Date(2026, time.May, 19, 20, 1, 0, 0, time.Local),
			want: "May 19, 20:01",
		},
		{
			name: "prior year includes year",
			when: time.Date(2025, time.May, 19, 20, 1, 0, 0, time.Local),
			want: "May 19 2025, 20:01",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := searchMatch("C1", "general", "grant", "hello")
			m.Timestamp = slackTS(tc.when)
			items := searchResultItems([]slack.SearchMessage{m}, "15:04", testNow, testResolveUser, testResolveChannel, nil)
			if items[0].Timestamp != tc.want {
				t.Errorf("Timestamp = %q, want %q", items[0].Timestamp, tc.want)
			}
		})
	}
}

func TestSearchResultItemsTimestampUnparseableFallsBack(t *testing.T) {
	m := searchMatch("C1", "general", "grant", "hello")
	m.Timestamp = "garbage"
	items := searchResultItems([]slack.SearchMessage{m}, "15:04", testNow, testResolveUser, testResolveChannel, nil)
	if items[0].Timestamp != "garbage" {
		t.Errorf("Timestamp = %q, want raw value kept", items[0].Timestamp)
	}
}
