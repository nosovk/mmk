// internal/ui/reducer_workspace.go
//
// Workspace-lifecycle reducer for App.Update (Phase 4k).
//
// Owns the nine Update arms that drive workspace activation,
// per-workspace data resolution echoes, and the sidebar / read-state
// refresh paths:
//
//	WorkspaceReadyMsg       - one of the configured workspaces
//	                          finished its initial connect. If
//	                          InitialActive (claimed exactly once),
//	                          activate it: apply theme, push
//	                          channels/users/emoji, open the first
//	                          channel. Always fires an initial
//	                          threads-list fetch.
//	WorkspaceSwitchedMsg    - user picked a different workspace.
//	                          Tear down per-workspace transient
//	                          state (threads view, edit, selection,
//	                          compose draft), push new channels/users/
//	                          emoji, apply theme, restore the last
//	                          channel viewed for this workspace.
//	ConversationOpenedMsg   - WS event: a DM/MPIM just opened.
//	                          Upsert into the active sidebar.
//	SectionsRefreshedMsg    - cache notice: sidebar sections were
//	                          reorganized. Re-push channel items
//	                          for the active workspace.
//	DMNameResolvedMsg       - DM display-name resolved (im.open
//	                          roundtrip): patch the sidebar entry
//	                          and re-render.
//	UserResolvedMsg         - per-user display-name resolved:
//	                          patch any in-history references in
//	                          both messages pane and thread panel.
//	UserExternalMsg         - per-user external/internal flag
//	                          resolved: update the cache + re-push
//	                          SetUserNames so styling refreshes.
//	ReadStateChangedMsg     - persistent read-state changed in the
//	                          cache: refresh sidebar + workspace rail.
//	CustomEmojisLoadedMsg   - per-workspace emoji list arrived.
//	                          Apply only to the active workspace.
//
// Free reducer (not controller-absorbed): these arms cooperate on
// the sidebar, messagepane, threadPanel, statusbar, channelFinder,
// workspaceRail, compose, threadCompose, threadsView, presence,
// bootstrap, themes, channel-state, and the threads service. No
// single existing controller owns this cross-section.
//
// WorkspaceReadyMsg and WorkspaceSwitchedMsg are extracted into
// reduceWorkspaceReady / reduceWorkspaceSwitched helpers to keep
// the dispatch switch readable. The two arms share substantial
// activation logic (theme apply, channels push, threads fetch);
// deduplication is deliberately NOT attempted in this commit
// (Phase 4 is behavior-preserving refactoring, not algorithmic
// changes). A follow-up could extract a shared
// activateWorkspace helper.
package ui

import (
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/styles"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

var reduceWorkspace reducerFunc = func(a *App, msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case ServerReadyMsg:
		state, accepted := a.acceptServerReadyState(m.Server)
		return reduceServerReady(a, state, accepted), true

	case ServerRefreshedMsg:
		state, accepted := a.acceptServerRefreshedState(m.Server)
		if !accepted {
			return nil, true
		}
		return reduceServerRefreshed(a, state), true

	case ServerSwitchedMsg:
		return reduceServerSwitched(a, a.acceptServerSwitchedState(m.Server)), true

	case ServerStateMsg:
		a.workspaceRail.SetState(string(m.ServerID), m.State, m.Err)
		if m.State == workspace.ItemStateError {
			a.bootstrap.MarkServerFailed(string(m.ServerID))
		}
		return nil, true

	case ServerConnectionStateMsg:
		a.workspaceRail.SetState(string(m.ServerID), m.State, nil)
		return nil, true

	case WorkspaceReadyMsg:
		return reduceWorkspaceReady(a, m), true

	case WorkspaceSwitchedMsg:
		return reduceWorkspaceSwitched(a, m), true

	case ConversationOpenedMsg:
		if m.TeamID == a.activeServerID {
			a.sidebar.UpsertItem(m.Item)
		}
		// Inactive-workspace events update WorkspaceContext.Channels
		// from the rtmEventHandler in cmd/mmk/main.go (Task 6);
		// App.Update only mutates the active sidebar.
		return nil, true

	case SectionsRefreshedMsg:
		if m.TeamID == a.activeServerID {
			a.SetChannels(m.Channels)
		}
		// Inactive-workspace events have already updated the
		// WorkspaceContext.Channels in cmd/mmk; App.Update only
		// mutates the active sidebar.
		return nil, true

	case DMNameResolvedMsg:
		items := a.sidebar.Items()
		for i := range items {
			if items[i].ID != m.ChannelID {
				continue
			}
			items[i].Name = m.DisplayName
			if m.IsBot && items[i].Type == "dm" {
				items[i].Type = "app"
			}
			break
		}
		a.SetChannels(items)
		return nil, true

	case UserResolvedMsg:
		if m.TeamID != a.activeServerID {
			return nil, true
		}
		for _, mp := range a.allWinModels() {
			mp.PatchUserName(m.UserID, m.DisplayName)
		}
		a.threadPanel.PatchUserName(m.UserID, m.DisplayName)
		// IsBot affects DM channel-type classification, but that's
		// orchestrated by DMNameResolvedMsg; this handler is only
		// the in-history name patch. IsBot is carried for forward
		// compatibility but not consumed here.
		return nil, true

	case UserExternalMsg:
		if a.externalUsers == nil {
			a.externalUsers = map[string]bool{}
		}
		if m.IsExternal {
			a.externalUsers[m.UserID] = true
		} else {
			delete(a.externalUsers, m.UserID)
		}
		if len(a.userNames) > 0 {
			a.SetUserNames(a.userNames)
		}
		return nil, true

	case ReadStateChangedMsg:
		_ = m
		// Persistent read state changed in the cache. Invalidate
		// the sidebar and refresh the workspace rail so both
		// re-read from the DB.
		a.notifyReadStateChanged()
		return nil, true

	case CustomEmojisLoadedMsg:
		if m.TeamID == a.activeServerID {
			a.SetCustomEmoji(m.CustomEmoji)
		}
		return nil, true

	case UserGroupsLoadedMsg:
		if m.TeamID == a.activeServerID {
			a.SetUserGroups(m.UserGroups)
		}
		return nil, true
	}
	return nil, false
}

