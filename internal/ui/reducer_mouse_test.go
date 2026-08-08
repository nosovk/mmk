// internal/ui/reducer_mouse_test.go
package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/help"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

// TestReduceMouseWheel_ScrollsActiveModal verifies that when a modal
// overlay is open, a mouse-wheel notch scrolls the items inside the
// modal (advancing its selection by mouseWheelLines) instead of
// scrolling the panel under the cursor on the main tab behind it.
func TestReduceMouseWheel_ScrollsActiveModal(t *testing.T) {
	app := NewApp()

	// Populate the help modal with enough rows to scroll through.
	entries := make([]help.Entry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, help.Entry{Key: "k", Desc: "desc"})
	}
	app.help.SetEntries(entries)
	app.help.Open()
	app.SetMode(ModeHelp)

	if got := app.help.Selected(); got != 0 {
		t.Fatalf("precondition: help selection should start at 0, got %d", got)
	}

	// A wheel-down notch (X anywhere on screen) should move the modal
	// selection down by mouseWheelLines (default 3), not touch panels.
	reduceMouseWheel(app, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5})
	if got, want := app.help.Selected(), app.mouseWheelLines; got != want {
		t.Fatalf("wheel down: help selection = %d, want %d", got, want)
	}

	// A wheel-up notch should move the selection back up, clamping at 0.
	reduceMouseWheel(app, tea.MouseWheelMsg{Button: tea.MouseWheelUp, X: 5})
	if got := app.help.Selected(); got != 0 {
		t.Fatalf("wheel up: help selection = %d, want 0", got)
	}
}

// TestReduceMouseWheel_NoModalLeavesModalUntouched is a guard that the
// modal-routing branch only fires when a modal mode is active: with the
// app in normal mode, a wheel notch must not advance the (open) help
// modal's selection through the modal path.
func TestReduceMouseWheel_NoModalLeavesModalUntouched(t *testing.T) {
	app := NewApp()

	entries := make([]help.Entry, 0, 20)
	for i := 0; i < 20; i++ {
		entries = append(entries, help.Entry{Key: "k", Desc: "desc"})
	}
	app.help.SetEntries(entries)
	// Note: NOT opening the modal / not setting ModeHelp; mode stays Normal.

	reduceMouseWheel(app, tea.MouseWheelMsg{Button: tea.MouseWheelDown, X: 5})
	if got := app.help.Selected(); got != 0 {
		t.Fatalf("normal mode: help selection should stay 0, got %d", got)
	}
}

// TestReduceMouseClick_IgnoredInMessagesRegionWhenSplit pins the
// interim Phase 2 guard: with multiple windows, PanelAt still maps
// the whole messages region to the single live pane, so a click on a
// placeholder window would begin a drag selection in the live channel
// at bogus coordinates. Clicks (and therefore drags, which can only
// start from the click branch) must be swallowed until per-window
// mouse routing lands in Phase 4.
func TestReduceMouseClick_IgnoredInMessagesRegionWhenSplit(t *testing.T) {
	a := newTestAppWithMessages(t)
	a.width = 200 // wide enough for two ≥42-col windows (wintree.MinWidth)
	if cmd := a.splitWindow(wintree.SplitSideBySide); cmd != nil {
		t.Fatal("split refused at width 200")
	}
	_ = a.View() // recompute layout bands for the split layout

	pressX := a.layout.sidebarEnd + 2
	pressY := 4
	_, _ = a.Update(tea.MouseClickMsg{X: pressX, Y: pressY, Button: tea.MouseLeft})
	_, _ = a.Update(tea.MouseMotionMsg{X: pressX + 10, Y: pressY + 1, Button: tea.MouseLeft})
	_, _ = a.Update(tea.MouseReleaseMsg{X: pressX + 10, Y: pressY + 1, Button: tea.MouseLeft})
	if a.messagepane.HasSelection() {
		t.Fatal("split mode: click+drag in the messages region must not start a selection")
	}
}
