package messages

import (
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
	"github.com/nosovk/mmk/internal/ui/selection"
)

func TestMessageItemCanonicalIdentityAndTime(t *testing.T) {
	slack := MessageItem{TS: "1700000000.123456", ThreadTS: "1699999999.000001"}
	if slack.MessageID() != slack.TS || slack.EventTime() != slack.TS || slack.RootMessageID() != slack.ThreadTS {
		t.Fatalf("Slack helpers regressed: %#v", slack)
	}
	mm := MessageItem{ID: "opaque-post", CreatedAt: 1700000000123, RootID: "opaque-root", TS: "must-not-win"}
	if mm.MessageID() != "opaque-post" || mm.EventTime() != "1700000000123" || mm.RootMessageID() != "opaque-root" {
		t.Fatalf("Mattermost helpers=%q %q %q", mm.MessageID(), mm.EventTime(), mm.RootMessageID())
	}
}

func TestMattermostDateAndTimeUseUnixMilliseconds(t *testing.T) {
	created := time.Date(2026, time.August, 10, 15, 4, 5, 0, time.Local).UnixMilli()
	item := MessageItem{ID: "p1", CreatedAt: created}
	if got, want := item.Date(), time.UnixMilli(created).Format("2006-01-02"); got != want {
		t.Fatalf("Date=%q want %q", got, want)
	}
	if got, want := item.DisplayTime(), time.UnixMilli(created).Format("3:04 PM"); got != want {
		t.Fatalf("DisplayTime=%q want %q", got, want)
	}
}

func TestMattermostPlainRendererPreservesLiteralTextAndStripsControls(t *testing.T) {
	input := "**literal** <https://example.com|label>\nUnicode: Привет 🚀\x1b[31mRED\x1b[0m\x00"
	got := RenderMattermostPlain(input, 24)
	plain := ansi.Strip(got)
	unwrapped := strings.ReplaceAll(plain, "\n", "")
	for _, want := range []string{"**literal**", "<https://example.com|label>", "Unicode:", "Привет", "🚀", "RED"} {
		if !strings.Contains(unwrapped, want) {
			t.Fatalf("rendered=%q missing %q", plain, want)
		}
	}
	if strings.Contains(got, "\x1b[31m") || strings.ContainsRune(got, '\x00') {
		t.Fatalf("unsafe controls survived: %q", got)
	}
	if got := RenderMattermostPlain("", 20); got != "" {
		t.Fatalf("empty=%q want empty", got)
	}
}

func TestDefaultMessageRenderingIsLiteralMattermostText(t *testing.T) {
	m := New([]MessageItem{{ID: "p1", UserName: "alice", Text: "*literal* <https://example.com|label>"}}, "general")
	out := ansi.Strip(m.View(10, 80))
	if !strings.Contains(out, "*literal* <https://example.com|label>") {
		t.Fatalf("default message text was interpreted instead of rendered literally: %q", out)
	}
}

func TestMessageRendererSanitizesRemoteUsernameControls(t *testing.T) {
	name := "Al\x1b[31mice\x1b]8;;https://evil.example\x07link\x1b]8;;\x1b\\ bell\x07 esc\x1b c1\u009b31m"
	m := New([]MessageItem{{ID: "p1", UserName: name, Text: "body"}}, "general")
	out := m.View(10, 60)
	plain := ansi.Strip(out)
	if !strings.Contains(plain, "Alicelink bell esc131m") {
		t.Fatalf("plain=%q", plain)
	}
	for _, unsafe := range []string{"\x07", "\u009b", "https://evil.example"} {
		if strings.Contains(plain, unsafe) {
			t.Fatalf("unsafe %q survived in %q", unsafe, out)
		}
	}
}