func reduceServerReady(a *App, state ServerViewState, accepted bool) tea.Cmd {
	a.bootstrap.MarkServerReady(string(state.ServerID))
	if accepted {
		a.workspaceRail.SetUnread(string(state.ServerID), state.HasUnread)
		a.notifyReadStateChanged()
	}
	if !state.InitialActive || !a.bootstrap.ClaimInitialActive() {
		return nil
	}
	return activateServer(a, state, false)
}

func reduceServerRefreshed(a *App, state ServerViewState) tea.Cmd {
	a.workspaceRail.SetUnread(string(state.ServerID), state.HasUnread)
	if string(state.ServerID) != a.activeServerID {
		a.notifyReadStateChanged()
		return nil
	}
	a.sidebar.SetSectionsProvider(state.SectionsProvider)
	readState := state.ReadState
	a.sidebar.SetReadStateReader(func() map[string]cache.ReadState { return readState })
	a.SetChannels(state.Channels)
	a.channelFinder.SetItems(state.FinderItems)
	a.SetUserNames(state.UserNames)
	a.SetCurrentUserID(state.UserID)

	for _, channel := range state.Channels {
		if channel.ID == a.activeChannelID {
			a.sidebar.SelectByID(channel.ID)
			a.sidebar.SetActiveChannelID(channel.ID)
			a.notifyReadStateChanged()
			return nil
		}
	}
	if len(state.Channels) == 0 {
		a.activeChannelID = ""
		a.installFocusedMattermostScope(state.ServerID, "")
		a.sidebar.SetActiveChannelID("")
		a.messagepane.SetLoading(false)
		a.messagepane.SetMessages(nil)
		a.CloseThread()
		a.clearSelections()
		a.notifyReadStateChanged()
		return nil
	}

	target := state.Channels[0]
	cmd, _ := reduceChannelSelected(a, ChannelSelectedMsg{ID: target.ID, Name: target.Name, Type: target.Type})
	a.sidebar.SelectByID(target.ID)
	a.notifyReadStateChanged()
	return cmd
}

func reduceServerSwitched(a *App, state ServerViewState) tea.Cmd {
	return activateServer(a, state, false)
}

