package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
)

type mattermostReconcileStore interface {
	mattermostAuthoritativeSnapshotStore
	ListMattermostChannelTimeline(string, string, int, string) ([]cache.MattermostPost, error)
	ListMattermostUsers(string) ([]cache.MattermostUser, error)
	UpsertMattermostHistoryContext(context.Context, string, []cache.MattermostPost, []cache.MattermostUser) error
}

type mattermostReconcileDeps struct {
	Cache                   mattermostReconcileStore
	Send                    func(tea.Msg)
	Clock                   func() time.Time
	ActiveSelection         func() (ids.ServerID, string)
	ActiveSelectionSnapshot func() (ids.ServerID, string, uint64)
}

var errMattermostReconciliationSuperseded = errors.New("Mattermost reconciliation superseded")

type mattermostAppliedPost struct {
	ID             string
	ChannelID      string
	CreatedAt      int64
	ActivelyViewed bool
}

type mattermostReconcileJournal struct {
	epoch uint64
	posts map[string]mattermostAppliedPost
}

type mattermostReconcileState struct {
	apply      sync.Mutex
	runtime    sync.Mutex
	generation uint64
	inFlight   *mattermostReconcileJournal
}

type mattermostReconcileDispatcher struct {
	mu         sync.Mutex
	pending    bool
	stopped    bool
	wake       chan struct{}
	reconcile  func(context.Context) error
	diagnostic func(error)
}

func newMattermostReconcileDispatcher(reconcile func(context.Context) error, diagnostic func(error)) *mattermostReconcileDispatcher {
	return &mattermostReconcileDispatcher{wake: make(chan struct{}, 1), reconcile: reconcile, diagnostic: diagnostic}
}

func (d *mattermostReconcileDispatcher) Enqueue() {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.pending = true
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

func (d *mattermostReconcileDispatcher) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			d.stop()
			return
		}
		if d.takePending() {
			err := d.reconcile(ctx)
			if err != nil && ctx.Err() == nil && !errors.Is(err, errMattermostReconciliationSuperseded) && d.diagnostic != nil {
				d.diagnostic(err)
			}
			continue
		}
		select {
		case <-ctx.Done():
			d.stop()
			return
		case <-d.wake:
		}
	}
}

func (d *mattermostReconcileDispatcher) takePending() bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if !d.pending {
		return false
	}
	d.pending = false
	return true
}

func (d *mattermostReconcileDispatcher) stop() {
	d.mu.Lock()
	d.stopped = true
	d.pending = false
	d.mu.Unlock()
}

