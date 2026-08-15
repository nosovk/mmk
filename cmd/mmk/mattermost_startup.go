package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/config"
	"github.com/nosovk/mmk/internal/debuglog"
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
	activeSelection := newMattermostActiveSelection()
	app.SetSelectionObserver(activeSelection.Store)
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
	eventSend := func(ctx context.Context, msg tea.Msg) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		send(msg)
		return ctx.Err()
	}
	startup, err := startMattermost(runCtx, mattermostStartupDeps{
		Registry: registry,
		Secrets:  mattermost.NewOSSecretStore(),
		NewClient: func(server mattermost.Server, token string) (mattermostStartupClient, error) {
			return mattermost.NewClient(server.URL, token)
		},
		Cache:                   db,
		Send:                    send,
		Clock:                   time.Now,
		ActiveSelection:         activeSelection.Load,
		ActiveSelectionSnapshot: activeSelection.LoadSnapshot,
		NewEventHandler: func(startup *mattermostStartup) func(context.Context, ids.ServerID, mattermost.Event) {
			return mattermostProductionEventHandler(db, eventSend, activeSelection.Load, startup, func(err error) {
				debuglog.WS("Mattermost realtime event error: %v", err)
			})
		},
		OnConnectionState: mattermostConnectionStateSender(send),
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

func mattermostConnectionStateSender(send func(tea.Msg)) func(ids.ServerID, mattermost.ConnectionState) {
	return func(serverID ids.ServerID, state mattermost.ConnectionState) {
		railState := workspace.ItemStateConnecting
		switch state {
		case mattermost.ConnectionStateConnected:
			railState = workspace.ItemStateReady
		case mattermost.ConnectionStateOffline:
			railState = workspace.ItemStateOffline
		case mattermost.ConnectionStateReconnecting:
			railState = workspace.ItemStateReconnecting
		}
		send(ui.ServerConnectionStateMsg{ServerID: serverID, State: railState})
	}
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
		case ui.ServerStateMsg, ui.ServerConnectionStateMsg:
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
	ReplaceMattermostBootstrapSnapshotContext(context.Context, cache.MattermostBootstrapSnapshot) error
}

type mattermostSecretReader interface {
	Get(context.Context, string) (string, error)
}

type mattermostStartupDeps struct {
	Registry                config.ServerRegistry
	Secrets                 mattermostSecretReader
	NewClient               func(mattermost.Server, string) (mattermostStartupClient, error)
	Cache                   mattermostSnapshotStore
	Send                    func(tea.Msg)
	Clock                   func() time.Time
	ActiveSelection         func() (ids.ServerID, string)
	ActiveSelectionSnapshot func() (ids.ServerID, string, uint64)
	OnEvent                 func(context.Context, ids.ServerID, mattermost.Event)
	NewEventHandler         func(*mattermostStartup) func(context.Context, ids.ServerID, mattermost.Event)
	OnConnectionState       func(ids.ServerID, mattermost.ConnectionState)
	ConnectionWait          func(context.Context, time.Duration) error
}

type mattermostRailMsg struct {
	Items []workspace.WorkspaceItem
}

type mattermostServerContext struct {
	server            mattermost.Server
	snapshot          service.ServerSnapshot
	client            mattermostStartupClient
	clientReady       chan struct{}
	clientErr         error
	usable            bool
	revision          uint64
	localReadOverlays map[string]mattermostLocalReadOverlay
}

type mattermostLocalReadOverlay struct {
	ViewedMsgCount  int64
	LastViewedAt    int64
	HasLastViewedAt bool
}

type mattermostStartupClient interface {
	service.ServerBootstrapClient
	RunWebSocket(context.Context, func(), func(mattermost.Event), func(error)) error
	ChannelPosts(context.Context, string, mattermost.ChannelPostsOptions) (mattermost.MessagePage, error)
	CreatePost(context.Context, mattermost.CreatePostRequest) (mattermost.Message, error)
	ViewChannel(context.Context, string, string, string) (mattermost.ViewChannelResult, error)
}

type mattermostRuntimeApp interface {
	SetMattermostHistoryService(ui.MattermostHistoryService)
	SetMattermostSendService(ui.MattermostSendService)
	SetMattermostReadService(ui.MattermostReadService)
}

func wireMattermostRuntime(app mattermostRuntimeApp, runCtx context.Context, startup *mattermostStartup, db *cache.DB) {
	app.SetMattermostHistoryService(mattermostUIHistoryService{ctx: runCtx, startup: startup, cache: db})
	app.SetMattermostSendService(ui.NewMattermostSendService((mattermostUISendService{ctx: runCtx, startup: startup}).Send))
	app.SetMattermostReadService(ui.NewMattermostReadService(newMattermostUIReadService(runCtx, startup, func(err error) {
		debuglog.WS("%v", err)
	}).View))
}

type mattermostUIReadService struct {
	ctx        context.Context
	startup    *mattermostStartup
	diagnostic func(error)
	mu         sync.Mutex
	tails      map[ids.ServerID]<-chan struct{}
}

func newMattermostUIReadService(ctx context.Context, startup *mattermostStartup, diagnostic func(error)) *mattermostUIReadService {
	return &mattermostUIReadService{ctx: ctx, startup: startup, diagnostic: diagnostic, tails: make(map[ids.ServerID]<-chan struct{})}
}

func (s *mattermostUIReadService) View(request ui.MattermostReadRequest) (ui.ServerViewState, tea.Cmd) {
	state, ok := s.startup.optimisticallyViewChannel(request.ServerID, request.ChannelID)
	if !ok {
		return ui.ServerViewState{}, nil
	}
	previousChannelID := ""
	if request.PreviousServerID == request.ServerID {
		previousChannelID = request.PreviousChannelID
	}

	done := make(chan struct{})
	s.mu.Lock()
	previous := s.tails[request.ServerID]
	s.tails[request.ServerID] = done
	s.mu.Unlock()

	return state, func() tea.Msg {
		defer close(done)
		if previous != nil {
			select {
			case <-previous:
			case <-s.ctx.Done():
				return nil
			}
		}
		client, ready, terminal, ok := s.startup.readClient(request.ServerID)
		if !ok {
			return nil
		}
		if terminal {
			return nil
		}
		if client == nil {
			select {
			case <-ready:
			case <-s.ctx.Done():
				return nil
			}
			_, _, terminal, ok = s.startup.readClient(request.ServerID)
			if !ok || terminal {
				return nil
			}
		}
		client, userID, viewedCounts, ok := s.startup.readClientBaseline(request.ServerID, request.ChannelID)
		if !ok {
			return nil
		}
		result, err := client.ViewChannel(s.ctx, userID, request.ChannelID, previousChannelID)
		if err != nil {
			if s.ctx.Err() == nil && s.diagnostic != nil {
				s.diagnostic(errors.New("Mattermost channel view request failed"))
			}
			return nil
		}
		corrected, changed := s.startup.applyViewChannelTimes(request.ServerID, result.LastViewedAtTimes, viewedCounts)
		if !changed {
			return nil
		}
		return ui.ServerRefreshedMsg{Server: corrected}
	}
}

type mattermostStartup struct {
	cancel          context.CancelFunc
	wg              sync.WaitGroup
	done            chan struct{}
	remaining       atomic.Int64
	mu              sync.RWMutex
	contexts        map[ids.ServerID]mattermostServerContext
	reconcileStates sync.Map
	initial         ids.ServerID
	claimed         bool
}

func (s *mattermostStartup) Cancel() { s.cancel() }
func (s *mattermostStartup) Wait()   { <-s.done }
func (s *mattermostStartup) WaitContext(ctx context.Context) error {
	select {
	case <-s.done:
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
	if deps.ActiveSelection == nil {
		deps.ActiveSelection = func() (ids.ServerID, string) { return "", "" }
	}
	if deps.OnEvent == nil {
		deps.OnEvent = func(context.Context, ids.ServerID, mattermost.Event) {}
	}
	if deps.OnConnectionState == nil {
		deps.OnConnectionState = func(ids.ServerID, mattermost.ConnectionState) {}
	}
	ctx, cancel := context.WithCancel(parent)
	startup := &mattermostStartup{cancel: cancel, done: make(chan struct{}), contexts: make(map[ids.ServerID]mattermostServerContext, len(deps.Registry.Servers))}
	if len(deps.Registry.Servers) > 0 {
		startup.initial = ids.ServerID(deps.Registry.Servers[0].ID)
	}
	items := make([]workspace.WorkspaceItem, 0, len(deps.Registry.Servers))
	for _, configured := range deps.Registry.Servers {
		server := mattermostServerFromRegistry(configured)
		serverID := ids.ServerID(server.ID)
		startup.contexts[serverID] = mattermostServerContext{server: server, clientReady: make(chan struct{})}
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
		state, initial := startup.setSnapshotAndClaim(serverID, snapshot)
		state.InitialActive = initial
		deps.Send(ui.ServerReadyMsg{Server: state})
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateReady})
	}
	eventHandler := deps.OnEvent
	if deps.NewEventHandler != nil {
		eventHandler = deps.NewEventHandler(startup)
	}

	workerCount := len(deps.Registry.Servers) * 3
	startup.wg.Add(workerCount)
	startup.remaining.Store(int64(workerCount))
	if workerCount == 0 {
		close(startup.done)
	}
	for _, configured := range deps.Registry.Servers {
		configured := configured
		serverID := ids.ServerID(configured.ID)
		dispatcher := newMattermostEventDispatcher(serverID, eventHandler)
		reconcileDispatcher := newMattermostReconcileDispatcher(func(ctx context.Context) error {
			reconcileCache, ok := deps.Cache.(mattermostReconcileStore)
			if !ok {
				return errors.New("Mattermost reconciliation cache unavailable")
			}
			return startup.reconcile(ctx, serverID, mattermostReconcileDeps{
				Cache: reconcileCache, Send: deps.Send, Clock: deps.Clock, ActiveSelection: deps.ActiveSelection, ActiveSelectionSnapshot: deps.ActiveSelectionSnapshot,
			})
		}, func(err error) {
			debuglog.WS("Mattermost server=%s reconciliation error: %v", serverID, err)
		})
		go func() {
			defer startup.workerDone()
			dispatcher.Run(ctx)
		}()
		go func() {
			defer startup.workerDone()
			reconcileDispatcher.Run(ctx)
		}()
		go func() {
			defer startup.workerDone()
			startup.refreshServer(ctx, deps, configured, dispatcher, reconcileDispatcher)
		}()
	}
	return startup, nil
}

func (s *mattermostStartup) workerDone() {
	s.wg.Done()
	if s.remaining.Add(-1) == 0 {
		close(s.done)
	}
}

func (s *mattermostStartup) refreshServer(ctx context.Context, deps mattermostStartupDeps, configured config.MattermostServer, dispatcher *mattermostEventDispatcher, reconcileDispatcher *mattermostReconcileDispatcher) {
	serverID := ids.ServerID(configured.ID)
	token, err := deps.Secrets.Get(ctx, configured.ID)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		terminalErr := errors.New("Mattermost credential unavailable")
		s.resolveClientFailure(serverID, terminalErr)
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: terminalErr})
		return
	}
	client, err := deps.NewClient(mattermostServerFromRegistry(configured), token)
	token = ""
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		terminalErr := errors.New("Mattermost client initialization failed")
		s.resolveClientFailure(serverID, terminalErr)
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: terminalErr})
		return
	}
	snapshot, err := service.BootstrapServer(ctx, client, mattermostServerFromRegistry(configured))
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		terminalErr := errors.New("Mattermost bootstrap failed")
		s.resolveClientFailure(serverID, terminalErr)
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: terminalErr})
		return
	}
	wasUsable := s.usable(serverID)
	state := s.reconcileState(serverID)
	state.apply.Lock()
	state.runtime.Lock()
	serverState, initial, err := s.installAuthoritativeSnapshot(ctx, serverID, snapshot, client, deps.Cache, deps.Clock(), true)
	state.runtime.Unlock()
	state.apply.Unlock()
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		terminalErr := errors.New("Mattermost bootstrap persistence failed")
		s.resolveClientFailure(serverID, terminalErr)
		deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateError, Err: terminalErr})
		return
	}
	serverState.InitialActive = initial
	if wasUsable {
		deps.Send(ui.ServerRefreshedMsg{Server: serverState})
	} else {
		deps.Send(ui.ServerReadyMsg{Server: serverState})
	}
	deps.Send(ui.ServerStateMsg{ServerID: serverID, State: workspace.ItemStateReady})
	s.runConnectionManager(ctx, serverID, client, deps, dispatcher, reconcileDispatcher)
}

