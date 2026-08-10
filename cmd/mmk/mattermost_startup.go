package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/channelfinder"
	"github.com/nosovk/mmk/internal/ui/sidebar"
	"github.com/nosovk/mmk/internal/ui/workspace"
)

func runMattermost(registry config.ServerRegistry, cfg config.Config, db *cache.DB) error {
	app := ui.NewApp()
	app.SetHelpFooter("Mattermost")
	app.SetTypingEnabled(false)
	app.SetThemeItems(nil)
	app.SetThemeOverrides(cfg.Theme)
	app.SetMouseWheelLines(cfg.Appearance.MouseWheelLines)
	app.SetSidebarStaleThreshold(0)

	var program *tea.Program
	pending := make([]tea.Msg, 0, len(registry.Servers)*3)
	var pendingMu sync.Mutex
	send := func(msg tea.Msg) {
		pendingMu.Lock()
		if program == nil {
			pending = append(pending, msg)
			pendingMu.Unlock()
			return
		}
		pendingMu.Unlock()
		program.Send(msg)
	}
	startup, err := startMattermost(context.Background(), mattermostStartupDeps{
		Registry: registry,
		Secrets:  mattermost.NewOSSecretStore(),
		NewClient: func(server mattermost.Server, token string) (service.ServerBootstrapClient, error) {
			return mattermost.NewClient(server.URL, token)
		},
		Cache: db,
		Send:  send,
		Clock: time.Now,
	})
	if err != nil {
		return err
	}
	defer func() {
		startup.Cancel()
		startup.Wait()
	}()

	pendingMu.Lock()
	for _, msg := range pending {
		switch value := msg.(type) {
		case mattermostRailMsg:
			app.SetLoadingWorkspaces(serverNames(value.Items))
			app.SetWorkspaces(value.Items)
		case ui.ServerReadyMsg, ui.ServerRefreshedMsg, ui.ServerStateMsg:
			_, _ = app.Update(msg)
		}
	}
	program = tea.NewProgram(app)
	pending = nil
	pendingMu.Unlock()

	app.SetWorkspaceSwitcher(func(serverID string) tea.Msg {
		return ui.ServerSwitchedMsg{Server: startup.viewState(ids.ServerID(serverID))}
	})
	_, err = program.Run()
	return err
}

func serverNames(items []workspace.WorkspaceItem) []string {
	names := make([]string, len(items))
	for i := range items {
		names[i] = items[i].Name
	}
	return names
}

type startupModeKind uint8

const (
	startupSlack startupModeKind = iota
	startupMattermost
)

func startupMode(registry config.ServerRegistry) startupModeKind {
	if len(registry.Servers) > 0 {
		return startupMattermost
	}
	return startupSlack
}

type mattermostSnapshotStore interface {
	LoadMattermostBootstrapSnapshot(string) (cache.MattermostBootstrapSnapshot, error)
	ApplyMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error
}

type mattermostSecretReader interface {
	Get(context.Context, string) (string, error)
}

type mattermostStartupDeps struct {
	Registry  config.ServerRegistry
	Secrets   mattermostSecretReader
	NewClient func(mattermost.Server, string) (service.ServerBootstrapClient, error)
	Cache     mattermostSnapshotStore
	Send      func(tea.Msg)
	Clock     func() time.Time
}

type mattermostRailMsg struct {
	Items []workspace.WorkspaceItem
}

type mattermostServerContext struct {
	server   mattermost.Server
	snapshot service.ServerSnapshot
	usable   bool
}

type mattermostStartup struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
	contexts map[ids.ServerID]mattermostServerContext
	initial  ids.ServerID
}

func (s *mattermostStartup) Cancel() { s.cancel() }
func (s *mattermostStartup) Wait()   { s.wg.Wait() }

func startMattermost(parent context.Context, deps mattermostStartupDeps) (*mattermostStartup, error) {
	if deps.Secrets == nil || deps.NewClient == nil || deps.Cache == nil || deps.Send == nil {
		return nil, errors.New("Mattermost startup dependencies must not be nil")
	}
	if deps.Clock == nil {
		deps.Clock = time.Now
	}
	ctx, cancel := context.WithCancel(parent)
	startup := &mattermostStartup{cancel: cancel, contexts: make(map[ids.ServerID]mattermostServerContext, len(deps.Registry.Servers))}
	if len(deps.Registry.Servers) > 0 {
		startup.initial = ids.ServerID(deps.Registry.Servers[0].ID)
	}
	items := make([]workspace.WorkspaceItem, 0, len(deps.Registry.Servers))
	for _, configured := range deps.Registry.Servers {
		server := mattermostServerFromRegistry(configured)
		serverID := ids.ServerID(server.ID)
		startup.contexts[serverID] = mattermostServerContext{server: server}
		items = append(items, workspace.WorkspaceItem{ID: server.ID, Name: server.Name, Initials: workspace.WorkspaceInitials(server.Name), State: workspace.ItemStateLoading})
	}
	deps.Send(mattermostRailMsg{Items: items})

	for index, configured := range deps.Registry.Servers {
		serverID := ids.ServerID(configured.ID)
		raw, err := deps.Cache.LoadMattermostBootstrapSnapshot(configured.ID)
		if err != nil {
			if !errors.Is(err, sql.ErrNoRows) {
				deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: fmt.Errorf("load cached server: %w", err)})
			}
			continue
		}
		snapshot, err := mattermostServiceSnapshot(raw)
		if err != nil {
			deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: fmt.Errorf("hydrate cached server: %w", err)})
			continue
		}
		startup.setSnapshot(serverID, snapshot)
		deps.Send(ui.ServerReadyMsg{Server: mattermostServerViewState(snapshot, index == 0)})
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateReady})
	}

	for _, configured := range deps.Registry.Servers {
		configured := configured
		startup.wg.Add(1)
		go func() {
			defer startup.wg.Done()
			startup.refreshServer(ctx, deps, configured)
		}()
	}
	return startup, nil
}