func (s *mattermostStartup) reconcile(ctx context.Context, serverID ids.ServerID, deps mattermostReconcileDeps) error {
	if err := validateMattermostReconcileDeps(deps); err != nil {
		return err
	}
	state := s.reconcileState(serverID)
	state.apply.Lock()
	state.generation++
	generation := state.generation
	state.runtime.Lock()
	state.inFlight = &mattermostReconcileJournal{epoch: generation, posts: make(map[string]mattermostAppliedPost)}
	state.runtime.Unlock()
	state.apply.Unlock()
	s.mu.RLock()
	serverContext, ok := s.contexts[serverID]
	s.mu.RUnlock()
	if !ok || serverContext.client == nil {
		return errors.New("Mattermost reconciliation client unavailable")
	}

	snapshot, err := service.BootstrapServer(ctx, serverContext.client, serverContext.server)
	if err != nil {
		clearMattermostReconcileJournal(state, generation)
		return fmt.Errorf("bootstrap Mattermost reconciliation for server %q: %w", serverID, err)
	}

	state.apply.Lock()
	defer state.apply.Unlock()
	if state.generation != generation {
		clearMattermostReconcileJournal(state, generation)
		return errMattermostReconciliationSuperseded
	}
	state.runtime.Lock()
	if state.inFlight == nil || state.inFlight.epoch != generation {
		state.runtime.Unlock()
		return errMattermostReconciliationSuperseded
	}
	journal := state.inFlight
	snapshot, err = persistMattermostAuthoritativeSnapshot(ctx, snapshot, deps.Cache, deps.Clock())
	if err != nil {
		state.inFlight = nil
		state.runtime.Unlock()
		return fmt.Errorf("persist Mattermost reconciliation for server %q: %w", serverID, err)
	}
	s.mu.Lock()
	serverContext, ok = s.contexts[serverID]
	if !ok {
		s.mu.Unlock()
		state.inFlight = nil
		state.runtime.Unlock()
		return errors.New("Mattermost server context unavailable")
	}
	snapshot = rebaseMattermostJournal(snapshot, journal.posts)
	snapshot = replayMattermostLocalReads(snapshot, serverContext.localReadOverlays)
	serverContext.localReadOverlays = make(map[string]mattermostLocalReadOverlay)
	serverContext.snapshot = snapshot
	serverContext.usable = true
	serverContext.revision++
	s.contexts[serverID] = serverContext
	serverState := mattermostServerViewState(snapshot, false, serverContext.revision)
	state.inFlight = nil
	s.mu.Unlock()
	state.runtime.Unlock()
	applied := ui.NewUpdateApplied()
	deps.Send(ui.ServerRefreshedMsg{Server: serverState, Applied: applied})
	select {
	case <-applied.Done():
	case <-ctx.Done():
		return ctx.Err()
	}

	activeServer, channelID := deps.ActiveSelection()
	selectionGeneration := uint64(0)
	if deps.ActiveSelectionSnapshot != nil {
		activeServer, channelID, selectionGeneration = deps.ActiveSelectionSnapshot()
	}
	if activeServer != serverID {
		return nil
	}
	if channelID == "" {
		return nil
	}
	history := service.NewMattermostHistoryService(string(serverID), serverContext.client, deps.Cache, mattermostHistoryPageSize)
	page, err := history.FetchRecent(ctx, channelID)
	deps.Send(ui.MattermostReconciledHistoryMsg{
		ServerID: serverID, ChannelID: channelID, Generation: selectionGeneration,
		AuthoritativeIDs: page.AuthoritativeIDs, DeletedIDs: page.DeletedIDs,
		Messages: mattermostHistoryItems(page.Messages), HasMore: page.HasMore, Err: err,
	})
	if err != nil {
		return fmt.Errorf("reconcile Mattermost history for server %q channel %q: %w", serverID, channelID, err)
	}
	return nil
}

func (s *mattermostStartup) reconcileState(serverID ids.ServerID) *mattermostReconcileState {
	state, _ := s.reconcileStates.LoadOrStore(serverID, &mattermostReconcileState{})
	return state.(*mattermostReconcileState)
}

func clearMattermostReconcileJournal(state *mattermostReconcileState, epoch uint64) {
	state.runtime.Lock()
	if state.inFlight != nil && state.inFlight.epoch == epoch {
		state.inFlight = nil
	}
	state.runtime.Unlock()
}

func rebaseMattermostJournal(snapshot service.ServerSnapshot, posts map[string]mattermostAppliedPost) service.ServerSnapshot {
	ordered := make([]mattermostAppliedPost, 0, len(posts))
	for _, post := range posts {
		ordered = append(ordered, post)
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].CreatedAt != ordered[j].CreatedAt {
			return ordered[i].CreatedAt < ordered[j].CreatedAt
		}
		return ordered[i].ID < ordered[j].ID
	})
	for _, post := range ordered {
		entry, ok := mattermostSnapshotChannel(snapshot, post.ChannelID)
		if !ok || mattermostChannelCoversPost(entry.Channel, post.CreatedAt) {
			continue
		}
		updateMattermostSnapshotChannel(&snapshot, post.ChannelID, func(entry service.ChannelEntry) service.ChannelEntry {
			return service.ChannelWithNewPost(entry, post.ActivelyViewed)
		})
	}
	return snapshot
}

func validateMattermostReconcileDeps(deps mattermostReconcileDeps) error {
	if isNilMattermostReconcileDependency(deps.Cache) {
		return errors.New("Mattermost reconciliation cache must not be nil")
	}
	if deps.Send == nil {
		return errors.New("Mattermost reconciliation send callback must not be nil")
	}
	if deps.Clock == nil {
		return errors.New("Mattermost reconciliation clock must not be nil")
	}
	if deps.ActiveSelection == nil {
		return errors.New("Mattermost reconciliation active selection callback must not be nil")
	}
	return nil
}

func isNilMattermostReconcileDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	switch reflected.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice:
		return reflected.IsNil()
	default:
		return false
	}
}
