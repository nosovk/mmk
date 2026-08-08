package thread

import (
	"fmt"
	"testing"

	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/styles"
)

// TestSetThreadOpensAtBottom guards that opening a thread scrolls the
// viewport to the newest reply (like the Slack app), even when the new
// thread's newest-reply index matches the previously-viewed thread's
// snapped selection — the case where the missing snap-state reset left the
// viewport stuck at the top.
func TestSetThreadOpensAtBottom(t *testing.T) {
	styles.Apply("dark", config.Theme{})
	t.Cleanup(func() { styles.Apply("dark", config.Theme{}) })

	parent := messages.MessageItem{TS: "100.0", UserName: "alice", Text: "parent"}
	mk := func(n int) []messages.MessageItem {
		r := make([]messages.MessageItem, n)
		for i := range r {
			r[i] = messages.MessageItem{
				TS:        fmt.Sprintf("%d.000000", 1700000000+i),
				UserName:  "bob",
				Text:      fmt.Sprintf("reply number %d", i),
				Timestamp: "10:30 AM",
			}
		}
		return r
	}
	const h, w = 12, 80

	m := New()
	// Thread A: a long thread opens scrolled to the bottom.
	m.SetThread(parent, mk(25), "C1", "100.0")
	m.View(h, w)
	if m.vp.YOffset() == 0 {
		t.Fatalf("thread A: expected to open scrolled to the bottom, got YOffset=0")
	}

	// Simulate scrolling thread A up to the top: leaves hasSnapped=true with
	// snappedSelection == the newest-reply index.
	m.vp.SetYOffset(0)
	m.hasSnapped = true
	m.snappedSelection = m.selected

	// Thread B with the same reply count (so the newest-reply index matches).
	// Pre-fix this skipped the re-snap and left the viewport at the top.
	m.SetThread(parent, mk(25), "C2", "200.0")
	if m.hasSnapped {
		t.Fatalf("SetThread must reset hasSnapped so the next View re-snaps to the new selection")
	}
	m.View(h, w)
	if m.vp.YOffset() == 0 {
		t.Errorf("thread B opened at the top (YOffset=0); want scrolled to the latest reply")
	}
}
