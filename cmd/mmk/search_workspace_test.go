// cmd/mmk/search_workspace_test.go
//
// Tests for the SearchWorkspace service closure wiring.
package main

import (
	"testing"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ui"
)

// TestSearchWorkspaceNoActiveWorkspaceReturnsErr verifies the
// SearchWorkspace closure never returns a nil msg when no workspace
// is active: a nil msg would leave the ctrl+f modal spinner stuck
// (the reducer only clears loading on a WorkspaceSearchResultsMsg).
// TestLookupUserCachedDoesNotMutateMap pins the read-only contract of
// lookupUserCached: the search path runs in a bubbletea cmd goroutine,
// where a write to the shared UserNames map would race the UI goroutine
// (see the concurrent-map-writes note on userResolver.Request). A DB
// hit must be returned WITHOUT being memoized into the map.
func TestLookupUserCachedDoesNotMutateMap(t *testing.T) {
	db, err := cache.New(":memory:")
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	defer db.Close()
	if err := db.UpsertWorkspace(cache.Workspace{ID: "T1", Name: "Test"}); err != nil {
		t.Fatalf("UpsertWorkspace: %v", err)
	}
	if err := db.UpsertUser(cache.User{ID: "U1", WorkspaceID: "T1", Name: "alice", DisplayName: "Alice"}); err != nil {
		t.Fatalf("UpsertUser: %v", err)
	}

	userNames := map[string]string{}
	name, ok := lookupUserCached("U1", userNames, db)
	if !ok || name != "Alice" {
		t.Fatalf("lookupUserCached = (%q, %v), want (\"Alice\", true)", name, ok)
	}
	if len(userNames) != 0 {
		t.Fatalf("lookupUserCached mutated the map: %v", userNames)
	}

	// Map hits still work and still don't mutate.
	userNames = map[string]string{"U2": "Bob"}
	name, ok = lookupUserCached("U2", userNames, db)
	if !ok || name != "Bob" {
		t.Fatalf("map hit = (%q, %v), want (\"Bob\", true)", name, ok)
	}
	if len(userNames) != 1 || userNames["U2"] != "Bob" {
		t.Fatalf("map changed: %v", userNames)
	}
}

func TestSearchWorkspaceNoActiveWorkspaceReturnsErr(t *testing.T) {
	fn := searchWorkspaceFunc(newWorkspaceRouter(), nil, "15:04")
	msg := fn("deploy")
	res, ok := msg.(ui.WorkspaceSearchResultsMsg)
	if !ok {
		t.Fatalf("got %T, want ui.WorkspaceSearchResultsMsg", msg)
	}
	if res.Query != "deploy" || res.Err == nil {
		t.Fatalf("res = %+v, want Query=%q and non-nil Err", res, "deploy")
	}
}