func (s *mattermostStartup) runConnectionManager(ctx context.Context, serverID ids.ServerID, client mattermostStartupClient, deps mattermostStartupDeps, dispatcher *mattermostEventDispatcher, reconcileDispatcher *mattermostReconcileDispatcher) {
	manager := service.ConnectionManager{
		Client: client,
		OnEvent: func(event mattermost.Event) {
			dispatcher.Enqueue(event)
		},
		OnState: func(state mattermost.ConnectionState) {
			debuglog.WS("Mattermost server=%s state=%s", serverID, state)
			deps.OnConnectionState(serverID, state)
		},
		Reconcile: func(context.Context) error {
			reconcileDispatcher.Enqueue()
			return nil
		},
		OnError: func(err error) {
			debuglog.WS("Mattermost server=%s connection error: %v", serverID, err)
		},
		Wait: deps.ConnectionWait,
	}
	if err := manager.Run(ctx); err != nil && !errors.Is(err, context.Canceled) {
		debuglog.WS("Mattermost server=%s connection manager stopped: %v", serverID, err)
	}
}

type mattermostSelectionValue struct {
	serverID   ids.ServerID
	channelID  string
	generation uint64
}

type mattermostActiveSelection struct {
	value atomic.Pointer[mattermostSelectionValue]
}

