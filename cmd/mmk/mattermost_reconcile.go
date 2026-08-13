package main

import (
	"context"
	"errors"
	"fmt"
	"reflect"
	"sync"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
)

type mattermostReconcileStore interface {
	ReplaceMattermostBootstrapSnapshotContext(context.Context, cache.MattermostBootstrapSnapshot) error
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

type mattermostReconcileState struct {
	apply      sync.Mutex
	generation uint64
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
			if err := d.reconcile(ctx); err != nil && ctx.Err() == nil && !errors.Is(err, errMattermostReconciliationSuperseded) && d.diagnostic != nil {
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
	state.apply.Unlock()

	s.mu.RLock()
	serverContext, ok := s.contexts[serverID]
	s.mu.RUnlock()
	if !ok || serverContext.client == nil {
		return errors.New("Mattermost reconciliation client unavailable")
	}

	snapshot, err := service.BootstrapServer(ctx, serverContext.client, serverContext.server)
	if err != nil {
		return fmt.Errorf("bootstrap Mattermost reconciliation for server %q: %w", serverID, err)
	}

	state.apply.Lock()
	defer state.apply.Unlock()
	if state.generation != generation {
		return errMattermostReconciliationSuperseded
	}
	if err := deps.Cache.ReplaceMattermostBootstrapSnapshotContext(ctx, mattermostCacheSnapshot(snapshot, deps.Clock())); err != nil {
		return fmt.Errorf("persist Mattermost reconciliation for server %q: %w", serverID, err)
	}
	s.setSnapshot(serverID, snapshot)
	applied := ui.NewUpdateApplied()
	deps.Send(ui.ServerRefreshedMsg{Server: mattermostServerViewState(snapshot, false), Applied: applied})
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
