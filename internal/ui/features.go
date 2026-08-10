package ui

import "github.com/nosovk/mmk/internal/ui/help"

type ContextKind uint8

const (
	ContextSlack ContextKind = iota
	ContextMattermost
)

type Feature uint8

const (
	FeatureThreads Feature = iota
	FeatureReactions
	FeatureUploads
	FeatureEditDelete
	FeatureMarkUnread
	FeaturePresence
	FeatureWorkspaceSearch
	FeatureRemoteChannelSearch
	FeatureNewConversation
	FeaturePermalink
	FeatureSlackExternal
	FeatureTyping
	FeatureSend
)

type FeatureSet struct {
	kind     ContextKind
	disabled map[Feature]bool
}

func SlackFeatures() FeatureSet { return FeatureSet{kind: ContextSlack} }

func MattermostTask8Features() FeatureSet {
	disabled := make(map[Feature]bool)
	for feature := FeatureThreads; feature <= FeatureSend; feature++ {
		disabled[feature] = true
	}
	return FeatureSet{kind: ContextMattermost, disabled: disabled}
}

func (f FeatureSet) Allows(feature Feature) bool { return !f.disabled[feature] }

func (a *App) allows(feature Feature) bool { return a.features.Allows(feature) }

func (a *App) helpEntries() []help.Entry {
	entries := help.FromKeyMap(a.keys)
	if a.features.kind == ContextSlack {
		return entries
	}
	hidden := map[string]Feature{
		"add reaction": FeatureReactions, "navigate reactions": FeatureReactions, "list reactions": FeatureReactions,
		"edit message": FeatureEditDelete, "delete message": FeatureEditDelete,
		"mark unread": FeatureMarkUnread, "search workspace": FeatureWorkspaceSearch,
		"new message": FeatureNewConversation, "copy permalink": FeaturePermalink,
		"set status": FeaturePresence, "insert mode": FeatureSend,
		"toggle thread": FeatureThreads, "save thread": FeatureThreads,
	}
	out := make([]help.Entry, 0, len(entries))
	for _, entry := range entries {
		if feature, ok := hidden[entry.Desc]; ok && !a.features.Allows(feature) {
			continue
		}
		out = append(out, entry)
	}
	return out
}