func newMattermostActiveSelection() *mattermostActiveSelection {
	selection := &mattermostActiveSelection{}
	selection.Store("", "")
	return selection
}

func (s *mattermostActiveSelection) Store(serverID ids.ServerID, channelID string) {
	current := s.value.Load()
	generation := uint64(0)
	if current != nil {
		generation = current.generation
		if current.serverID != serverID || current.channelID != channelID {
			generation++
		}
	}
	s.value.Store(&mattermostSelectionValue{serverID: serverID, channelID: channelID, generation: generation})
}

func (s *mattermostActiveSelection) LoadSnapshot() (ids.ServerID, string, uint64) {
	selection := s.value.Load()
	if selection == nil {
		return "", "", 0
	}
	return selection.serverID, selection.channelID, selection.generation
}

func (s *mattermostActiveSelection) Load() (ids.ServerID, string) {
	selection := s.value.Load()
	if selection == nil {
		return "", ""
	}
	return selection.serverID, selection.channelID
}

func (s *mattermostStartup) setClient(serverID ids.ServerID, client mattermostStartupClient) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext := s.contexts[serverID]
	if serverContext.clientErr != nil {
		return
	}
	serverContext.client = client
	if client != nil && serverContext.clientReady != nil {
		close(serverContext.clientReady)
		serverContext.clientReady = nil
	}
	s.contexts[serverID] = serverContext
}

