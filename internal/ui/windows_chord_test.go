// internal/ui/windows_chord_test.go
//
// ctrl+w window-command chord tests (window-management design §4).
// The prefix arms a pending sub-state in normal mode; the next key is
// dispatched through handleWindowChord. Unmapped keys and Esc cancel
// silently; any mode change disarms (SetMode).
package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/nosovk/mmk/internal/ui/wintree"
)

func pressCtrlW(a *App) {
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: 'w', Mod: tea.ModCtrl})
}

func press(a *App, r rune) tea.Cmd {
	return handleNormalMode(a, tea.KeyPressMsg{Code: r, Text: string(r)})
}

func TestChord_CtrlWThenVSplits(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	if !a.pendingWinCmd {
		t.Fatal("ctrl+w should arm the pending window-command state")
	}
	_ = press(a, 'v')
	if a.pendingWinCmd {
		t.Fatal("chord key should disarm the pending state")
	}
	if a.wins.Len() != 2 {
		t.Fatalf("Len = %d, want 2 after ctrl+w v", a.wins.Len())
	}
}

func TestChord_SplitCloseCycleOnly(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	_ = press(a, 's')
	if a.wins.Len() != 2 {
		t.Fatalf("ctrl+w s: Len = %d, want 2", a.wins.Len())
	}
	pressCtrlW(a)
	_ = press(a, 'w')
	first := a.wins.Leaves()[0]
	if a.focusedWin != first {
		t.Fatalf("ctrl+w w should cycle focus to %v, got %v", first, a.focusedWin)
	}
	pressCtrlW(a)
	_ = press(a, 'q')
	if a.wins.Len() != 1 {
		t.Fatalf("ctrl+w q: Len = %d, want 1", a.wins.Len())
	}
	_ = a.splitWindow(wintree.SplitStacked)
	pressCtrlW(a)
	_ = press(a, 'o')
	if a.wins.Len() != 1 {
		t.Fatalf("ctrl+w o: Len = %d, want 1", a.wins.Len())
	}
}

func TestChord_DirectionalFocus(t *testing.T) {
	a := newWideTestApp(t)
	left := a.focusedWin
	pressCtrlW(a)
	_ = press(a, 'v') // focus moves to new right-hand window
	right := a.focusedWin
	pressCtrlW(a)
	_ = press(a, 'h')
	if a.focusedWin != left {
		t.Fatalf("ctrl+w h: focused %v, want %v", a.focusedWin, left)
	}
	pressCtrlW(a)
	_ = press(a, 'l')
	if a.focusedWin != right {
		t.Fatalf("ctrl+w l: focused %v, want %v", a.focusedWin, right)
	}
}

func TestChord_UnmappedKeyCancelsSilently(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	_ = press(a, 'z')
	if a.pendingWinCmd {
		t.Fatal("unmapped chord key should cancel the pending state")
	}
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1 (no side effects)", a.wins.Len())
	}
	// Esc cancels too:
	pressCtrlW(a)
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if a.pendingWinCmd {
		t.Fatal("esc should cancel the pending state")
	}
	if a.wins.Len() != 1 {
		t.Fatalf("Len = %d, want 1", a.wins.Len())
	}
}

func TestChord_EscMidChordDoesNotLeakToEscapeCase(t *testing.T) {
	// Pins the intercept ORDER: the pending sub-state must swallow
	// Esc before the main switch, whose Escape case would close a
	// visible thread panel.
	a := newWideTestApp(t)
	a.threadVisible = true
	pressCtrlW(a)
	_ = handleNormalMode(a, tea.KeyPressMsg{Code: tea.KeyEscape})
	if !a.threadVisible {
		t.Fatal("esc mid-chord must be swallowed, not close the thread panel")
	}
	if a.pendingWinCmd {
		t.Fatal("esc should still cancel the pending state")
	}
}

// statusHint renders the statusbar wide enough that the help hint is
// never dropped for lack of gap space, and strips ANSI for Contains.
func statusHint(a *App) string {
	return ansi.Strip(a.statusbar.View(200))
}

func TestChord_HintLifecycle(t *testing.T) {
	a := newWideTestApp(t)
	if !strings.Contains(statusHint(a), "? for keybindings") {
		t.Fatalf("precondition: default hint missing, got %q", statusHint(a))
	}

	// Armed: prefix hint shown.
	pressCtrlW(a)
	if !strings.Contains(statusHint(a), "ctrl+w …") {
		t.Fatalf("armed: want %q hint, got %q", "ctrl+w …", statusHint(a))
	}

	// (a) Completed chord restores the default hint (not blanked).
	_ = press(a, 'v')
	if strings.Contains(statusHint(a), "ctrl+w …") {
		t.Fatalf("after chord: prefix hint must clear, got %q", statusHint(a))
	}
	if !strings.Contains(statusHint(a), "? for keybindings") {
		t.Fatalf("after chord: default hint must be restored, got %q", statusHint(a))
	}

	// (b) SetMode disarm restores the default hint too.
	pressCtrlW(a)
	a.SetMode(ModeConfirm)
	if strings.Contains(statusHint(a), "ctrl+w …") {
		t.Fatalf("after SetMode disarm: prefix hint must clear, got %q", statusHint(a))
	}
	if !strings.Contains(statusHint(a), "? for keybindings") {
		t.Fatalf("after SetMode disarm: default hint must be restored, got %q", statusHint(a))
	}
}

func TestChord_CtrlWCtrlWCycles(t *testing.T) {
	a := newWideTestApp(t)
	_ = a.splitWindow(wintree.SplitSideBySide) // focus on second window
	pressCtrlW(a)
	pressCtrlW(a) // vim: ctrl+w ctrl+w == ctrl+w w
	if a.focusedWin != a.wins.Leaves()[0] {
		t.Fatalf("ctrl+w ctrl+w should cycle, focused %v", a.focusedWin)
	}
	if a.pendingWinCmd {
		t.Fatal("pending state should be disarmed after the chord")
	}
}

func TestChord_ModeChangeDisarms(t *testing.T) {
	a := newWideTestApp(t)
	pressCtrlW(a)
	a.SetMode(ModeConfirm) // e.g. global ctrl+c intercept fires mid-chord
	if a.pendingWinCmd {
		t.Fatal("any mode change must disarm the pending chord state")
	}
}
