package messages

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
	"github.com/slack-go/slack"

	"github.com/nosovk/mmk/internal/ui/messages/blockkit"
)

// renderedFor builds a model with a single message, runs buildCache
// at the given width, and returns the joined plain-text rendering of
// the first cache entry. Mirrors the existing test-helper pattern in
// plain_test.go and selection_test.go.
func renderedFor(t *testing.T, msg MessageItem, width int) string {
	t.Helper()
	m := New([]MessageItem{msg}, "general")
	m.buildCache(width)
	if len(m.cache) == 0 {
		t.Fatal("buildCache produced no entries")
	}
	var lines []string
	for _, e := range m.cache {
		if e.msgIdx == 0 {
			lines = e.linesNormal
			break
		}
	}
	if lines == nil {
		t.Fatal("no entry with msgIdx 0 in cache")
	}
	return ansi.Strip(strings.Join(lines, "\n"))
}

func TestRenderMessagePlainEmitsBlockKitContent(t *testing.T) {
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "github",
		UserID:    "U-BOT",
		Text:      "PR opened",
		Timestamp: "1:23 PM",
		Blocks: []blockkit.Block{
			blockkit.HeaderBlock{Text: "Pull Request opened"},
			blockkit.SectionBlock{Text: "Pay system: bug fix for retry logic"},
		},
	}
	plain := renderedFor(t, msg, 100)
	if !strings.Contains(plain, "PR opened") {
		t.Errorf("missing message body Text: %q", plain)
	}
	if !strings.Contains(plain, "Pull Request opened") {
		t.Errorf("missing header block: %q", plain)
	}
	if !strings.Contains(plain, "Pay system: bug fix for retry logic") {
		t.Errorf("missing section block: %q", plain)
	}
}

func TestRenderMessagePlainEmitsLegacyAttachment(t *testing.T) {
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "pagerduty",
		UserID:    "U-BOT",
		Text:      "alert",
		Timestamp: "1:23 PM",
		LegacyAttachments: []blockkit.LegacyAttachment{{
			Color: "danger",
			Title: "Service down",
			Text:  "checkout-svc 5xx > 1%",
		}},
	}
	plain := renderedFor(t, msg, 100)
	if !strings.Contains(plain, "Service down") {
		t.Errorf("missing legacy title: %q", plain)
	}
	if !strings.Contains(plain, "█") {
		t.Errorf("missing color stripe glyph: %q", plain)
	}
}

// TestRenderMessagePlainPreservesPlainTextRendering guards against
// regressions: a message with no blocks/attachments renders exactly
// as before this task (text body present, no extra spacing).
func TestRenderMessagePlainPreservesPlainTextRendering(t *testing.T) {
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "alice",
		Text:      "hello world",
		Timestamp: "1:00 PM",
	}
	plain := renderedFor(t, msg, 100)
	if !strings.Contains(plain, "hello world") {
		t.Errorf("plain text body missing: %q", plain)
	}
}

func TestRenderMessagePlainAppendsHintWhenInteractive(t *testing.T) {
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "deploy-bot",
		Timestamp: "1:23 PM",
		Blocks: []blockkit.Block{
			blockkit.SectionBlock{Text: "Deploy?"},
			blockkit.ActionsBlock{Elements: []blockkit.ActionElement{
				{Kind: "button", Label: "Approve"},
			}},
		},
	}
	plain := renderedFor(t, msg, 100)
	if !strings.Contains(plain, "↗ open in Slack to interact") {
		t.Errorf("expected hint line, got %q", plain)
	}
}

func TestRenderMessagePlainOmitsHintWhenNotInteractive(t *testing.T) {
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "github",
		Timestamp: "1:23 PM",
		Blocks: []blockkit.Block{
			blockkit.SectionBlock{Text: "PR merged"},
		},
	}
	plain := renderedFor(t, msg, 100)
	if strings.Contains(plain, "↗ open in Slack to interact") {
		t.Errorf("hint should not appear for non-interactive message: %q", plain)
	}
}