func (s *mattermostStartup) resolveClientFailure(serverID ids.ServerID, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext, ok := s.contexts[serverID]
	if !ok || serverContext.client != nil || serverContext.clientErr != nil {
		return
	}
	serverContext.clientErr = err
	if serverContext.clientReady == nil {
		serverContext.clientReady = make(chan struct{})
	}
	close(serverContext.clientReady)
	s.contexts[serverID] = serverContext
}

func (s *mattermostStartup) readClient(serverID ids.ServerID) (mattermostStartupClient, <-chan struct{}, bool, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext, ok := s.contexts[serverID]
	if !ok || !serverContext.usable {
		return nil, nil, false, false
	}
	if serverContext.client != nil {
		return serverContext.client, nil, false, true
	}
	if serverContext.clientErr != nil {
		return nil, nil, true, true
	}
	if serverContext.clientReady == nil {
		serverContext.clientReady = make(chan struct{})
		s.contexts[serverID] = serverContext
	}
	return nil, serverContext.clientReady, false, true
}

func (s *mattermostStartup) readClientBaseline(serverID ids.ServerID, channelID string) (mattermostStartupClient, string, map[string]int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	serverContext, ok := s.contexts[serverID]
	if !ok || !serverContext.usable || serverContext.client == nil || serverContext.snapshot.CurrentUser.ID == "" {
		return nil, "", nil, false
	}
	entry, found := mattermostSnapshotChannel(serverContext.snapshot, channelID)
	if !found || entry.Membership == nil || entry.Membership.ChannelID != channelID || entry.Membership.UserID != serverContext.snapshot.CurrentUser.ID {
		return nil, "", nil, false
	}
	return serverContext.client, serverContext.snapshot.CurrentUser.ID, mattermostSnapshotChannelCounts(serverContext.snapshot), true
}