func (s *mattermostStartup) refreshServer(ctx context.Context, deps mattermostStartupDeps, configured config.MattermostServer) {
	serverID := ids.ServerID(configured.ID)
	token, err := deps.Secrets.Get(ctx, configured.ID)
	if err != nil {
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: errors.New("Mattermost credential unavailable")})
		return
	}
	client, err := deps.NewClient(mattermostServerFromRegistry(configured), token)
	token = ""
	if err != nil {
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: errors.New("Mattermost client initialization failed")})
		return
	}
	snapshot, err := service.BootstrapServer(ctx, client, mattermostServerFromRegistry(configured))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: errors.New("Mattermost bootstrap failed")})
		return
	}
	observedAt := deps.Clock()
	if err := deps.Cache.ApplyMattermostBootstrapSnapshot(mattermostCacheSnapshot(snapshot, observedAt)); err != nil {
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: fmt.Errorf("persist Mattermost bootstrap: %w", err)})
		return
	}
	wasUsable := s.usable(serverID)
	s.setSnapshot(serverID, snapshot)
	if wasUsable {
		deps.Send(ui.ServerRefreshedMsg{Server: mattermostServerViewState(snapshot, false)})
	} else {
		deps.Send(ui.ServerReadyMsg{Server: mattermostServerViewState(snapshot, serverID == s.initial)})
	}
	deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateReady})
}

func (s *mattermostStartup) viewState(serverID ids.ServerID) ui.ServerViewState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	context := s.contexts[serverID]
	if context.usable {
		return mattermostServerViewState(context.snapshot, false)
	}
	return ui.ServerViewState{ServerID: serverID, ServerName: context.server.Name}
}

func (s *mattermostStartup) setSnapshot(serverID ids.ServerID, snapshot service.ServerSnapshot) {
	s.mu.Lock()
	defer s.mu.Unlock()
	context := s.contexts[serverID]
	context.snapshot = snapshot
	context.usable = true
	s.contexts[serverID] = context
}

func (s *mattermostStartup) usable(serverID ids.ServerID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contexts[serverID].usable
}

func mattermostServerViewState(snapshot service.ServerSnapshot, initialActive bool) ui.ServerViewState {
	channels := make([]sidebar.ChannelItem, 0)
	finder := make([]channelfinder.Item, 0)
	users := make(map[string]string, len(snapshot.Users))
	sections := make([]sidebar.SectionMeta, 0, len(snapshot.Sections))
	for _, user := range snapshot.Users {
		users[user.ID] = user.DisplayName()
	}
	for _, section := range snapshot.Sections {
		kind := sidebar.SectionKindTeam
		if section.Kind == service.ChannelSectionKindDirect {
			kind = sidebar.SectionKindDirect
		}
		sections = append(sections, sidebar.SectionMeta{ID: section.ID, Name: section.Name, Kind: kind})
		for _, entry := range section.Channels {
			itemKind, legacyType := mattermostSidebarKind(entry.Channel.Kind)
			item := sidebar.ChannelItem{ID: entry.Channel.ID, Name: entry.DisplayName, Kind: itemKind, SectionID: section.ID, Type: legacyType}
			channels = append(channels, item)
			finder = append(finder, channelfinder.Item{ID: item.ID, Name: item.Name, Type: legacyType})
		}
	}
	return ui.ServerViewState{ServerID: ids.ServerID(snapshot.Server.ID), ServerName: snapshot.Server.Name, Channels: channels, FinderItems: finder, UserNames: users, UserID: snapshot.CurrentUser.ID, SectionsProvider: staticSectionsProvider{sections: sections}, InitialActive: initialActive}
}

func mattermostSidebarKind(kind mattermost.ChannelKind) (sidebar.ChannelKind, string) {
	switch kind {
	case mattermost.ChannelKindPrivate:
		return sidebar.ChannelKindPrivate, "private"
	case mattermost.ChannelKindDirect:
		return sidebar.ChannelKindDirect, "dm"
	case mattermost.ChannelKindGroup:
		return sidebar.ChannelKindGroup, "group_dm"
	default:
		return sidebar.ChannelKindPublic, "channel"
	}
}

type staticSectionsProvider struct{ sections []sidebar.SectionMeta }

func (staticSectionsProvider) Ready() bool { return true }
func (s staticSectionsProvider) OrderedSections() []sidebar.SectionMeta {
	return append([]sidebar.SectionMeta(nil), s.sections...)
}
