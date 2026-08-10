package messages

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
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