// TestMessageTextSource_NoBlocksReturnsRawText: the common case for
// user-typed messages. With no rich_text block on hand, the helper
// just hands msg.Text through unchanged.
func TestMessageTextSource_NoBlocksReturnsRawText(t *testing.T) {
	msg := MessageItem{Text: "hello world"}
	if got := MessageTextSource(msg); got != "hello world" {
		t.Errorf("got %q, want %q", got, "hello world")
	}
}

// TestMessageTextSource_NonRichTextBlocksReturnRawText: messages that
// have block-kit content (header/section/etc.) but no rich_text body
// continue to use msg.Text for the body. Those block types render
// separately via the blockkit renderer.
func TestMessageTextSource_NonRichTextBlocksReturnRawText(t *testing.T) {
	msg := MessageItem{
		Text: "PR opened",
		Blocks: []blockkit.Block{
			blockkit.HeaderBlock{Text: "Pull Request opened"},
			blockkit.SectionBlock{Text: "details"},
		},
	}
	if got := MessageTextSource(msg); got != "PR opened" {
		t.Errorf("got %q, want %q", got, "PR opened")
	}
}

// TestMessageTextSource_RichTextOverridesLossyText: the bug-fix
// contract. When a message has a rich_text block, the helper
// reconstructs the body from it instead of using Slack's
// newline-stripped text fallback.
func TestMessageTextSource_RichTextOverridesLossyText(t *testing.T) {
	rt := blockkit.RichTextBlock{Elements: []slack.RichTextElement{
		&slack.RichTextSection{
			Type: slack.RTESection,
			Elements: []slack.RichTextSectionElement{
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "line1"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "\n"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "line2"},
			},
		},
	}}
	msg := MessageItem{
		Text:   "line1 line2", // Slack's lossy fallback (newline → space)
		Blocks: []blockkit.Block{rt},
	}
	got := MessageTextSource(msg)
	if !strings.Contains(got, "line1\nline2") {
		t.Errorf("got %q, want it to contain %q (newline preserved from rich_text)", got, "line1\nline2")
	}
}

// TestRenderMessagePlainRichTextProducesMultipleLines: integration
// test for the bug — a rendered rich_text-bodied message must
// produce multiple body lines, not one squashed line. Mirrors the
// GitHub Pending Review reproduction.
func TestRenderMessagePlainRichTextProducesMultipleLines(t *testing.T) {
	rt := blockkit.RichTextBlock{Elements: []slack.RichTextElement{
		&slack.RichTextSection{
			Type: slack.RTESection,
			Elements: []slack.RichTextSectionElement{
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "PR #1: fix retries"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "\n"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "PR #2: docs typo"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "\n"},
				&slack.RichTextSectionTextElement{Type: slack.RTSEText, Text: "PR #3: bump deps"},
			},
		},
	}}
	msg := MessageItem{
		TS:        "1700000000.000000",
		UserName:  "github",
		UserID:    "U-BOT",
		Text:      "PR #1: fix retries  PR #2: docs typo  PR #3: bump deps", // lossy
		Timestamp: "1:23 PM",
		Blocks:    []blockkit.Block{rt},
	}
	plain := renderedFor(t, msg, 100)
	for _, want := range []string{"PR #1: fix retries", "PR #2: docs typo", "PR #3: bump deps"} {
		if !strings.Contains(plain, want) {
			t.Errorf("missing %q in rendered output: %q", want, plain)
		}
	}
	// Each PR should be on its own line — count occurrences of "PR #"
	// at line boundaries.
	prLines := 0
	for _, line := range strings.Split(plain, "\n") {
		if strings.Contains(line, "PR #") {
			prLines++
		}
	}
	if prLines < 3 {
		t.Errorf("expected >=3 lines containing 'PR #' (one per PR), got %d. Full output:\n%s", prLines, plain)
	}
}