func (s *mattermostStartup) viewState(serverID ids.ServerID) ui.ServerViewState {
	s.mu.RLock()
	defer s.mu.RUnlock()
	context := s.contexts[serverID]
	if context.usable {
		return mattermostServerViewState(context.snapshot, false, context.revision)
	}
	return ui.ServerViewState{ServerID: serverID, ServerName: context.server.Name, Revision: context.revision}
}

func (s *mattermostStartup) setSnapshot(serverID ids.ServerID, snapshot service.ServerSnapshot) ui.ServerViewState {
	s.mu.Lock()
	defer s.mu.Unlock()
	context := s.contexts[serverID]
	context.snapshot = snapshot
	context.usable = true
	context.revision++
	s.contexts[serverID] = context
	return mattermostServerViewState(context.snapshot, false, context.revision)
}

func (s *mattermostStartup) updateRuntimeEvent(serverID ids.ServerID, event mattermost.Event, activeServer ids.ServerID, activeChannel string, _ bool) (ui.ServerViewState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.updateRuntimeEventLocked(serverID, event, activeServer, activeChannel)
}

func (s *mattermostStartup) updateRuntimeEventWithSelection(serverID ids.ServerID, event mattermost.Event, activeSelection func() (ids.ServerID, string)) (ui.ServerViewState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	activeServer, activeChannel := ids.ServerID(""), ""
	if activeSelection != nil {
		activeServer, activeChannel = activeSelection()
	}
	return s.updateRuntimeEventLocked(serverID, event, activeServer, activeChannel)
}

