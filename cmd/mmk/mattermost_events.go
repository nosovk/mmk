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
	"github.com/nosovk/mmk/internal/ui"
)

type mattermostEventStore interface {
	UpsertMattermostRealtimePostContext(context.Context, string, cache.MattermostPost) error
}

type mattermostEventDeps struct {
	Cache           mattermostEventStore
	Send            func(context.Context, tea.Msg) error
	ActiveSelection func() (ids.ServerID, string)
	Diagnostic      func(error)
}

type mattermostEventAdapter struct {
	deps mattermostEventDeps
}

func newMattermostEventAdapter(deps mattermostEventDeps) *mattermostEventAdapter {
	return &mattermostEventAdapter{deps: deps}
}

func mattermostProductionEventHandler(cache mattermostEventStore, send func(context.Context, tea.Msg) error, activeSelection func() (ids.ServerID, string), diagnostic func(error)) func(context.Context, ids.ServerID, mattermost.Event) {
	adapter := newMattermostEventAdapter(mattermostEventDeps{Cache: cache, Send: send, ActiveSelection: activeSelection, Diagnostic: diagnostic})
	return adapter.Handle
}

func (a *mattermostEventAdapter) Handle(ctx context.Context, serverID ids.ServerID, event mattermost.Event) {
	posted, ok := event.(mattermost.PostedEvent)
	if !ok || a == nil || isNilMattermostEventDependency(a.deps.Cache) {
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
	if err := a.deps.Cache.UpsertMattermostRealtimePostContext(ctx, string(serverID), post); err != nil {
		if ctx.Err() == nil {
			a.diagnostic(errors.New("Mattermost posted event persistence failed"))
		}
		return
	}
	if a.deps.Send == nil || a.deps.ActiveSelection == nil {
		return
	}
	activeServer, activeChannel := a.deps.ActiveSelection()
	if activeServer != serverID || activeChannel != message.ChannelID {
		return
	}
	if err := a.deps.Send(ctx, ui.MattermostHistoryRefreshMsg{ServerID: serverID, ChannelID: message.ChannelID}); err != nil && ctx.Err() == nil {
		a.diagnostic(errors.New("Mattermost posted event UI notification failed"))
	}
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
