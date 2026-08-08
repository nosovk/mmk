package ui

import (
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ui/help"
)

func TestNormalMode_ColonEntersCommandMode(t *testing.T) {
	a := NewApp()
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: ':', Text: ":"})
	if a.mode != ModeCommand {
		t.Fatalf("mode = %v, want ModeCommand", a.mode)
	}
}

func TestNormalMode_CtrlWNoLongerOpensWorkspaceFinder(t *testing.T) {
	a := NewApp()
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
	if a.mode == ModeWorkspaceFinder {
		t.Fatal("ctrl+w must not open the workspace finder (reclaimed as window prefix)")
	}
	if a.mode != ModeNormal {
		t.Fatalf("mode = %v, want ModeNormal (ctrl+w is a no-op in phase 1)", a.mode)
	}
}

func TestHelp_StillListsWorkspaceFinderViaWS(t *testing.T) {
	entries := help.FromKeyMap(DefaultKeyMap())
	found := false
	for _, e := range entries {
		// Phase 2: ctrl+w is the window-command prefix; the only help
		// entry allowed to carry it is that one. It must never again
		// advertise the workspace finder.
		if e.Key == "ctrl+w" && e.Desc != "window commands" {
			t.Fatalf("help entry %+v advertises ctrl+w for something other than window commands", e)
		}
		if e.Key == ":ws" && e.Desc == "switch workspace" {
			found = true
		}
	}
	if !found {
		t.Fatal("help entries missing {Key: \":ws\", Desc: \"switch workspace\"}")
	}
}