func (s *mattermostStartup) updateRuntimeEventLocked(serverID ids.ServerID, event mattermost.Event, activeServer ids.ServerID, activeChannel string) (ui.ServerViewState, bool) {
	serverContext, ok := s.contexts[serverID]
	if !ok || !serverContext.usable {
		return ui.ServerViewState{}, false
	}

	snapshot := serverContext.snapshot
	changed := false
	switch value := event.(type) {
	case mattermost.PostedEvent:
		changed = updateMattermostSnapshotChannel(&snapshot, value.Message.ChannelID, func(entry service.ChannelEntry) service.ChannelEntry {
			return service.ChannelWithNewPost(entry, activeServer == serverID && activeChannel == value.Message.ChannelID)
		})
	case mattermost.ChannelViewedEvent:
		if value.UserID != snapshot.CurrentUser.ID {
			return ui.ServerViewState{}, false
		}
		for _, update := range value.Updates {
			entry, found := mattermostSnapshotChannel(snapshot, update.ChannelID)
			if !found || entry.Membership == nil || update.HasViewedAt && update.ViewedAt <= entry.Membership.LastViewedAt {
				continue
			}
			if serverContext.localReadOverlays == nil {
				serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
			}
			updated := updateMattermostSnapshotChannel(&snapshot, update.ChannelID, func(entry service.ChannelEntry) service.ChannelEntry {
				if update.HasViewedAt && update.ViewedAt <= entry.Membership.LastViewedAt {
					return entry
				}
				entry = service.ChannelViewed(entry)
				if update.HasViewedAt {
					membership := *entry.Membership
					membership.LastViewedAt = update.ViewedAt
					entry.Membership = &membership
				}
				return entry
			})
			if updated || !update.HasViewedAt {
				if serverContext.localReadOverlays == nil {
					serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
				}
				overlay := serverContext.localReadOverlays[update.ChannelID]
				overlay.ViewedMsgCount = max(overlay.ViewedMsgCount, entry.Channel.TotalMsgCount)
				if update.HasViewedAt {
					overlay.HasLastViewedAt = true
					overlay.LastViewedAt = max(overlay.LastViewedAt, update.ViewedAt)
				}
				serverContext.localReadOverlays[update.ChannelID] = overlay
			}
			changed = changed || updated
		}
	default:
		return ui.ServerViewState{}, false
	}
	if !changed {
		s.contexts[serverID] = serverContext
		return ui.ServerViewState{}, false
	}
	serverContext.snapshot = snapshot
	serverContext.revision++
	s.contexts[serverID] = serverContext
	return mattermostServerViewState(snapshot, false, serverContext.revision), true
}

func (s *mattermostStartup) optimisticallyViewChannel(serverID ids.ServerID, channelID string) (ui.ServerViewState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext, ok := s.contexts[serverID]
	if !ok || !serverContext.usable || serverContext.snapshot.CurrentUser.ID == "" {
		return ui.ServerViewState{}, false
	}
	snapshot := serverContext.snapshot
	entry, found := mattermostSnapshotChannel(snapshot, channelID)
	if !found || entry.Membership == nil || entry.Membership.ChannelID != channelID || entry.Membership.UserID != snapshot.CurrentUser.ID {
		return ui.ServerViewState{}, false
	}
	if !updateMattermostSnapshotChannel(&snapshot, channelID, service.ChannelViewed) {
		return mattermostServerViewState(snapshot, false, serverContext.revision), true
	}
	viewed, _ := mattermostSnapshotChannel(snapshot, channelID)
	serverContext.snapshot = snapshot
	serverContext.revision++
	if serverContext.localReadOverlays == nil {
		serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
	}
	overlay := serverContext.localReadOverlays[channelID]
	overlay.ViewedMsgCount = max(overlay.ViewedMsgCount, viewed.Channel.TotalMsgCount)
	serverContext.localReadOverlays[channelID] = overlay
	s.contexts[serverID] = serverContext
	return mattermostServerViewState(snapshot, false, serverContext.revision), true
}

func (s *mattermostStartup) applyViewChannelTimes(serverID ids.ServerID, times map[string]int64, viewedCounts map[string]int64) (ui.ServerViewState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext, ok := s.contexts[serverID]
	if !ok || !serverContext.usable {
		return ui.ServerViewState{}, false
	}
	snapshot := serverContext.snapshot
	changed := false
	changedOverlays := make(map[string]mattermostLocalReadOverlay)
	for channelID, viewedAt := range times {
		clearedBoundary := int64(0)
		updated := updateMattermostSnapshotChannel(&snapshot, channelID, func(entry service.ChannelEntry) service.ChannelEntry {
			if entry.Membership == nil || viewedAt < entry.Membership.LastViewedAt {
				return entry
			}
			membership := *entry.Membership
			if entry.Channel.TotalMsgCount <= viewedCounts[channelID] {
				membership.MsgCount = entry.Channel.TotalMsgCount
				membership.MentionCount = 0
				clearedBoundary = viewedCounts[channelID]
			}
			membership.LastViewedAt = viewedAt
			entry.Membership = &membership
			return entry
		})
		changed = changed || updated
		if updated {
			overlay := serverContext.localReadOverlays[channelID]
			overlay.HasLastViewedAt = true
			overlay.LastViewedAt = max(overlay.LastViewedAt, viewedAt)
			overlay.ViewedMsgCount = max(overlay.ViewedMsgCount, clearedBoundary)
			changedOverlays[channelID] = overlay
		}
	}
	if !changed {
		return ui.ServerViewState{}, false
	}
	serverContext.snapshot = snapshot
	serverContext.revision++
	if serverContext.localReadOverlays == nil {
		serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
	}
	for channelID, overlay := range changedOverlays {
		serverContext.localReadOverlays[channelID] = overlay
	}
	s.contexts[serverID] = serverContext
	return mattermostServerViewState(snapshot, false, serverContext.revision), true
}

