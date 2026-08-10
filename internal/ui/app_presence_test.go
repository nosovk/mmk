// internal/ui/app_presence_test.go
//
// Phase 0 characterization tests for the presence/DND state mutations
// on App: applyOptimisticStatus (called by the presence menu and
// custom-snooze flow) and the StatusChangeMsg handler in Update
// (called by the WS event router). These pin behavior for the future
// presenceController extraction.
package ui

import (
	"testing"
	"time"

	"github.com/nosovk/mmk/internal/ui/presencemenu"
)

func TestApplyOptimisticStatusSetActive(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	// Seed: was previously away with DND on, just to be sure we don't
	// stomp DND fields with this action.
	a.presence.byTeam["T1"] = workspaceStatus{
		Presence:   "away",
		DNDEnabled: true,
		DNDEndTS:   time.Now().Add(5 * time.Minute),
	}

	a.presence.Apply(a.activeServerID, presencemenu.ActionSetActive, 0)

	got := a.presence.byTeam["T1"]
	if got.Presence != "active" {
		t.Errorf("Presence: want %q, got %q", "active", got.Presence)
	}
	if !got.DNDEnabled {
		t.Error("ActionSetActive must not clear DNDEnabled")
	}
	if got.DNDEndTS.IsZero() {
		t.Error("ActionSetActive must not clear DNDEndTS")
	}
}

func TestApplyOptimisticStatusSetAway(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	a.presence.byTeam["T1"] = workspaceStatus{Presence: "active"}

	a.presence.Apply(a.activeServerID, presencemenu.ActionSetAway, 0)

	if got := a.presence.byTeam["T1"].Presence; got != "away" {
		t.Errorf("Presence: want %q, got %q", "away", got)
	}
}

func TestApplyOptimisticStatusSnoozeSetsDND(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	a.presence.byTeam["T1"] = workspaceStatus{Presence: "active"}

	before := time.Now()
	a.presence.Apply(a.activeServerID, presencemenu.ActionSnooze, 30)
	after := time.Now()

	got := a.presence.byTeam["T1"]
	if !got.DNDEnabled {
		t.Fatal("expected DNDEnabled=true after Snooze")
	}
	// DNDEndTS must be ~30m from now (loose bounds to handle test jitter).
	earliest := before.Add(30 * time.Minute)
	latest := after.Add(30 * time.Minute)
	if got.DNDEndTS.Before(earliest) || got.DNDEndTS.After(latest) {
		t.Errorf("DNDEndTS=%v out of expected window [%v..%v]",
			got.DNDEndTS, earliest, latest)
	}
	// Presence is unchanged by Snooze.
	if got.Presence != "active" {
		t.Errorf("Snooze must not change Presence; got %q", got.Presence)
	}
}

func TestApplyOptimisticStatusEndDNDClearsDND(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	a.presence.byTeam["T1"] = workspaceStatus{
		Presence:   "active",
		DNDEnabled: true,
		DNDEndTS:   time.Now().Add(10 * time.Minute),
	}

	a.presence.Apply(a.activeServerID, presencemenu.ActionEndDND, 0)

	got := a.presence.byTeam["T1"]
	if got.DNDEnabled {
		t.Error("DNDEnabled must be false after ActionEndDND")
	}
	if !got.DNDEndTS.IsZero() {
		t.Errorf("DNDEndTS must be zero after ActionEndDND; got %v", got.DNDEndTS)
	}
	// Presence is unchanged by EndDND.
	if got.Presence != "active" {
		t.Errorf("EndDND must not change Presence; got %q", got.Presence)
	}
}

func TestApplyOptimisticStatusInitializesMissingTeamEntry(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	// statusByTeam["T1"] does not exist yet.

	a.presence.Apply(a.activeServerID, presencemenu.ActionSetAway, 0)

	got, ok := a.presence.byTeam["T1"]
	if !ok {
		t.Fatal("expected entry for T1 to be created on first apply")
	}
	if got.Presence != "away" {
		t.Errorf("Presence: want %q, got %q", "away", got.Presence)
	}
}

func TestStatusChangeMsgUpdatesPerTeamCache(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	dndEnd := time.Now().Add(1 * time.Hour)

	// Status arrives for a DIFFERENT workspace: must update the cache
	// but must NOT touch the active workspace's status bar.
	_, _ = a.Update(StatusChangeMsg{
		TeamID:     "T2",
		Presence:   "away",
		DNDEnabled: true,
		DNDEndTS:   dndEnd,
	})

	got, ok := a.presence.byTeam["T2"]
	if !ok {
		t.Fatal("expected statusByTeam[T2] to be set")
	}
	if got.Presence != "away" || !got.DNDEnabled || !got.DNDEndTS.Equal(dndEnd) {
		t.Errorf("T2 cache mismatch: got %+v", got)
	}
	// T1 (active) must not have been written.
	if _, exists := a.presence.byTeam["T1"]; exists {
		t.Error("active T1 entry should not be created by a T2 status change")
	}
}

func TestStatusChangeMsgForActiveTeamStartsDNDTickerOnce(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	dndEnd := time.Now().Add(1 * time.Hour)

	// First StatusChangeMsg for the active team with DND enabled in the
	// future flips dndTickerOn and schedules a tick.
	_, cmd := a.Update(StatusChangeMsg{
		TeamID:     "T1",
		Presence:   "away",
		DNDEnabled: true,
		DNDEndTS:   dndEnd,
	})
	if !a.presence.dndTickerOn {
		t.Fatal("expected dndTickerOn=true after first DND-active StatusChangeMsg")
	}
	if cmd == nil {
		t.Fatal("expected a tea.Cmd (the DNDTick scheduler) to be returned")
	}

	// A second identical StatusChangeMsg must NOT re-schedule (the
	// dndTickerOn guard prevents parallel tick chains).
	_, cmd2 := a.Update(StatusChangeMsg{
		TeamID:     "T1",
		Presence:   "away",
		DNDEnabled: true,
		DNDEndTS:   dndEnd,
	})
	if cmd2 != nil {
		t.Error("second DND-active StatusChangeMsg must not schedule another tick")
	}
}

func TestStatusChangeMsgExpiredDNDDoesNotStartTicker(t *testing.T) {
	a := NewApp()
	a.activeServerID = "T1"
	// DNDEndTS already in the past → no tick should be scheduled even
	// though DNDEnabled is true (the renderer falls back to presence).
	_, cmd := a.Update(StatusChangeMsg{
		TeamID:     "T1",
		Presence:   "away",
		DNDEnabled: true,
		DNDEndTS:   time.Now().Add(-1 * time.Minute),
	})
	if a.presence.dndTickerOn {
		t.Error("ticker must not start for already-expired DND")
	}
	if cmd != nil {
		t.Error("expired DND must not return a tick cmd")
	}
}
