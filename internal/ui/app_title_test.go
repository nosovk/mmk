package ui

import "testing"

func TestFormatTitle(t *testing.T) {
	tests := []struct {
		name     string
		initials string
		active   int
		other    int
		want     string
	}{
		{"no unreads", "SW", 0, 0, "mmk SW"},
		{"active only", "SW", 3, 0, "mmk SW (3)"},
		{"other only", "SW", 0, 1, "mmk SW +1"},
		{"both", "SW", 3, 1, "mmk SW (3) +1"},
		{"max values", "SW", 99, 99, "mmk SW (99) +99"},
		{"fallback initials", "?", 0, 0, "mmk ?"},
		{"fallback initials with unreads", "?", 5, 2, "mmk ? (5) +2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := formatTitle(tt.initials, tt.active, tt.other)
			if got != tt.want {
				t.Errorf("formatTitle(%q, %d, %d) = %q want %q",
					tt.initials, tt.active, tt.other, got, tt.want)
			}
		})
	}
}

// TestComputeWindowTitle covers the full pipeline from raw inputs to
// rendered title. The pre-bootstrap branch (empty activeTeamID) MUST
// produce a stable "mmk" regardless of stray non-zero counts -- a
// brief window during startup where the readers may already return
// data but the active workspace isn't yet selected.
func TestComputeWindowTitle(t *testing.T) {
	tests := []struct {
		name          string
		activeTeamID  string
		workspaceName string
		activeUnreads int
		otherUnreads  int
		want          string
	}{
		{"pre-bootstrap (no active workspace)", "", "", 0, 0, "mmk"},
		{"pre-bootstrap ignores stray inputs", "", "Ignored", 99, 99, "mmk"},
		{"active workspace, no unreads", "T1", "SWAP", 0, 0, "mmk SW"},
		{"active workspace, with unreads", "T1", "SWAP", 3, 0, "mmk SW (3)"},
		{"active + other workspaces have unreads", "T1", "SWAP", 3, 2, "mmk SW (3) +2"},
		{"only other workspaces have unreads", "T1", "SWAP", 0, 1, "mmk SW +1"},
		{"empty workspace name yields fallback initials", "T1", "", 0, 0, "mmk ?"},
		{"single-word name uses first 2 chars", "T1", "Home", 1, 0, "mmk HO (1)"},
		{"multi-word name uses initials of first two words", "T1", "StratusGrid Eng", 5, 1, "mmk SE (5) +1"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := computeWindowTitle(tt.activeTeamID, tt.workspaceName, tt.activeUnreads, tt.otherUnreads)
			if got != tt.want {
				t.Errorf("computeWindowTitle(%q, %q, %d, %d) = %q want %q",
					tt.activeTeamID, tt.workspaceName, tt.activeUnreads, tt.otherUnreads, got, tt.want)
			}
		})
	}
}
