package main

import (
	"context"
	"errors"
	"reflect"
	"sync"

	tea "charm.land/bubbletea/v2"
	"github.com/nosovk/mmk/internal/cache"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/mattermost"
	"github.com/nosovk/mmk/internal/service"
	"github.com/nosovk/mmk/internal/ui"
	"github.com/nosovk/mmk/internal/ui/messages"
)

type mattermostEventStore interface {
	UpsertMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) (bool, error)
	PersistMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) error
}

type mattermostEventDeps struct {
	Cache               mattermostEventStore
	Send                func(context.Context, tea.Msg) error
	ActiveSelection     func() (ids.ServerID, string)
	LiveHistoryRequests func() []ui.HistoryRequest
	Startup             *mattermostStartup
	Diagnostic          func(error)
}

type mattermostEventAdapter struct {
	deps mattermostEventDeps
}

func newMattermostEventAdapter(deps mattermostEventDeps) *mattermostEventAdapter {
	return &mattermostEventAdapter{deps: deps}
}

func mattermostProductionEventHandler(cache mattermostEventStore, send func(context.Context, tea.Msg) error, activeSelection func() (ids.ServerID, string), liveHistoryRequests func() []ui.HistoryRequest, startup *mattermostStartup, diagnostic func(error)) func(context.Context, ids.ServerID, mattermost.Event) {
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: cache, Send: send, ActiveSelection: activeSelection, LiveHistoryRequests: liveHistoryRequests, Startup: startup, Diagnostic: diagnostic})
	return adapter.Handle
}

func (a *mattermostEventAdapter) Handle(ctx context.Context, serverID ids.ServerID, event mattermost.Event) {
	if a == nil {
		return
	}
	var runtimeState *mattermostReconcileState
	if a.deps.Startup != nil {
		runtimeState = a.deps.Startup.reconcileState(serverID)
		runtimeState.runtime.Lock()
		defer func() {
			if runtimeState != nil {
				runtimeState.runtime.Unlock()
			}
		}()
	}
	posted, isPosted := event.(mattermost.PostedEvent)
	applyPosted := false
	var realtime []ui.MattermostRealtimePostMsg
	if isPosted {
		var requests []ui.HistoryRequest
		if a.deps.LiveHistoryRequests != nil {
			requests = a.deps.LiveHistoryRequests()
		}
		if posted.Message.CreatedAt <= 0 {
			return
		}
		if isNilMattermostEventDependency(a.deps.Cache) {
			return
		}
		message := posted.Message
		message.ServerID = string(serverID)
		post := cache.MattermostPost{
			ID: message.ID, ChannelID: message.ChannelID, UserID: message.UserID,
			RootID: message.RootID, Text: message.Text, CorrelationID: message.CorrelationID,
			CreatedAt: message.CreatedAt, UpdatedAt: message.UpdatedAt, EditedAt: message.EditedAt,
			DeletedAt: message.DeletedAt, ReplyCount: message.ReplyCount,
		}
		covered := false
		if a.deps.Startup != nil {
			a.deps.Startup.mu.RLock()
			serverContext, ok := a.deps.Startup.contexts[serverID]
			if ok {
				entry, found := mattermostSnapshotChannel(serverContext.snapshot, message.ChannelID)
				covered = found && mattermostChannelCoversPost(entry.Channel, message.CreatedAt)
			}
			a.deps.Startup.mu.RUnlock()
		}
		claimed := false
		var err error
		if covered {
			err = a.deps.Cache.PersistMattermostRealtimePostContext(ctx, string(serverID), post)
		} else {
			claimed, err = a.deps.Cache.UpsertMattermostRealtimePostContext(ctx, string(serverID), post)
		}
		if err != nil {
			if ctx.Err() == nil {
				a.diagnostic(errors.New("Mattermost posted event persistence failed"))
			}
			return
		}
		applyPosted = claimed
		item := a.mattermostRealtimeItem(serverID, message)
		seen := make(map[ui.HistoryRequest]struct{}, len(requests))
		for _, request := range requests {
			if request.ServerID != serverID || request.ChannelID != message.ChannelID {
				continue
			}
			if _, exists := seen[request]; exists {
				continue
			}
			seen[request] = struct{}{}
			realtime = append(realtime, ui.MattermostRealtimePostMsg{Request: request, Message: item})
		}
	}
	if a.deps.Startup != nil {
		changed := false
		var state ui.ServerViewState
		if applyPosted {
			state, changed = a.applyPostedRuntime(serverID, posted, runtimeState)
		} else if !isPosted {
			state, changed = a.deps.Startup.updateRuntimeEventWithSelection(serverID, event, a.deps.ActiveSelection)
		}
		runtimeState.runtime.Unlock()
		runtimeState = nil
		if changed && a.deps.Send != nil {
			if err := a.deps.Send(ctx, ui.ServerRefreshedMsg{Server: state}); err != nil && ctx.Err() == nil {
				a.diagnostic(errors.New("Mattermost runtime event UI notification failed"))
			}
		}
	}
	if a.deps.Send != nil {
		for _, msg := range realtime {
			if err := a.deps.Send(ctx, msg); err != nil && ctx.Err() == nil {
				a.diagnostic(errors.New("Mattermost posted event UI notification failed"))
			}
		}
	}
	activeServer, activeChannel := ids.ServerID(""), ""
	if a.deps.ActiveSelection != nil {
		activeServer, activeChannel = a.deps.ActiveSelection()
	}
	if !isPosted || a.deps.Send == nil || activeServer != serverID || activeChannel != posted.Message.ChannelID {
		return
	}
	if err := a.deps.Send(ctx, ui.MattermostHistoryRefreshMsg{ServerID: serverID, ChannelID: posted.Message.ChannelID}); err != nil && ctx.Err() == nil {
		a.diagnostic(errors.New("Mattermost posted event UI notification failed"))
	}
}

