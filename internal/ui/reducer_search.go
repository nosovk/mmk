// internal/ui/reducer_search.go
//
// Reducer for the in-channel `/` search results plus the App-level
// helpers that drive match navigation (n/N with wrap) and the
// status-line `/query  i/N` segment.
//
// Message family:
//
//	ChannelSearchResultsMsg   - FTS match list for the active channel
//	WorkspaceSearchResultsMsg - server-side search.messages results
//	                            for the ctrl+f modal
//
// Stale results (the user switched channels while the query ran) are
// dropped. An error clears search state and toasts; an empty result
// clears state and shows "no matches" in the status line. Otherwise
// the match list becomes the active search, highlights are pushed to
// the messages pane, and the selection jumps to the nearest match at
// or older than the cursor (FetchAround-ing a history window when the
// match is outside the loaded buffer).
package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/debuglog"
	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/text"
)

// activeSearch is the in-channel `/` search state: the query, its
// folded terms (for highlighting), the match ts list newest-first,
// and the index of the currently-selected match.
type activeSearch struct {
	query   string
	terms   []string
	matches []string
	idx     int
}

var reduceSearch reducerFunc = func(a *App, msg tea.Msg) (tea.Cmd, bool) {
	switch m := msg.(type) {
	case ChannelSearchResultsMsg:
		return reduceChannelSearchResults(a, m), true
	case WorkspaceSearchResultsMsg:
		if !a.searchResults.IsVisible() || a.searchResults.Query() != m.Query {
			return nil, true // stale: modal closed or query changed
		}
		if m.Err != nil {
			a.searchResults.SetError("Search failed: " + m.Err.Error())
			return nil, true
		}
		a.searchResults.SetHighlightTerms(workspaceHighlightTerms(m.Query))
		a.searchResults.SetResults(m.Items, m.Total)
		return nil, true
	}
	return nil, false
}

// workspaceHighlightTerms derives the highlightable terms of a
// workspace search query for the ctrl+f results modal: whitespace-split
// tokens minus Slack search modifiers (any token containing ':', e.g.
// from:@user, in:#general, before:2026-01-01), each folded (text.Fold)
// for case- and accent-insensitive matching. Surrounding quote runes
// are stripped so a quoted phrase highlights its individual words
// (`"foo bar"` → foo, bar); tokens that strip to nothing are dropped.
// An empty result disables highlighting.
func workspaceHighlightTerms(query string) []string {
	var terms []string
	for _, tok := range strings.Fields(query) {
		if strings.Contains(tok, ":") {
			continue
		}
		tok = strings.Trim(tok, `"`)
		if tok == "" {
			continue
		}
		terms = append(terms, text.Fold(tok))
	}
	return terms
}

// reduceChannelSearchResults applies an in-channel `/` FTS result:
// stale/superseded drop, error toast, no-match status, or install the
// match list and jump to the nearest match.
func reduceChannelSearchResults(a *App, m ChannelSearchResultsMsg) tea.Cmd {
	if m.ChannelID != a.activeChannelID {
		return nil // stale: channel changed since query
	}
	if m.Gen != a.searchGen {
		// Superseded: the user cleared the search or dispatched a
		// newer query while this one was in flight.
		return nil
	}
	if m.Err != nil {
		debuglog.General("ChannelSearchResultsMsg: channel=%s query=%q err=%v",
			m.ChannelID, m.Query, m.Err)
		a.clearActiveSearch()
		return func() tea.Msg { return ToastMsg{Text: "Search failed"} }
	}
	if len(m.TSes) == 0 {
		a.clearActiveSearch()
		a.statusbar.SetSearch(fmt.Sprintf("/%s  no matches", m.Query))
		return nil
	}

	a.search = &activeSearch{query: m.Query, terms: m.Terms, matches: m.TSes}
	a.messagepane.SetSearchTerms(m.Terms)

	// Nearest match at or older than the cursor (matches are newest
	// first; slack ts strings compare lexicographically).
	cursorTS := ""
	if sel, ok := a.messagepane.SelectedMessage(); ok {
		cursorTS = sel.TS
	}
	a.search.idx = 0
	for i, ts := range m.TSes {
		if ts <= cursorTS {
			a.search.idx = i
			break
		}
	}
	return a.jumpToCurrentMatch()
}

// jumpToCurrentMatch selects the match at a.search.idx, fetching a
// history window when it is outside the loaded buffer, and refreshes
// the status-line search segment.
func (a *App) jumpToCurrentMatch() tea.Cmd {
	s := a.search
	if s == nil || len(s.matches) == 0 {
		return nil
	}
	a.statusbar.SetSearch(fmt.Sprintf("/%s  %d/%d", s.query, s.idx+1, len(s.matches)))
	ts := s.matches[s.idx]
	if a.messagepane.SelectByTS(ts) {
		return nil
	}
	channels := a.channels
	chID := a.activeChannelID
	return func() tea.Msg {
		return channels.FetchAround(ids.ChannelID(chID), ids.MessageTS(ts))
	}
}

// searchStep moves the match selection by delta with wrap: +1 is the
// next-older match (n, wrapping to newest), -1 the next-newer (N,
// wrapping to oldest). No-op without an active search; toasts on wrap.
func (a *App) searchStep(delta int) tea.Cmd {
	s := a.search
	if s == nil || len(s.matches) == 0 {
		return nil
	}
	var wrapped bool
	s.idx += delta
	if s.idx >= len(s.matches) {
		s.idx, wrapped = 0, true
	} else if s.idx < 0 {
		s.idx, wrapped = len(s.matches)-1, true
	}
	cmd := a.jumpToCurrentMatch()
	if wrapped {
		return tea.Batch(cmd, func() tea.Msg { return ToastMsg{Text: "Search wrapped"} })
	}
	return cmd
}