func activateServer(a *App, state ServerViewState, preserveSelection bool) tea.Cmd {
	wasMattermost := a.features.kind == ContextMattermost
	a.features = MattermostTask14Features()
	previousChannelID := ""
	if preserveSelection {
		previousChannelID = a.activeChannelID
	}
	a.view = ViewChannels
	a.sidebar.SetThreadsActive(false)
	a.sidebar.SetThreadsEnabled(false)
	a.threadsView.SetSummaries(nil)
	a.sidebar.SetThreadsUnreadCount(0)
	a.CloseThread()
	a.clearSelections()
	a.resetWindowTree()
	if wasMattermost {
		scope := a.newMattermostHistoryScope(state.ServerID, "")
		a.mattermostWindowScopes[a.focusedWin] = scope
		a.setFocusedMattermostScope(scope)
	}
	a.compose.Reset()
	a.SetMode(ModeNormal)
	a.compose.Blur()
	a.sidebar.SetSectionsProvider(state.SectionsProvider)
	readState := state.ReadState
	a.sidebar.SetReadStateReader(func() map[string]cache.ReadState { return readState })
	a.sidebar.ResetPresence()
	a.SetChannels(state.Channels)
	a.channelFinder.SetItems(state.FinderItems)
	a.SetExternalUsers(nil)
	a.SetUserNames(state.UserNames)
	a.SetCustomEmoji(nil)
	a.SetUserGroups(nil)
	a.SetCurrentUserID(state.UserID)
	a.activeServerID = string(state.ServerID)
	a.activeChannelID = ""
	a.workspaceRail.SelectByID(a.activeServerID)
	a.workspaceRail.SetUnread(a.activeServerID, state.HasUnread)
	if state.Theme != "" {
		styles.Apply(state.Theme, a.themeOverrides)
		a.invalidateAllWinModelCaches()
		a.threadPanel.InvalidateCache()
		a.sidebar.InvalidateCache()
		a.compose.RefreshStyles()
		a.threadCompose.RefreshStyles()
	}
	if state.SidebarWidth != 0 {
		a.sidebar.SetWidth(state.SidebarWidth)
	}
	if len(state.Channels) == 0 {
		a.messagepane.SetLoading(false)
		a.messagepane.SetMessages(nil)
		a.notifyReadStateChanged()
		return nil
	}
	target := state.Channels[0]
	for _, candidateID := range []string{previousChannelID, state.LastChannelID} {
		for _, channel := range state.Channels {
			if candidateID != "" && channel.ID == candidateID {
				target = channel
				break
			}
		}
		if target.ID == candidateID {
			break
		}
	}
	a.sidebar.SelectByID(target.ID)
	a.messagepane.SetLoading(true)
	a.messagepane.SetMessages(nil)
	a.notifyReadStateChanged()
	return func() tea.Msg { return ChannelSelectedMsg{ID: target.ID, Name: target.Name, Type: target.Type} }
}

func (a *App) ensureServerStateMaps() {
	if a.serverRevisions == nil {
		a.serverRevisions = map[string]uint64{}
	}
	if a.serverStates == nil {
		a.serverStates = map[string]ServerViewState{}
	}
	a.workspaceRail.SetUnreadReader(func() []string {
		var unread []string
		for serverID, state := range a.serverStates {
			if state.HasUnread {
				unread = append(unread, serverID)
			}
		}
		return unread
	})
}

func (a *App) acceptServerReadyState(state ServerViewState) (ServerViewState, bool) {
	a.ensureServerStateMaps()
	serverID := string(state.ServerID)
	currentRevision := a.serverRevisions[serverID]
	initialActive := state.InitialActive
	accepted := false
	switch {
	case state.Revision == 0 && currentRevision > 0:
		state = a.serverStates[serverID]
	case state.Revision == 0:
		a.serverStates[serverID] = state
		accepted = true
	case state.Revision > currentRevision:
		a.serverRevisions[serverID] = state.Revision
		a.serverStates[serverID] = state
		accepted = true
	case state.Revision > 0:
		state = a.serverStates[serverID]
	}
	state.InitialActive = initialActive
	return state, accepted
}

func (a *App) acceptServerRefreshedState(state ServerViewState) (ServerViewState, bool) {
	a.ensureServerStateMaps()
	serverID := string(state.ServerID)
	currentRevision := a.serverRevisions[serverID]
	if state.Revision == 0 {
		if currentRevision > 0 {
			return ServerViewState{}, false
		}
		a.serverStates[serverID] = state
		return state, true
	}
	if state.Revision <= currentRevision {
		return ServerViewState{}, false
	}
	a.serverRevisions[serverID] = state.Revision
	a.serverStates[serverID] = state
	return state, true
}

func (a *App) acceptServerSwitchedState(state ServerViewState) ServerViewState {
	a.ensureServerStateMaps()
	serverID := string(state.ServerID)
	currentRevision := a.serverRevisions[serverID]
	switch {
	case state.Revision == 0 && currentRevision > 0:
		return a.serverStates[serverID]
	case state.Revision == 0:
		a.serverStates[serverID] = state
		return state
	case state.Revision > currentRevision:
		a.serverRevisions[serverID] = state.Revision
		a.serverStates[serverID] = state
		return state
	case state.Revision > 0:
		return a.serverStates[serverID]
	}
	return state
}

