// Package linkpicker provides the modal overlay that lets the user
// pick which link in a message to open (issue #62). Opened by the
// `o` keybinding when the selected message has more than one link;
// the chosen link is dispatched as ui.OpenLinkMsg by the mode handler.
package linkpicker

// Item is one selectable link row.
type Item struct {
	URL   string
	Label string // empty for bare links
	// InApp marks links that the router will navigate inside mmk
	// (active-workspace archive permalinks); rendered with a badge.
	InApp bool
}

// Model is the link picker overlay state.
type Model struct {
	items    []Item
	selected int
	visible  bool
}

// New creates a hidden link picker.
func New() *Model { return &Model{} }

// Open shows the picker over items, with the first row selected.
func (m *Model) Open(items []Item) {
	m.items = items
	m.selected = 0
	m.visible = true
}

// Close hides the picker and drops its items.
func (m *Model) Close() {
	m.visible = false
	m.items = nil
	m.selected = 0
}

// IsVisible reports whether the picker is showing.
func (m *Model) IsVisible() bool { return m.visible }

// Items returns the current rows (for rendering and tests).
func (m *Model) Items() []Item { return m.items }

// Selected returns the highlighted row index.
func (m *Model) Selected() int { return m.selected }

// HandleKey processes one key. Returns (item, true) when the user
// chose a link with enter (the picker closes itself); (Item{}, false)
// otherwise. esc/q close without choosing.
func (m *Model) HandleKey(key string) (Item, bool) {
	switch key {
	case "esc", "q":
		m.Close()
	case "j", "down":
		if m.selected < len(m.items)-1 {
			m.selected++
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
		}
	case "enter":
		if len(m.items) == 0 {
			return Item{}, false
		}
		item := m.items[m.selected]
		m.Close()
		return item, true
	}
	return Item{}, false
}
