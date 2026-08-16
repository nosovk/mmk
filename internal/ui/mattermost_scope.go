package ui

import (
	"context"

	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/wintree"
)

type mattermostHistoryScope struct {
	request        HistoryRequest
	ctx            context.Context
	cancel         context.CancelFunc
	refs           int
	recentInFlight bool
	fetchInFlight  bool
	refreshPending bool
	recentSequence uint64
}

func (a *App) newMattermostHistoryScope(serverID ids.ServerID, channelID string) *mattermostHistoryScope {
	ctx, cancel := context.WithCancel(context.Background())
	a.historyGeneration++
	return &mattermostHistoryScope{
		request: HistoryRequest{ServerID: serverID, ChannelID: channelID, Generation: a.historyGeneration},
		ctx:     ctx,
		cancel:  cancel,
		refs:    1,
	}
}

func (a *App) setFocusedMattermostScope(scope *mattermostHistoryScope) {
	if scope == nil {
		a.activeHistoryRequest = HistoryRequest{}
		a.activeHistoryContext = nil
		a.activeHistoryCancel = nil
		a.statusbar.SetSyncing(false)
		if a.historyRequestObserver != nil {
			a.historyRequestObserver(a.activeHistoryRequest)
		}
		return
	}
	a.activeHistoryRequest = scope.request
	a.activeHistoryContext = scope.ctx
	a.activeHistoryCancel = scope.cancel
	a.statusbar.SetSyncing(scope.recentInFlight)
	if a.historyRequestObserver != nil {
		a.historyRequestObserver(a.activeHistoryRequest)
	}
}

func (a *App) installFocusedMattermostScope(serverID ids.ServerID, channelID string) *mattermostHistoryScope {
	a.releaseMattermostWindowScope(a.focusedWin)
	scope := a.newMattermostHistoryScope(serverID, channelID)
	a.mattermostWindowScopes[a.focusedWin] = scope
	a.publishMattermostHistoryRequests()
	a.setFocusedMattermostScope(scope)
	return scope
}

func (a *App) retainMattermostWindowScope(source, target wintree.LeafID) {
	scope := a.mattermostWindowScopes[source]
	if scope == nil {
		return
	}
	scope.refs++
	a.mattermostWindowScopes[target] = scope
	a.publishMattermostHistoryRequests()
}

func (a *App) releaseMattermostWindowScope(id wintree.LeafID) {
	scope := a.mattermostWindowScopes[id]
	if scope == nil {
		return
	}
	delete(a.mattermostWindowScopes, id)
	a.publishMattermostHistoryRequests()
	scope.refs--
	if scope.refs > 0 {
		return
	}
	scope.cancel()
	delete(a.mattermostFetchingOlder, scope.request)
	delete(a.mattermostHistoryExhausted, scope.request)
	const reason = "message send canceled"
	for key := range a.mattermostThreadSends {
		if key.Request == scope.request {
			delete(a.mattermostThreadSends, key)
		}
	}
	for _, model := range a.modelsForChannel(scope.request.ChannelID) {
		model.MarkDeliveryScopeFailed(string(scope.request.ServerID), scope.request.ChannelID, scope.request.Generation, reason)
	}
}

func (a *App) publishMattermostHistoryRequests() {
	if a.historyRequestsObserver == nil {
		return
	}
	seen := make(map[HistoryRequest]struct{})
	requests := make([]HistoryRequest, 0, len(a.mattermostWindowScopes))
	for _, id := range a.wins.Leaves() {
		scope := a.mattermostWindowScopes[id]
		if scope == nil || scope.ctx.Err() != nil {
			continue
		}
		if _, exists := seen[scope.request]; exists {
			continue
		}
		seen[scope.request] = struct{}{}
		requests = append(requests, scope.request)
	}
	a.historyRequestsObserver(requests)
}

func (a *App) cancelAllMattermostWindowScopes() {
	for id := range a.mattermostWindowScopes {
		a.releaseMattermostWindowScope(id)
	}
	clear(a.mattermostFetchingOlder)
	clear(a.mattermostHistoryExhausted)
	a.setFocusedMattermostScope(nil)
}

func (a *App) restoreFocusedMattermostScope() {
	if a.features.kind != ContextMattermost {
		return
	}
	scope := a.mattermostWindowScopes[a.focusedWin]
	channel, _ := a.wins.Channel(a.focusedWin)
	if scope == nil || scope.request.ServerID != ids.ServerID(a.activeServerID) || scope.request.ChannelID != channel.ID {
		scope = a.installFocusedMattermostScope(ids.ServerID(a.activeServerID), channel.ID)
	}
	a.setFocusedMattermostScope(scope)
}

func (a *App) hasMattermostScope(request HistoryRequest) bool {
	for _, scope := range a.mattermostWindowScopes {
		if scope != nil && scope.request == request {
			return true
		}
	}
	return false
}

func (a *App) mattermostScope(request HistoryRequest) *mattermostHistoryScope {
	for _, scope := range a.mattermostWindowScopes {
		if scope != nil && scope.request == request {
			return scope
		}
	}
	return nil
}

func (a *App) modelsForMattermostScope(request HistoryRequest) []*messages.Model {
	var out []*messages.Model
	for _, id := range a.wins.Leaves() {
		scope := a.mattermostWindowScopes[id]
		if scope != nil && scope.request == request {
			if model := a.winModels[id]; model != nil {
				out = append(out, model)
			}
		}
	}
	return out
}
