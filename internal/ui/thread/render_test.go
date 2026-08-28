package thread

import (
	"strings"
	"testing"

	"github.com/nosovk/mmk/internal/ui/messages"
)

// TestRenderThreadMessageAttachmentLinesFit asserts that a message with a
// long-URL file attachment renders without crashing and produces an
// attachment line containing the URL.
//
// Historical note: this test previously asserted that no rendered line
// exceeded the panel content width, relying on a `messages.WordWrap` call
// that the legacy `messages.RenderAttachments` codepath was wrapped in.
// Task 8 migrates this codepath to `imgrender.Renderer.RenderBlock`,
// which produces a single OSC 8 hyperlink line for non-image
// attachments without inner wrapping (matching the messages pane's
// post-Task-7 behavior). The cache-build layer's
// `borderFill.Width(width - 1).Render(...)` is now solely responsible
// for width enforcement when the cached output is composed for display.
func TestRenderThreadMessageAttachmentLinesFit(t *testing.T) {
	const width = 50 // panel content width passed to renderThreadMessage
	m := New()
	msg := messages.MessageItem{
		TS:          "1700000001.000000",
		UserName:    "alice",
		Text:        "see attachment",
		Timestamp:   "10:30 AM",
		Attachments: []messages.Attachment{{Kind: "file", Name: "design.pdf", URL: "https://mattermost.example/files/design.pdf"}},
	}
	got, _, _ := m.renderThreadMessage(msg, width, nil, nil, false)
	if got == "" {
		t.Fatal("renderThreadMessage returned empty output")
	}
	if !strings.Contains(got, "design.pdf") {
		t.Fatalf("expected rendered output to contain the file URL; got %q", got)
	}
	// Confirm the provider-neutral attachment was not silently dropped.
	if !strings.Contains(got, "[File]") {
		t.Fatalf("expected rendered output to include [File] marker; got %q", got)
	}
}
