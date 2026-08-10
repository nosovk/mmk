package ui

import "testing"

func TestServerLoadingOverlayTracksDuplicateNamesByIDAndCompletesFailures(t *testing.T) {
	b := newWorkspaceBootstrap()
	b.SetServers([]LoadingServer{{ID: "s1", Name: "Same"}, {ID: "s2", Name: "Same"}})
	b.MarkServerFailed("s1")
	if !b.IsLoading() {
		t.Fatal("one pending server should keep overlay visible")
	}
	if b.states[0].Status != "failed" || b.states[1].Status != "connecting" {
		t.Fatalf("states = %#v", b.states)
	}
	b.MarkServerFailed("s2")
	if b.IsLoading() {
		t.Fatal("all terminal failures should dismiss immediately")
	}
}
