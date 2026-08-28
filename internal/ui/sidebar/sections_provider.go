package sidebar

// SectionsProvider supplies ordered sidebar sections to the model.
// When non-nil and Ready returns true, the model uses provider data
// instead of the config-glob path. The interface keeps the sidebar
// package free of cross-package dependencies.
type SectionsProvider interface {
	Ready() bool
	// OrderedSections returns sections in the order they should
	// render, already filtered to the renderable set. Each entry is
	// the data the sidebar needs for the header row.
	OrderedSections() []SectionMeta
}

type SectionKind uint8

const (
	SectionKindUnknown SectionKind = iota
	SectionKindStandard
	SectionKindChannels
	SectionKindDirect
	SectionKindApps
	SectionKindStars
	SectionKindTeam
)

// SectionMeta is the sidebar's provider-neutral view of one section.
type SectionMeta struct {
	ID    string
	Name  string
	Emoji string // shortcode like "orange_book"; empty for none
	Kind  SectionKind
	Type  string // legacy Slack fallback until Task 15
}

func (s SectionMeta) EffectiveKind() SectionKind {
	if s.Kind != SectionKindUnknown {
		return s.Kind
	}
	switch s.Type {
	case "standard":
		return SectionKindStandard
	case "channels":
		return SectionKindChannels
	case "direct_messages":
		return SectionKindDirect
	case "recent_apps":
		return SectionKindApps
	case "stars":
		return SectionKindStars
	default:
		return SectionKindUnknown
	}
}