func TestPrependMessagesDedupesOpaqueIDsAndPreservesOrderSelectionAndAnchor(t *testing.T) {
	m := New([]MessageItem{
		{ID: "current-a", CreatedAt: 1000, UserName: "a", Text: strings.Repeat("a ", 20)},
		{ID: "selected", CreatedAt: 1000, UserName: "b", Text: strings.Repeat("b ", 20)},
		{ID: "current-c", CreatedAt: 1001, UserName: "c", Text: strings.Repeat("c ", 20)},
	}, "general")
	m.SelectByID("selected")
	_ = m.View(8, 24)
	selectedStartBefore := m.selectedStartLine - m.yOffset

	m.PrependMessages([]MessageItem{
		{ID: "older-z", CreatedAt: 1000, Text: "first incoming"},
		{ID: "current-c", CreatedAt: 999, Text: "duplicate anywhere"},
		{ID: "older-a", CreatedAt: 1000, Text: "second incoming same millisecond"},
		{ID: "older-z", CreatedAt: 998, Text: "duplicate incoming"},
	})
	got := m.Messages()
	wantIDs := []string{"older-z", "older-a", "current-a", "selected", "current-c"}
	for i, want := range wantIDs {
		if got[i].MessageID() != want {
			t.Fatalf("ids[%d]=%q want %q", i, got[i].MessageID(), want)
		}
	}
	selected, ok := m.SelectedMessage()
	if !ok || selected.MessageID() != "selected" {
		t.Fatalf("selection=%#v ok=%v", selected, ok)
	}
	_ = m.View(8, 24)
	if got := m.selectedStartLine - m.yOffset; got != selectedStartBefore {
		t.Fatalf("selected visual y=%d want %d", got, selectedStartBefore)
	}
	if m.OldestID() != "older-z" {
		t.Fatalf("OldestID=%q", m.OldestID())
	}
}

func TestReplaceMessagesPreservingPositionKeepsScrolledSelectionAndTextSelection(t *testing.T) {
	m := New([]MessageItem{{ID: "a", Text: "alpha"}, {ID: "b", Text: "bravo " + strings.Repeat("wrap ", 20)}, {ID: "c", Text: "charlie"}}, "general")
	m.SelectByID("b")
	_ = m.View(8, 24)
	beforeY := m.selectedStartLine - m.yOffset
	m.selRange = selection.Range{Start: selection.Anchor{MessageID: "a"}, End: selection.Anchor{MessageID: "b"}, Active: true}
	m.hasSelection = true
	m.ReplaceMessagesPreservingPosition([]MessageItem{{ID: "a", Text: "alpha live"}, {ID: "b", Text: "bravo live " + strings.Repeat("wrap ", 20)}, {ID: "c", Text: "charlie live"}, {ID: "d", Text: "delta"}})
	selected, _ := m.SelectedMessage()
	if selected.MessageID() != "b" {
		t.Fatalf("selected=%q", selected.MessageID())
	}
	_ = m.View(8, 24)
	if got := m.selectedStartLine - m.yOffset; got != beforeY {
		t.Fatalf("visual y=%d want %d", got, beforeY)
	}
	if !m.hasSelection {
		t.Fatal("text selection over retained IDs was cleared")
	}
}

func TestReplaceMessagesPreservingPositionKeepsBottomFollowAndClearsMissingSelection(t *testing.T) {
	m := New([]MessageItem{{ID: "a"}, {ID: "b"}}, "general")
	m.selRange = selection.Range{Start: selection.Anchor{MessageID: "missing"}, End: selection.Anchor{MessageID: "b"}, Active: true}
	m.hasSelection = true
	m.ReplaceMessagesPreservingPosition([]MessageItem{{ID: "a"}, {ID: "b"}, {ID: "c"}})
	selected, _ := m.SelectedMessage()
	if selected.MessageID() != "c" {
		t.Fatalf("bottom selection=%q want c", selected.MessageID())
	}
	if m.hasSelection {
		t.Fatal("selection referencing missing ID survived")
	}
}

