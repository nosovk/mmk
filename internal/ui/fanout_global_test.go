// internal/ui/fanout_global_test.go
//
// Workspace-wide / global mutation fan-out (window-management
// Phase 3, Task 3): mutations that are not scoped to a channel —
// user names, channel names, custom emoji, theme invalidation, the
// shared spinner frame, selection clearing — must reach EVERY
// window's model, not just the focused one.
package ui

import (
	"testing"

	"github.com/nosovk/mmk/internal/ui/imgrender"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/sidebar"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

func TestGlobalFanout_UserNamesReachAllWindows(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	a.SetUserNames(map[string]string{"U7": "newname"})
	if got := a.winModels[w1].ResolveUserName("U7"); got != "newname" {
		t.Fatalf("w1 ResolveUserName = %q", got)
	}
	if got := a.winModels[w2].ResolveUserName("U7"); got != "newname" {
		t.Fatalf("w2 ResolveUserName = %q", got)
	}
}

func TestGlobalFanout_PatchUserNameReachesAllWindows(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	// UserResolvedMsg gates on TeamID == a.activeServerID; both are ""
	// in the test app, so the patch applies.
	_, _ = a.Update(UserResolvedMsg{UserID: "U9", DisplayName: "zed"})
	if got := a.winModels[w1].ResolveUserName("U9"); got != "zed" {
		t.Fatalf("w1 ResolveUserName after patch = %q", got)
	}
	if got := a.winModels[w2].ResolveUserName("U9"); got != "zed" {
		t.Fatalf("w2 ResolveUserName after patch = %q", got)
	}
}

func TestGlobalFanout_ChannelNamesBumpAllVersions(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	a.SetChannels([]sidebar.ChannelItem{{ID: "C9", Name: "random", Type: "channel"}})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("SetChannels must push channel names into every window model")
	}
}

func TestGlobalFanout_CustomEmojiBumpsAllVersions(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	a.SetCustomEmoji(map[string]string{"partyparrot": "https://e.example/p.gif"})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("SetCustomEmoji must update every window model's emoji customs")
	}
}

func TestGlobalFanout_ThemeInvalidationBumpsAllVersions(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	a.invalidateAllWinModelCaches()
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("theme invalidation must bump every window model's version")
	}
}

func TestGlobalFanout_EmojiInvalidateReachesAllWindows(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	// emojiInvalidateMsg is the debounced wholesale emoji-cache
	// invalidation dispatched after EmojiImageReadyMsg arrivals.
	_, _ = a.Update(emojiInvalidateMsg{})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("emoji invalidation must reach every window model")
	}
}

func TestGlobalFanout_ImageReadyReachesUnfocusedWindow(t *testing.T) {
	a, w1, _ := twoWindowApp(t)
	v1 := a.winModels[w1].Version()
	// Key == "" drives the legacy wholesale-invalidation path, which
	// self-gates on the model's channel NAME: only w1 views "general".
	_, _ = a.Update(imgrender.ImageReadyMsg{Channel: "general", TS: "1.0", Key: ""})
	if a.winModels[w1].Version() == v1 {
		t.Fatal("ImageReady for general must reach the unfocused window viewing it")
	}
}

func TestGlobalFanout_SpinnerFrameReachesAllLoadingWindows(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	a.winModels[w1].SetLoading(true)
	a.winModels[w2].SetLoading(true)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	// SpinnerTickMsg is owned by the bootstrap reducer registered in
	// App.Update's dispatch chain; this is the real production path.
	_, _ = a.Update(SpinnerTickMsg{})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("spinner frame must reach all loading windows")
	}
}

// Regression (reviewer-flagged): backfill sets loading on the
// then-focused model; if focus moves before completion, the old
// focused-only IsLoading gate killed the tick chain, freezing the
// other window's spinner glyph. The gate must consider EVERY
// window's loading state.
func TestGlobalFanout_SpinnerTickSurvivesUnfocusedLoading(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	a.winModels[w1].SetLoading(true)  // unfocused window still loading
	a.winModels[w2].SetLoading(false) // focused window done
	v1 := a.winModels[w1].Version()
	_, cmd := a.Update(SpinnerTickMsg{})
	if cmd == nil {
		t.Fatal("tick chain must stay alive while any window's model is loading")
	}
	if a.winModels[w1].Version() == v1 {
		t.Fatal("the loading unfocused window must receive the new spinner frame")
	}
}

func TestGlobalFanout_ClearSelectionsReachesAllWindows(t *testing.T) {
	a := newWideTestApp(t)
	_, _ = a.Update(ChannelSelectedMsg{ID: "C1", Name: "general", Type: "channel"})
	_, _ = a.Update(MessagesLoadedMsg{ChannelID: "C1", Messages: testMessageItems(3)})
	w1 := a.focusedWin
	// Render while w1 is the (single, focused) window so its model's
	// selection geometry (chrome height, line index) populates.
	_ = a.View()
	// Pane-local y=3 is the first content row past the 2-row chrome
	// (mirrors app_selection_test.go's terminal y=4 minus the border).
	a.winModels[w1].BeginSelectionAt(3, 2)
	if !a.winModels[w1].HasSelection() {
		t.Fatal("precondition: selection did not take in window 1")
	}
	_ = a.splitWindow(wintree.SplitSideBySide) // focus moves to the new window
	w2 := a.focusedWin
	a.clearSelections()
	for _, w := range []wintree.LeafID{w1, w2} {
		if a.winModels[w].HasSelection() {
			t.Fatalf("window %v still has a selection after clearSelections", w)
		}
	}
}

// Startup-wiring forwarders (SetAvatarFunc / SetImageContext /
// SetEmojiContext) can re-fire after startup (reconnect); they must
// hit every window. SetEmojiContext and SetImageContext invalidate
// the model cache, so a version bump is observable.
func TestGlobalFanout_EmojiContextBumpsAllVersions(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	a.SetEmojiContext(messages.EmojiContext{Cells: 2})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("SetEmojiContext must reach every window model")
	}
}

func TestGlobalFanout_ImageContextBumpsAllVersions(t *testing.T) {
	a, w1, w2 := twoWindowApp(t)
	v1, v2 := a.winModels[w1].Version(), a.winModels[w2].Version()
	a.SetImageContext(imgrender.ImageContext{})
	if a.winModels[w1].Version() == v1 || a.winModels[w2].Version() == v2 {
		t.Fatal("SetImageContext must reach every window model")
	}
}