func mattermostSnapshotChannel(snapshot service.ServerSnapshot, channelID string) (service.ChannelEntry, bool) {
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			if entry.Channel.ID == channelID {
				return entry, true
			}
		}
	}
	return service.ChannelEntry{}, false
}

func mattermostSnapshotChannelCounts(snapshot service.ServerSnapshot) map[string]int64 {
	counts := make(map[string]int64)
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			counts[entry.Channel.ID] = entry.Channel.TotalMsgCount
		}
	}
	return counts
}

func updateMattermostSnapshotChannel(snapshot *service.ServerSnapshot, channelID string, transition func(service.ChannelEntry) service.ChannelEntry) bool {
	for sectionIndex := range snapshot.Sections {
		for channelIndex := range snapshot.Sections[sectionIndex].Channels {
			if snapshot.Sections[sectionIndex].Channels[channelIndex].Channel.ID != channelID {
				continue
			}
			current := snapshot.Sections[sectionIndex].Channels[channelIndex]
			next := transition(current)
			if reflect.DeepEqual(current, next) {
				return false
			}
			sections := append([]service.ChannelSection(nil), snapshot.Sections...)
			channels := append([]service.ChannelEntry(nil), sections[sectionIndex].Channels...)
			channels[channelIndex] = next
			sections[sectionIndex].Channels = channels
			snapshot.Sections = sections
			return true
		}
	}
	return false
}

func (s *mattermostStartup) setSnapshotAndClaim(serverID ids.ServerID, snapshot service.ServerSnapshot) (ui.ServerViewState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext := s.contexts[serverID]
	serverContext.snapshot = snapshot
	serverContext.usable = true
	serverContext.revision++
	serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
	s.contexts[serverID] = serverContext
	if s.claimed {
		return mattermostServerViewState(snapshot, false, serverContext.revision), false
	}
	s.claimed = true
	return mattermostServerViewState(snapshot, false, serverContext.revision), true
}

func replayMattermostLocalReads(snapshot service.ServerSnapshot, overlays map[string]mattermostLocalReadOverlay) service.ServerSnapshot {
	for channelID, overlay := range overlays {
		updateMattermostSnapshotChannel(&snapshot, channelID, func(entry service.ChannelEntry) service.ChannelEntry {
			if entry.Membership == nil {
				return entry
			}
			membership := *entry.Membership
			membership.MsgCount = max(membership.MsgCount, overlay.ViewedMsgCount)
			if entry.Channel.TotalMsgCount <= overlay.ViewedMsgCount {
				membership.MentionCount = 0
			}
			if overlay.HasLastViewedAt {
				membership.LastViewedAt = max(membership.LastViewedAt, overlay.LastViewedAt)
			}
			entry.Membership = &membership
			return entry
		})
	}
	return snapshot
}

type mattermostAuthoritativeSnapshotStore interface {
	ReplaceMattermostBootstrapSnapshotContext(context.Context, cache.MattermostBootstrapSnapshot) error
}

type mattermostLastPostAtStore interface {
	MattermostChannelLastPostAtsContext(context.Context, string, []string) (map[string]int64, error)
}