func (a *mattermostEventAdapter) mattermostRealtimeItem(serverID ids.ServerID, message mattermost.Message) messages.MessageItem {
	userName := message.UserID
	if a.deps.Startup != nil {
		a.deps.Startup.mu.RLock()
		serverContext, ok := a.deps.Startup.contexts[serverID]
		if ok {
			for _, user := range serverContext.snapshot.Users {
				if user.ID == message.UserID {
					userName = user.DisplayName()
					break
				}
			}
		}
		a.deps.Startup.mu.RUnlock()
	}
	if userName == "" {
		userName = message.UserID
	}
	items := mattermostHistoryItems([]service.MattermostHistoryMessage{{Message: message, UserName: userName}})
	return items[0]
}

func (a *mattermostEventAdapter) applyPostedRuntime(serverID ids.ServerID, posted mattermost.PostedEvent, runtimeState *mattermostReconcileState) (ui.ServerViewState, bool) {
	message := posted.Message
	activeServer, activeChannel := ids.ServerID(""), ""
	if a.deps.ActiveSelection != nil {
		activeServer, activeChannel = a.deps.ActiveSelection()
	}
	activelyViewed := activeServer == serverID && activeChannel == message.ChannelID
	a.deps.Startup.mu.Lock()
	defer a.deps.Startup.mu.Unlock()
	serverContext, ok := a.deps.Startup.contexts[serverID]
	if !ok || !serverContext.usable {
		return ui.ServerViewState{}, false
	}
	entry, ok := mattermostSnapshotChannel(serverContext.snapshot, message.ChannelID)
	if !ok {
		return ui.ServerViewState{}, false
	}
	metadata := mattermostAppliedPost{ID: message.ID, ChannelID: message.ChannelID, CreatedAt: message.CreatedAt, ActivelyViewed: activelyViewed}
	if mattermostChannelCoversPost(entry.Channel, message.CreatedAt) {
		return ui.ServerViewState{}, false
	}
	snapshot := serverContext.snapshot
	if !updateMattermostSnapshotChannel(&snapshot, message.ChannelID, func(entry service.ChannelEntry) service.ChannelEntry {
		return service.ChannelWithNewPost(entry, activelyViewed)
	}) {
		return ui.ServerViewState{}, false
	}
	serverContext.snapshot = snapshot
	serverContext.revision++
	a.deps.Startup.contexts[serverID] = serverContext
	if runtimeState.inFlight != nil {
		runtimeState.inFlight.posts[message.ID] = metadata
	}
	return mattermostServerViewState(snapshot, false, serverContext.revision), true
}

func (a *mattermostEventAdapter) diagnostic(err error) {
	if a.deps.Diagnostic != nil {
		a.deps.Diagnostic(err)
	}
}

type mattermostEventDispatcher struct {
	serverID ids.ServerID
	handle   func(context.Context, ids.ServerID, mattermost.Event)
	mu       sync.Mutex
	queue    []mattermost.Event
	wake     chan struct{}
	stopped  bool
}

func newMattermostEventDispatcher(serverID ids.ServerID, handle func(context.Context, ids.ServerID, mattermost.Event)) *mattermostEventDispatcher {
	return &mattermostEventDispatcher{serverID: serverID, handle: handle, wake: make(chan struct{}, 1)}
}

func (d *mattermostEventDispatcher) Enqueue(event mattermost.Event) {
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.queue = append(d.queue, event)
	d.mu.Unlock()
	select {
	case d.wake <- struct{}{}:
	default:
	}
}

// Run processes queued value events in FIFO order. Cancellation discards any
// remaining queue so shutdown is bounded by the context-aware in-flight work.
func (d *mattermostEventDispatcher) Run(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			d.clear()
			return
		}
		event, ok := d.pop()
		if ok {
			d.handle(ctx, d.serverID, event)
			continue
		}
		select {
		case <-ctx.Done():
			d.clear()
			return
		case <-d.wake:
		}
	}
}

func (d *mattermostEventDispatcher) pop() (mattermost.Event, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if len(d.queue) == 0 {
		return nil, false
	}
	event := d.queue[0]
	d.queue[0] = nil
	if len(d.queue) == 1 {
		d.queue = nil
	} else {
		d.queue = d.queue[1:]
	}
	return event, true
}

func (d *mattermostEventDispatcher) clear() {
	d.mu.Lock()
	d.stopped = true
	clear(d.queue)
	d.queue = nil
	d.mu.Unlock()
}

func isNilMattermostEventDependency(value any) bool {
	if value == nil {
		return true
	}
	reflected := reflect.ValueOf(value)
	return reflected.Kind() == reflect.Pointer && reflected.IsNil()
}
