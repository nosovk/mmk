package export

import (
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/ui/messages"
)

func TestThreadToMarkdown_ParentOnly(t *testing.T) {
	parent := messages.MessageItem{UserName: "alice", DateStr: "2026-05-18", Timestamp: "3:04 PM", Text: "hello world"}
	got := ThreadToMarkdown(parent, nil, nil, nil)
	if !strings.Contains(got, "**alice** — 2026-05-18 3:04 PM") {
		t.Errorf("missing header, got:\n%s", got)
	}
	if !strings.Contains(got, "hello world") {
		t.Errorf("missing body, got:\n%s", got)
	}
}

func TestThreadToMarkdown_ParentAndReplies(t *testing.T) {
	parent := messages.MessageItem{UserName: "alice", DateStr: "2026-05-18", Timestamp: "3:04 PM", Text: "question?"}
	replies := []messages.MessageItem{
		{UserName: "bob", DateStr: "2026-05-18", Timestamp: "3:05 PM", Text: "answer!"},
	}
	got := ThreadToMarkdown(parent, replies, nil, nil)
	if strings.Count(got, "**alice**") != 1 || strings.Count(got, "**bob**") != 1 {
		t.Errorf("expected both users, got:\n%s", got)
	}
}

func TestThreadToMarkdown_Attachments(t *testing.T) {
	parent := messages.MessageItem{
		UserName: "alice", DateStr: "2026-05-18", Timestamp: "3:04 PM", Text: "see attached",
		Attachments: []messages.Attachment{
			{Kind: "image", Name: "screenshot.png", URL: "https://files.slack.com/img.png"},
			{Kind: "file", URL: "https://files.slack.com/doc.pdf"},
		},
	}
	got := ThreadToMarkdown(parent, nil, nil, nil)
	if !strings.Contains(got, "[screenshot.png](https://files.slack.com/img.png)") {
		t.Errorf("missing named image attachment, got:\n%s", got)
	}
	if !strings.Contains(got, "[File](https://files.slack.com/doc.pdf)") {
		t.Errorf("missing unnamed file attachment, got:\n%s", got)
	}
}

func TestThreadToMarkdown_Reactions(t *testing.T) {
	parent := messages.MessageItem{
		UserName: "alice", DateStr: "2026-05-18", Timestamp: "3:04 PM", Text: "great idea",
		Reactions: []messages.ReactionItem{
			{Emoji: "thumbsup", Count: 3},
		},
	}
	got := ThreadToMarkdown(parent, nil, nil, nil)
	if !strings.Contains(got, "3") {
		t.Errorf("missing reaction count, got:\n%s", got)
	}
}

func TestThreadToMarkdown_PreservesMattermostCommonMark(t *testing.T) {
	body := "**important** See [runbook](https://mattermost.example/runbook) and ask @alice."
	parent := messages.MessageItem{UserName: "alice", DateStr: "2026-05-18", Timestamp: "3:04 PM", Text: body}
	got := ThreadToMarkdown(parent, nil, nil, nil)
	want := "**alice** — 2026-05-18 3:04 PM\n" + body + "\n"
	if got != want {
		t.Fatalf("saved Mattermost thread changed CommonMark:\n got %q\nwant %q", got, want)
	}
}
