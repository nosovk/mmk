// internal/ui/help_render_test.go
package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/help"
)

// Regression: when only the help modal is open, App.View must re-render
// as the modal selection changes. The screen-memoization cache keys off
// the base panels/status/size only; if help is not treated as an active
// overlay, a selection change returns a stale cached frame and the modal
// appears frozen (keys "do nothing" on screen even though state moves).
func TestHelpModal_ViewUpdatesOnSelectionChange(t *testing.T) {
	a := NewApp()
	a.Update(tea.WindowSizeMsg{Width: 120, Height: 40})

	entries := make([]help.Entry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, help.Entry{Key: "k", Desc: "binding"})
	}
	a.help.SetEntries(entries)
	a.help.Open()
	a.SetMode(ModeHelp)

	first := a.View().Content

	// Move the selection the way a keypress would.
	a.help.HandleKey("j")

	second := a.View().Content

	if first == second {
		t.Fatalf("help modal View did not change after selection moved; " +
			"stale memoized frame served (overlayActive omits help)")
	}
}