// reduceWorkspaceReady handles WorkspaceReadyMsg. Extracted from
// the reduceWorkspace dispatch switch because the arm is ~60 lines
// (InitialActive activation branch alone is ~50).
func reduceWorkspaceReady(a *App, m WorkspaceReadyMsg) tea.Cmd {
	a.features = SlackFeatures()
	a.bootstrap.MarkReady(m.TeamName)
	if m.Domain != "" {
		a.workspaceDomains[m.TeamID] = m.Domain
	}
	var batch []tea.Cmd
	// Only the workspace flagged InitialActive auto-claims active
	// state. main.go computes this deterministically
	// (default_workspace match, else first to connect) so two
	// simultaneous WorkspaceReadyMsgs can no longer race on
	// (activeChannelID == "") and both claim. ClaimInitialActive
	// is a defensive one-shot guard against any future bug that
	// delivers InitialActive=true twice.
	if m.InitialActive && a.bootstrap.ClaimInitialActive() {
		a.view = ViewChannels
		a.sidebar.SetThreadsActive(false)
		a.threadsView.SetSummaries(nil)
		a.sidebar.SetThreadsUnreadCount(0)
		a.lastOpenedChannelID = ""
		a.lastOpenedThreadTS = ""
		// Apply the resolved theme for the initial active
		// workspace. Without this, per-workspace themes silently
		// revert to the global default on startup until the user
		// manually switches workspaces.
		if m.Theme != "" {
			styles.Apply(m.Theme, a.themeOverrides)
			a.invalidateAllWinModelCaches()
			a.threadPanel.InvalidateCache()
			a.sidebar.InvalidateCache()
			a.compose.RefreshStyles()
			a.threadCompose.RefreshStyles()
		}
		if m.SidebarWidth != 0 {
			a.sidebar.SetWidth(m.SidebarWidth)
		}
		a.sidebar.SetSectionsProvider(m.SectionsProvider)
		a.SetChannels(m.Channels)
		a.channelFinder.SetItems(m.FinderItems)
		// SetExternalUsers re-pushes user-names; calling
		// SetUserNames last is the canonical state.
		a.SetExternalUsers(m.ExternalUsers)
		a.SetUserNames(m.UserNames)
		a.SetCustomEmoji(m.CustomEmoji)
		a.SetUserGroups(m.UserGroups)
		// Route through the setter so messagepane/threadPanel also learn
		// the current user — production never calls SetCurrentUserID
		// otherwise, which would leave live self-reactions unstyled.
		a.SetCurrentUserID(m.UserID)
		a.activeServerID = m.TeamID
		pres, dndEnabled, dndEnd, _ := a.presence.Status(a.activeServerID)
		a.statusbar.SetStatus(pres, dndEnabled, dndEnd)
		a.workspaceRail.SelectByID(m.TeamID)
		if len(m.Channels) > 0 {
			// Restore the last-visited channel across restarts (persisted in
			// channel_visits). Falls back to the first sidebar entry when
			// there's no recorded visit or the channel no longer exists.
			target := m.Channels[0]
			if m.LastChannelID != "" {
				for _, ch := range m.Channels {
					if ch.ID == m.LastChannelID {
						target = ch
						break
					}
				}
			}
			a.sidebar.SelectByID(target.ID)
			a.messagepane.SetLoading(true)
			a.messagepane.SetMessages(nil)
			batch = append(batch, spinnerTickCmd())
			batch = append(batch, func() tea.Msg {
				return ChannelSelectedMsg{ID: target.ID, Name: target.Name, Type: target.Type}
			})
		}
	}
	// Initial threads-list fetch fires for every workspace as it
	// becomes ready; the result is gated by ThreadsListLoadedMsg's
	// TeamID == activeServerID check, so background fetches are
	// dropped without affecting the active sidebar.
	threads := a.threads
	team := ids.TeamID(m.TeamID)
	batch = append(batch, func() tea.Msg { return threads.ListFetch(team) })
	return tea.Batch(batch...)
}