func TestReconcileRecentPagePreservesOlderPrefixAndReplacesAuthoritativeSuffix(t *testing.T) {
	m := New([]MessageItem{{ID: "old"}, {ID: "p1", Text: "cached 1"}, {ID: "missing"}, {ID: "p3", Text: "cached 3"}}, "general")
	m.SelectByID("p1")
	_ = m.View(8, 24)
	m.ReconcileRecentPage([]string{"p1", "missing", "p3"}, []string{"p1", "p2", "p3"}, nil, []MessageItem{{ID: "p1", Text: "live 1"}, {ID: "p2", Text: "live 2"}, {ID: "p3", Text: "live 3"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"old", "p1", "p2", "p3"}) {
		t.Fatalf("ids=%v", got)
	}
	if m.Messages()[1].Text != "live 1" {
		t.Fatalf("overlap not updated: %#v", m.Messages()[1])
	}
	selected, _ := m.SelectedMessage()
	if selected.MessageID() != "p1" {
		t.Fatalf("selected=%q", selected.MessageID())
	}
}

func TestReconcileRecentPagePreservesOlderPagesLoadedWhileRecentInFlight(t *testing.T) {
	m := New([]MessageItem{{ID: "older1"}, {ID: "older2"}, {ID: "cached1"}, {ID: "cached2"}}, "general")
	m.ReconcileRecentPage([]string{"cached1", "cached2"}, []string{"cached1", "live2"}, nil, []MessageItem{{ID: "cached1", Text: "updated"}, {ID: "live2"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older1", "older2", "cached1", "live2"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconcileOlderPageReplacesCapturedSegmentBeforeAnchor(t *testing.T) {
	m := New([]MessageItem{{ID: "older"}, {ID: "p1", Text: "cached"}, {ID: "p3"}, {ID: "anchor"}, {ID: "new"}}, "general")
	m.SelectByID("anchor")
	_ = m.View(8, 24)
	m.ReconcileOlderPage("anchor", []string{"p1", "p3"}, []string{"p1", "p2"}, nil, []MessageItem{{ID: "p1", Text: "updated"}, {ID: "p2"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "p1", "p2", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
	if m.Messages()[1].Text != "updated" {
		t.Fatal("edit not reconciled")
	}
	selected, _ := m.SelectedMessage()
	if selected.MessageID() != "anchor" {
		t.Fatalf("selected=%q", selected.MessageID())
	}
}

func TestReconcileRecentPageMovesGlobalLiveIDIntoAuthoritativeRange(t *testing.T) {
	m := New([]MessageItem{{ID: "moved", Text: "stale prefix"}, {ID: "older"}, {ID: "cached1"}, {ID: "cached2"}}, "general")
	m.ReconcileRecentPage([]string{"cached1", "cached2"}, []string{"cached1", "moved", "new"}, nil, []MessageItem{{ID: "cached1"}, {ID: "moved", Text: "live"}, {ID: "new"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "cached1", "moved", "new"}) {
		t.Fatalf("ids=%v", got)
	}
	if m.Messages()[2].Text != "live" {
		t.Fatalf("live content lost: %#v", m.Messages()[2])
	}
}

func TestReconcileOlderPageMovesGlobalLiveIDIntoRange(t *testing.T) {
	m := New([]MessageItem{{ID: "moved", Text: "stale prefix"}, {ID: "older"}, {ID: "cached"}, {ID: "anchor"}, {ID: "moved", Text: "stale suffix"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"moved", "page2"}, nil, []MessageItem{{ID: "moved", Text: "live"}, {ID: "page2"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "moved", "page2", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
	if m.Messages()[1].Text != "live" {
		t.Fatalf("live content lost: %#v", m.Messages()[1])
	}
}

func TestReconcileRecentTerminalReplacesEntireLoadedHistory(t *testing.T) {
	m := New([]MessageItem{{ID: "older-loaded"}, {ID: "cached"}}, "general")
	m.SelectByID("older-loaded")
	m.ReconcileRecentPage([]string{"cached"}, []string{"live"}, nil, []MessageItem{{ID: "live"}}, false)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"live"}) {
		t.Fatalf("ids=%v", got)
	}
	selected, _ := m.SelectedMessage()
	if selected.MessageID() != "live" {
		t.Fatalf("fallback=%q", selected.MessageID())
	}
	m.ReconcileRecentPage([]string{"live"}, nil, nil, nil, false)
	if len(m.Messages()) != 0 {
		t.Fatalf("authoritative empty=%v", messageIDs(m.Messages()))
	}
}

func TestReconcileOlderTerminalRemovesStalePrefix(t *testing.T) {
	m := New([]MessageItem{{ID: "stale1"}, {ID: "stale2"}, {ID: "cached"}, {ID: "anchor"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"oldest", "next"}, nil, []MessageItem{{ID: "oldest"}, {ID: "next"}}, false)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"oldest", "next", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
	m = New([]MessageItem{{ID: "stale"}, {ID: "cached"}, {ID: "anchor"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, nil, nil, nil, false)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"anchor", "new"}) {
		t.Fatalf("empty ids=%v", got)
	}
}

func TestReconcileOlderNonterminalPreservesUnrelatedOlderPrefix(t *testing.T) {
	m := New([]MessageItem{{ID: "older"}, {ID: "cached"}, {ID: "anchor"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"live"}, nil, []MessageItem{{ID: "live"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "live", "anchor"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconcileRecentRemovesAuthoritativeDeletedIDOutsideCapturedRange(t *testing.T) {
	m := New([]MessageItem{{ID: "deleted", Text: "visible stale"}, {ID: "older"}, {ID: "cached"}}, "general")
	m.ReconcileRecentPage([]string{"cached"}, []string{"live", "deleted"}, []string{"deleted"}, []MessageItem{{ID: "live"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "live"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconcileOlderRemovesAuthoritativeDeletedIDOutsideCapturedRange(t *testing.T) {
	m := New([]MessageItem{{ID: "older"}, {ID: "cached"}, {ID: "anchor"}, {ID: "deleted", Text: "visible stale"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"live", "deleted"}, []string{"deleted"}, []MessageItem{{ID: "live"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "live", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconcileOlderPreservesInclusiveNondeletedAnchorOnce(t *testing.T) {
	m := New([]MessageItem{{ID: "cached"}, {ID: "anchor", Text: "existing"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"anchor", "older"}, nil, []MessageItem{{ID: "older"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "anchor", "new"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconcileOlderRemovesDeletedInclusiveAnchor(t *testing.T) {
	m := New([]MessageItem{{ID: "cached"}, {ID: "anchor"}, {ID: "new"}}, "general")
	m.ReconcileOlderPage("anchor", []string{"cached"}, []string{"anchor", "older"}, []string{"anchor"}, []MessageItem{{ID: "older"}}, true)
	if got := messageIDs(m.Messages()); !reflect.DeepEqual(got, []string{"older", "new"}) {
		t.Fatalf("ids=%v", got)
	}
}

func TestReconciliationClearsTextSelectionWhenEditedTextInvalidatesAnchor(t *testing.T) {
	m := New([]MessageItem{{ID: "selected", Text: "first\nsecond long line"}, {ID: "newer", Text: "new"}}, "general")
	_ = m.View(10, 40)
	m.selRange = selection.Range{Start: selection.Anchor{MessageID: "selected", Line: 1, Col: 2}, End: selection.Anchor{MessageID: "selected", Line: 1, Col: 10}, Active: true}
	m.hasSelection = true
	m.ReconcileRecentPage([]string{"selected", "newer"}, []string{"selected", "newer"}, nil, []MessageItem{{ID: "selected", Text: "short"}, {ID: "newer", Text: "new"}}, true)
	if m.HasSelection() || m.SelectionText() != "" {
		t.Fatalf("invalid selection survived: active=%v text=%q", m.HasSelection(), m.SelectionText())
	}
}

func messageIDs(items []MessageItem) []string {
	out := make([]string, len(items))
	for i := range items {
		out[i] = items[i].MessageID()
	}
	return out
}
