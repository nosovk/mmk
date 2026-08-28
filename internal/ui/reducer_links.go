// internal/ui/reducer_links.go
//
// Link-open routing.
//
// OpenLinkMsg is the single place every link open flows through. Until a
// Mattermost permalink design exists, every URL opens in the OS browser.
package ui

import (
	tea "charm.land/bubbletea/v2"

	"github.com/nosovk/mmk/internal/ids"
	"github.com/nosovk/mmk/internal/ui/messages"
)

// pendingMessageNav is the not-yet-completed tail of a workspace-search
// navigation. It is unrelated to URL routing.
type pendingMessageNav struct {
	channelID string
	messageTS string
	threadTS  string // non-empty: open the thread panel instead of selecting
}

var reduceLinks reducerFunc = func(a *App, msg tea.Msg) (tea.Cmd, bool) {
	m, ok := msg.(OpenLinkMsg)
	if !ok {
		return nil, false
	}
	return a.browserOpener(m.URL), true
}

// completePendingMessageNav finishes (or drops) a workspace-search
// navigation for channelID. authoritative=true means "no more message
// data is coming for this channel" — if the target ts still isn't in
// the buffer, dispatch ChannelService.FetchAround to load a history
// window centered on the target instead of waiting.
//
// Called from the workspace-search handler and reduceChannels,
// and reduceChannels' MessagesLoadedMsg arm (authoritative).
func (a *App) completePendingMessageNav(channelID string, authoritative bool) tea.Cmd {
	p := a.pendingMessageNav
	if p == nil {
		return nil
	}
	if p.channelID != channelID {
		// The user navigated somewhere unrelated before the link
		// target finished loading; the pending nav is stale.
		a.pendingMessageNav = nil
		return nil
	}
	if p.threadTS != "" {
		a.pendingMessageNav = nil
		return a.openThreadForMessageNav(p.channelID, p.threadTS)
	}
	if a.messagepane.SelectByTS(p.messageTS) {
		a.pendingMessageNav = nil
		return nil
	}
	if authoritative {
		a.pendingMessageNav = nil
		channels := a.channels
		chID, ts := p.channelID, p.messageTS
		return func() tea.Msg {
			return channels.FetchAround(ids.ChannelID(chID), ids.MessageTS(ts))
		}
	}
	return nil
}

func (a *App) openThreadForMessageNav(channelID, threadTS string) tea.Cmd {
	if !a.allows(FeatureThreadPanel) {
		return nil
	}
	parent := messages.MessageItem{ID: threadTS, TS: threadTS, RootID: threadTS, ThreadTS: threadTS}
	if channelID == a.activeChannelID {
		for _, item := range a.messagepane.Messages() {
			if item.MessageID() == threadTS {
				parent = item
				break
			}
		}
	}
	if parent.Text == "" {
		if cached := a.threads.CacheRead(ids.ChannelID(channelID), ids.ThreadTS(threadTS)); len(cached) > 0 {
			parent = cached[0]
		}
	}
	return a.openThreadPanel(parent, channelID, threadTS)
}