func (s *mattermostStartup) installAuthoritativeSnapshot(ctx context.Context, serverID ids.ServerID, snapshot service.ServerSnapshot, client mattermostStartupClient, store mattermostAuthoritativeSnapshotStore, observedAt time.Time, claim bool) (ui.ServerViewState, bool, error) {
	var err error
	snapshot, err = persistMattermostAuthoritativeSnapshot(ctx, snapshot, store, observedAt)
	if err != nil {
		return ui.ServerViewState{}, false, err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	serverContext, ok := s.contexts[serverID]
	if !ok {
		return ui.ServerViewState{}, false, errors.New("Mattermost server context unavailable")
	}
	snapshot = replayMattermostLocalReads(snapshot, serverContext.localReadOverlays)
	serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
	serverContext.snapshot = snapshot
	if client != nil {
		serverContext.client = client
		serverContext.clientErr = nil
		if serverContext.clientReady != nil {
			close(serverContext.clientReady)
			serverContext.clientReady = nil
		}
	}
	serverContext.usable = true
	serverContext.revision++
	s.contexts[serverID] = serverContext
	initial := false
	if claim && !s.claimed {
		s.claimed = true
		initial = true
	}
	return mattermostServerViewState(snapshot, false, serverContext.revision), initial, nil
}

func persistMattermostAuthoritativeSnapshot(ctx context.Context, snapshot service.ServerSnapshot, store mattermostAuthoritativeSnapshotStore, observedAt time.Time) (service.ServerSnapshot, error) {
	if err := store.ReplaceMattermostBootstrapSnapshotContext(ctx, mattermostCacheSnapshot(snapshot, observedAt)); err != nil {
		return service.ServerSnapshot{}, err
	}
	boundaryStore, ok := store.(mattermostLastPostAtStore)
	if !ok {
		return snapshot, nil
	}
	channelIDs := make([]string, 0)
	for _, section := range snapshot.Sections {
		for _, entry := range section.Channels {
			channelIDs = append(channelIDs, entry.Channel.ID)
		}
	}
	boundaries, err := boundaryStore.MattermostChannelLastPostAtsContext(ctx, snapshot.Server.ID, channelIDs)
	if err != nil {
		return service.ServerSnapshot{}, err
	}
	for channelID, lastPostAt := range boundaries {
		updateMattermostSnapshotChannel(&snapshot, channelID, func(entry service.ChannelEntry) service.ChannelEntry {
			entry.Channel.LastPostAt = max(entry.Channel.LastPostAt, lastPostAt)
			return entry
		})
	}
	return snapshot, nil
}

func mattermostChannelCoversPost(channel mattermost.Channel, createdAt int64) bool {
	return createdAt > 0 && createdAt <= channel.LastPostAt
}

func (s *mattermostStartup) switchMsg(serverID string) tea.Msg {
	s.mu.RLock()
	defer s.mu.RUnlock()
	serverContext := s.contexts[ids.ServerID(serverID)]
	if !serverContext.usable {
		return nil
	}
	return ui.ServerSwitchedMsg{Server: mattermostServerViewState(serverContext.snapshot, false, serverContext.revision)}
}

func (s *mattermostStartup) usable(serverID ids.ServerID) bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.contexts[serverID].usable
}

func mattermostServerViewState(snapshot service.ServerSnapshot, initialActive bool, revision uint64) ui.ServerViewState {
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
				unread := service.ChannelHasUnread(entry)
				readState[item.ID] = cache.ReadState{HasUnread: unread}
				hasUnread = hasUnread || unread
			}
		}
	}
	return ui.ServerViewState{ServerID: ids.ServerID(snapshot.Server.ID), Revision: revision, ServerName: snapshot.Server.Name, Channels: channels, FinderItems: finder, UserNames: users, UserID: snapshot.CurrentUser.ID, SectionsProvider: staticSectionsProvider{sections: sections}, ReadState: readState, HasUnread: hasUnread, InitialActive: initialActive}
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