// reduceWorkspaceSwitched handles WorkspaceSwitchedMsg. Extracted
// from the reduceWorkspace dispatch switch because the arm is ~87
// lines (tears down per-workspace transient state, applies new
// data, restores last-viewed channel).
func reduceWorkspaceSwitched(a *App, m WorkspaceSwitchedMsg) tea.Cmd {
	a.features = SlackFeatures()
	if a.compose.Uploading() || a.threadCompose.Uploading() {
		return a.uploadToastCmd("Upload in progress", 2*time.Second)
	}
	if m.Domain != "" {
		a.workspaceDomains[m.TeamID] = m.Domain
	}
	// Remember which channel we were on in the workspace we're
	// leaving so that switching back lands the user on the same
	// channel rather than always snapping to the sidebar's first
	// entry.
	if a.activeServerID != "" && a.activeChannelID != "" && a.activeServerID != m.TeamID {
		a.lastChannelByTeam[a.activeServerID] = a.activeChannelID
	}
	a.cancelEdit()
	// Always land in ViewChannels and drop any per-workspace
	// threads-view state so stale summaries / unread badges from
	// the previous workspace can't leak in. The sidebar cursor is
	// moved to the restored channel below (after SetChannels);
	// only fall back to the Threads row when the new workspace
	// has no channels at all.
	a.view = ViewChannels
	a.sidebar.SetThreadsActive(false)
	a.threadsView.SetSummaries(nil)
	a.sidebar.SetThreadsUnreadCount(0)
	a.lastOpenedChannelID = ""
	a.lastOpenedThreadTS = ""
	a.CloseThread()
	a.clearSelections()
	// The window tree + per-window models are per-workspace state:
	// collapse to a single empty window so no leaf carries a
	// cross-workspace channel. The queued ChannelSelectedMsg for
	// this workspace re-populates it.
	a.resetWindowTree()
	a.compose.Reset()
	a.statusbar.SetSyncing(false) // defensive: don't carry stale sync state across workspaces
	// resetWindowTree replaced the pane with a fresh empty model;
	// the queued ChannelSelectedMsg below paints it via the
	// three-tier dispatch. For empty workspaces (no Channels) the
	// pane's empty state is confirmed in the else branch below.
	a.SetMode(ModeNormal)
	a.compose.Blur()
	a.sidebar.SetSectionsProvider(m.SectionsProvider)
	// Forget the previous workspace's live DM presence before loading
	// the new one, so its cache-seeded item presence isn't overwritten
	// by a stale peer from the workspace we're leaving. The new
	// workspace's own presence_change events repopulate it.
	a.sidebar.ResetPresence()
	a.SetChannels(m.Channels)
	a.channelFinder.SetItems(m.FinderItems)
	// SetExternalUsers re-pushes user-names; calling SetUserNames
	// last is the canonical state.
	a.SetExternalUsers(m.ExternalUsers)
	a.SetUserNames(m.UserNames)
	a.SetCustomEmoji(m.CustomEmoji)
	a.SetUserGroups(m.UserGroups)
	// Route through the setter so messagepane/threadPanel also learn the
	// current user (see WorkspaceReadyMsg above).
	a.SetCurrentUserID(m.UserID)
	a.activeServerID = m.TeamID
	pres, dndEnabled, dndEnd, _ := a.presence.Status(a.activeServerID)
	a.statusbar.SetStatus(pres, dndEnabled, dndEnd)
	// Apply per-workspace theme. Must run on Update goroutine so
	// the component cache invalidations and compose-style refreshes
	// below take effect on the next render.
	if m.Theme != "" {
		styles.Apply(m.Theme, a.themeOverrides)
		a.invalidateAllWinModelCaches()
		a.threadPanel.InvalidateCache()
		a.sidebar.InvalidateCache()
		a.compose.RefreshStyles()
		a.threadCompose.RefreshStyles()
	}
	if m.SidebarWidth != 0 {
		a.sidebar.SetWidth(m.SidebarWidth)
	}
	a.workspaceRail.SelectByID(m.TeamID)

	var batch []tea.Cmd
	// Restore the last-viewed channel for this workspace if we
	// have one and it still exists; otherwise fall back to the
	// first channel in the sidebar. Move the sidebar cursor to
	// that channel as well so the highlight matches the messages
	// pane.
	if len(m.Channels) > 0 {
		target := m.Channels[0]
		if savedID, ok := a.lastChannelByTeam[m.TeamID]; ok && savedID != "" {
			for _, ch := range m.Channels {
				if ch.ID == savedID {
					target = ch
					break
				}
			}
		}
		a.sidebar.SelectByID(target.ID)
		batch = append(batch, func() tea.Msg {
			return ChannelSelectedMsg{ID: target.ID, Name: target.Name, Type: target.Type}
		})
	} else {
		a.sidebar.SelectThreadsRow()
		a.messagepane.SetLoading(false)
		a.messagepane.SetMessages(nil)
	}
	// Kick off an initial threads-list fetch so the sidebar
	// Threads row badge populates before the user opens the view.
	threads := a.threads
	team := ids.TeamID(m.TeamID)
	batch = append(batch, func() tea.Msg { return threads.ListFetch(team) })
	return tea.Batch(batch...)
}
