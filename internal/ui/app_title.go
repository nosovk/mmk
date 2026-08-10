package ui

import (
	"fmt"

	"github.com/nosovk/mmk/internal/ui/workspace"
)

// computeWindowTitle builds the mmk terminal-window-title string from
// pre-computed inputs. Pure (no I/O, no App reference); table-driven
// tests in app_title_test.go cover the full output matrix.
//
// The caller (App.notifyReadStateChanged) sources each input from the
// collaborator that already owns it:
//
//   - activeServerID:   App.activeServerID
//   - workspaceName:  App.workspaceRail.NameByID(activeServerID)
//   - activeUnreads:  App.sidebar.UnreadChannelCount() (mute-filtered)
//   - otherUnreads:   App.workspaceRail.OtherUnreadCount(activeServerID)
//
// Pre-bootstrap, activeServerID is "" and the function returns a bare
// "mmk" regardless of any stray non-zero counts. See
// docs/superpowers/specs/2026-05-21-tab-title-unread-indicator-design.md.
func computeWindowTitle(activeServerID, workspaceName string, activeUnreads, otherUnreads int) string {
	if activeServerID == "" {
		return "mmk"
	}
	return formatTitle(workspace.WorkspaceInitials(workspaceName), activeUnreads, otherUnreads)
}

// formatTitle assembles the final title string from already-derived
// pieces. Separated from computeWindowTitle so the assembly format is
// testable independent of input sourcing.
func formatTitle(initials string, active, other int) string {
	out := "mmk " + initials
	if active > 0 {
		out += fmt.Sprintf(" (%d)", active)
	}
	if other > 0 {
		out += fmt.Sprintf(" +%d", other)
	}
	return out
}
