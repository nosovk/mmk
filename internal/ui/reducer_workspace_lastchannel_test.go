package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/sidebar"
)

// TestWorkspaceReadyRestoresLastChannel is the headline of the
// restore-last-channel feature: on the InitialActive workspace, startup
// opens the persisted most-recently-visited channel (LastChannelID) rather
// than always snapping to the sidebar's first entry, falling back to the
// first channel when there's no recorded visit or it no longer exists.
func TestWorkspaceReadyRestoresLastChannel(t *testing.T) {
	selectOnReady := func(lastID string) (ChannelSelectedMsg, bool) {
		a := NewApp()
		_, _ = a.Update(tea.WindowSizeMsg{Width: 200, Height: 60})
		_, cmd := a.Update(WorkspaceReadyMsg{
			TeamID:        "T1",
			InitialActive: true,
			Channels: []sidebar.ChannelItem{
				{ID: "C1", Name: "general", Type: "channel"},
				{ID: "C2", Name: "random", Type: "channel"},
			},
			LastChannelID: lastID,
		})
		if cmd == nil {
			return ChannelSelectedMsg{}, false
		}
		return findChannelSelected(cmd())
	}

	// Persisted last channel is restored.
	if sel, ok := selectOnReady("C2"); !ok || sel.ID != "C2" {
		t.Errorf("LastChannelID=C2: want C2 opened, got ok=%v id=%q", ok, sel.ID)
	}
	// No recorded visit (e.g. first run) -> first channel.
	if sel, ok := selectOnReady(""); !ok || sel.ID != "C1" {
		t.Errorf("no LastChannelID: want C1, got ok=%v id=%q", ok, sel.ID)
	}
	// Channel was archived/left since the last visit -> first channel.
	if sel, ok := selectOnReady("CGONE"); !ok || sel.ID != "C1" {
		t.Errorf("stale LastChannelID: want C1 fallback, got ok=%v id=%q", ok, sel.ID)
	}
}
