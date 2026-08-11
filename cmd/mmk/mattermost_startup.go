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
	runCtx, stopRun := context.WithCancel(context.Background())
	defer stopRun()
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
		if runCtx.Err() != nil {
			pendingMu.Unlock()
			return
		}
		if program == nil {
			pending = append(pending, msg)
			pendingMu.Unlock()
			return
		}
		pendingMu.Unlock()
		program.Send(msg)
	}
	startup, err := startMattermost(runCtx, mattermostStartupDeps{
		Registry: registry,
		Secrets:  mattermost.NewOSSecretStore(),
		NewClient: func(server mattermost.Server, token string) (mattermostStartupClient, error) {
			return mattermost.NewClient(server.URL, token)
		},
		Cache: db,
		Send:  send,
		Clock: time.Now,
	})
	if err != nil {
		return err
	}
	wireMattermostRuntime(app, runCtx, startup, db)
	defer func() {
		stopRun()
		startup.Cancel()
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = startup.WaitContext(ctx)
	}()

	for {
		pendingMu.Lock()
		batch := pending
		pending = nil
		if len(batch) == 0 {
			program = tea.NewProgram(app)
			pendingMu.Unlock()
			break
		}
		pendingMu.Unlock()
		applyPendingMattermostMessages(app, batch)
	}

	app.SetWorkspaceSwitcher(func(serverID string) tea.Msg {
		return startup.switchMsg(serverID)
	})
	_, err = program.Run()
	return err
}

func applyPendingMattermostMessages(app *ui.App, pending []tea.Msg) {
	for _, msg := range pending {
		switch value := msg.(type) {
		case mattermostRailMsg:
			app.SetLoadingServers(loadingServers(value.Items))
			app.SetWorkspaces(value.Items)
		case ui.ServerReadyMsg, ui.ServerRefreshedMsg, ui.ServerSwitchedMsg:
			_, cmd := app.Update(msg)
			if cmd == nil {
				continue
			}
			// Server activation emits an immediate ChannelSelectedMsg. Apply
			// that local transition now so cache is visible before first View.
			selected := cmd()
			if channel, ok := selected.(ui.ChannelSelectedMsg); ok {
				_, async := app.Update(channel)
				app.QueueInitCmd(async)
			} else {
				app.QueueInitCmd(cmd)
			}
		case ui.ServerStateMsg:
			_, _ = app.Update(msg)
		}
	}
}

func loadingServers(items []workspace.WorkspaceItem) []ui.LoadingServer {
	servers := make([]ui.LoadingServer, len(items))
	for i := range items {
		servers[i] = ui.LoadingServer{ID: items[i].ID, Name: items[i].Name}
	}
	return servers
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
	ReplaceMattermostBootstrapSnapshot(cache.MattermostBootstrapSnapshot) error
}

type mattermostSecretReader interface {
	Get(context.Context, string) (string, error)
}

type mattermostStartupDeps struct {
	Registry  config.ServerRegistry
	Secrets   mattermostSecretReader
	NewClient func(mattermost.Server, string) (mattermostStartupClient, error)
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
	client   mattermostStartupClient
	usable   bool
}

type mattermostStartupClient interface {
	service.ServerBootstrapClient
	ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error)
	CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error)
}

type mattermostRuntimeApp interface {
	SetMattermostHistoryService(ui.MattermostHistoryService)
	SetMattermostSendService(ui.MattermostSendService)
}

func wireMattermostRuntime(app mattermostRuntimeApp, runCtx context.Context, startup *mattermostStartup, db *cache.DB) {
	app.SetMattermostHistoryService(mattermostUIHistoryService{ctx: runCtx, startup: startup, cache: db})
	app.SetMattermostSendService(ui.NewMattermostSendService((mattermostUISendService{ctx: runCtx, startup: startup}).Send))
}

type mattermostStartup struct {
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	mu       sync.RWMutex
	contexts map[ids.ServerID]mattermostServerContext
	initial  ids.ServerID
	claimed  bool
}

func (s *mattermostStartup) Cancel() { s.cancel() }
func (s *mattermostStartup) Wait()   { s.wg.Wait() }
func (s *mattermostStartup) WaitContext(ctx context.Context) error {
	done := make(chan struct{})
	go func() { s.wg.Wait(); close(done) }()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

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

	for _, configured := range deps.Registry.Servers {
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
		initial := startup.setSnapshotAndClaim(serverID, snapshot)
		deps.Send(ui.ServerReadyMsg{Server: mattermostServerViewState(snapshot, initial)})
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
	s.setClient(serverID, client)
	observedAt := deps.Clock()
	if err := deps.Cache.ReplaceMattermostBootstrapSnapshot(mattermostCacheSnapshot(snapshot, observedAt)); err != nil {
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: fmt.Errorf("persist Mattermost bootstrap: %w", err)})
		return
	}
	wasUsable := s.usable(serverID)
	initial := s.setSnapshotAndClaim(serverID, snapshot)
	if wasUsable {
		deps.Send(ui.ServerRefreshedMsg{Server: mattermostServerViewState(snapshot, false)})
	} else {
		deps.Send(ui.ServerReadyMsg{Server: mattermostServerViewState(snapshot, initial)})
	}
	deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateReady})
}

func (s *mattermostStartup) setClient(serverID ids.ServerID, client mattermostStartupClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext := s.contexts[serverID]
	serverContext.client = client
	s.contexts[serverID] = serverContext
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

func (s *mattermostStartup) setSnapshotAndClaim(serverID ids.ServerID, snapshot service.ServerSnapshot) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext := s.contexts[serverID]
	serverContext.snapshot = snapshot
	serverContext.usable = true
	s.contexts[serverID] = serverContext
	if s.claimed {
		return false
	}
	s.claimed = true
	return true
}

func (s *mattermostStartup) switchMsg(serverID string) tea.Msg {
	s.mu.RLock()
	defer s.mu.RUnlock()
	serverContext := s.contexts[ids.ServerID(serverID)]
	if !serverContext.usable {
		return nil
	}
	return ui.ServerSwitchedMsg{Server: mattermostServerViewState(serverContext.snapshot, false)}
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
	readState := make(map[string]cache.ReadState)
	hasUnread := false
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
			finder = append(finder, channelfinder.Item{ID: item.ID, Name: item.Name, Type: legacyType, Joined: true})
			if entry.Membership != nil {
				unread := entry.Membership.MentionCount > 0 || entry.Channel.TotalMsgCount > entry.Membership.MsgCount
				readState[item.ID] = cache.ReadState{HasUnread: unread}
				hasUnread = hasUnread || unread
			}
		}
	}
	return ui.ServerViewState{ServerID: ids.ServerID(snapshot.Server.ID), ServerName: snapshot.Server.Name, Channels: channels, FinderItems: finder, UserNames: users, UserID: snapshot.CurrentUser.ID, SectionsProvider: staticSectionsProvider{sections: sections}, ReadState: readState, HasUnread: hasUnread, InitialActive: initialActive}
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
