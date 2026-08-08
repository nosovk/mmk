// Package help provides a modal overlay that lists all keybindings.
// Entries are derived from any struct of key.Binding fields via FromKeyMap,
// so the modal automatically reflects current user-configured bindings
// without hardcoding key labels.
package help

import (
	"reflect"
	"sort"
	"strings"

	"charm.land/bubbles/v2/key"
	"charm.land/lipgloss/v2"
	"github.com/nosovk/mmk/internal/text"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/overlay"
	"github.com/nosovk/mmk/internal/ui/styles"
	"github.com/muesli/reflow/truncate"
)

// Entry is one row in the help modal: a key label and its description.
type Entry struct {
	Key  string
	Desc string
}

// Model is the help overlay state.
type Model struct {
	entries   []Entry
	filtered  []int // indices into entries that match the current query
	query     string
	selected  int // index into filtered
	visible   bool
	searching bool   // true while typing in the / search input
	footer    string // optional attribution line rendered above the controls footer
}

// New creates a new help overlay.
func New() Model {
	return Model{}
}

// SetEntries replaces the list of help entries.
func (m *Model) SetEntries(entries []Entry) {
	m.entries = entries
	m.filter()
}

// SetFooter sets an optional attribution line rendered above the
// controls footer. Pass "" to clear it.
func (m *Model) SetFooter(s string) {
	m.footer = s
}

// FromKeyMap derives Entries from any struct whose fields are key.Binding.
// Bindings without help metadata (empty Key and Desc) are skipped.
// The returned slice is sorted alphabetically by description (case-insensitive)
// for stable display order.
func FromKeyMap(km any) []Entry {
	var entries []Entry
	v := reflect.ValueOf(km)
	if v.Kind() == reflect.Pointer {
		v = v.Elem()
	}
	if v.Kind() != reflect.Struct {
		return entries
	}
	bindingType := reflect.TypeOf(key.Binding{})
	for i := 0; i < v.NumField(); i++ {
		f := v.Field(i)
		if !f.Type().AssignableTo(bindingType) {
			continue
		}
		b, ok := f.Interface().(key.Binding)
		if !ok {
			continue
		}
		h := b.Help()
		if h.Key == "" && h.Desc == "" {
			continue
		}
		entries = append(entries, Entry{Key: h.Key, Desc: h.Desc})
	}
	sort.SliceStable(entries, func(i, j int) bool {
		return strings.ToLower(entries[i].Desc) < strings.ToLower(entries[j].Desc)
	})
	return entries
}

// Open shows the overlay and resets transient state.
func (m *Model) Open() {
	m.visible = true
	m.query = ""
	m.searching = false
	m.selected = 0
	m.filter()
}

// Close hides the overlay.
func (m *Model) Close() {
	m.visible = false
	m.searching = false
	m.query = ""
}

// IsVisible reports whether the overlay is showing.
func (m Model) IsVisible() bool { return m.visible }

// IsSearching reports whether the user is currently typing in the search box.
func (m Model) IsSearching() bool { return m.searching }

// Query returns the current filter string.
func (m Model) Query() string { return m.query }

// Selected returns the index of the highlighted row within the filtered list.
func (m Model) Selected() int { return m.selected }

// VisibleEntries returns the currently filtered entries in display order.
func (m Model) VisibleEntries() []Entry {
	out := make([]Entry, 0, len(m.filtered))
	for _, idx := range m.filtered {
		out = append(out, m.entries[idx])
	}
	return out
}

// HandleKey processes a key event for the help modal.
//
// Behavior:
//   - While searching (/-mode): printable runes append to the query, backspace
//     deletes, enter exits search mode keeping the filter, esc exits search
//     mode and clears the query.
//   - Otherwise: j/k/arrows scroll, / enters search mode, esc/q/? close the modal.
//
// Returns the model unchanged for chaining clarity; mutations happen in place.
func (m *Model) HandleKey(keyStr string) {
	if m.searching {
		m.handleSearchKey(keyStr)
		return
	}
	switch keyStr {
	case "esc", "q", "?":
		m.Close()
	case "j", "down":
		if m.selected < len(m.filtered)-1 {
			m.selected++
		}
	case "k", "up":
		if m.selected > 0 {
			m.selected--
		}
	case "/":
		m.searching = true
	}
}

func (m *Model) handleSearchKey(keyStr string) {
	switch keyStr {
	case "esc":
		// First esc: leave search and clear query (but keep modal open).
		m.searching = false
		m.query = ""
		m.selected = 0
		m.filter()
	case "enter":
		// Exit search, keep filter applied so user can navigate.
		m.searching = false
	case "backspace":
		if len(m.query) > 0 {
			m.query = m.query[:len(m.query)-1]
			m.selected = 0
			m.filter()
		}
	default:
		// Single printable rune — append to query.
		if len(keyStr) == 1 && keyStr[0] >= 32 && keyStr[0] <= 126 {
			m.query += keyStr
			m.selected = 0
			m.filter()
		}
	}
}

// filter rebuilds the filtered index list from the current query, matching
// against both the key label and the description (case-insensitive substring).
func (m *Model) filter() {
	m.filtered = m.filtered[:0]
	q := text.Fold(m.query)
	for i, e := range m.entries {
		if q == "" ||
			strings.Contains(text.Fold(e.Desc), q) ||
			strings.Contains(text.Fold(e.Key), q) {
			m.filtered = append(m.filtered, i)
		}
	}
	if m.selected >= len(m.filtered) {
		m.selected = 0
	}
}

// listTopOffset is the box-local row of the first keybinding row: top
// border (1) + top padding (1) + title (1) + search/filter line (1) +
// blank separator (1).
const listTopOffset = 5

// maxVisibleRows returns the height of the scroll window for a terminal
// height, matching renderBox's computation.
func maxVisibleRows(termHeight int) int {
	mv := termHeight - 10
	if mv < 5 {
		mv = 5
	}
	if mv > 20 {
		mv = 20
	}
	return mv
}

// visibleWindow returns the [start, end) slice of m.filtered shown for a
// given terminal height, using the same scroll math as the renderer.
func (m *Model) visibleWindow(termHeight int) (int, int) {
	total := len(m.filtered)
	visible := maxVisibleRows(termHeight)
	if visible > total {
		visible = total
	}
	startIdx := 0
	if m.selected >= visible {
		startIdx = m.selected - visible + 1
	}
	endIdx := startIdx + visible
	if endIdx > total {
		endIdx = total
		startIdx = endIdx - visible
		if startIdx < 0 {
			startIdx = 0
		}
	}
	return startIdx, endIdx
}

// BoxSize returns the rendered modal box's outer dimensions. The help box
// height depends on the terminal height and its footer can wrap, so we
// measure the actual render (which has no side effects).
func (m Model) BoxSize(termWidth, termHeight int) (int, int) {
	box := m.renderBox(termWidth, termHeight)
	return lipgloss.Width(box), lipgloss.Height(box)
}

// ClickRow moves the highlight to the keybinding row at box-local localY
// and returns true when the click lands on a visible row. The help modal
// has no activation action, so the caller does not synthesize a key after
// a successful ClickRow; the highlight simply follows the click.
func (m *Model) ClickRow(termWidth, termHeight, localY int) bool {
	row := localY - listTopOffset
	if row < 0 {
		return false
	}
	start, end := m.visibleWindow(termHeight)
	if row >= end-start {
		return false
	}
	m.selected = start + row
	return true
}

// ViewOverlay renders the modal on top of background. Returns the background
// unchanged when the modal is not visible.
func (m Model) ViewOverlay(termWidth, termHeight int, background string) string {
	if !m.visible {
		return background
	}
	box := m.renderBox(termWidth, termHeight)
	if box == "" {
		return background
	}
	return overlay.DimmedOverlay(termWidth, termHeight, background, box, 0.5)
}

func (m Model) renderBox(termWidth, termHeight int) string {
	// Sizing: roughly 60% of the terminal width, capped between 40 and 80
	// columns so the modal stays readable on both small and large terminals.
	overlayWidth := termWidth * 6 / 10
	if overlayWidth < 40 {
		overlayWidth = 40
	}
	if overlayWidth > 80 {
		overlayWidth = 80
	}
	if overlayWidth > termWidth-2 {
		overlayWidth = termWidth - 2
	}
	innerWidth := overlayWidth - 4 // account for border + padding

	// Visible rows: leave headroom for title, search line, blank, footer (~6 lines).
	maxVisible := maxVisibleRows(termHeight)

	bg := styles.Background

	title := lipgloss.NewStyle().
		Bold(true).
		Background(bg).
		Foreground(styles.Primary).
		Render("Keybindings")

	// Search input line: shown either as the active prompt or a hint.
	var inputLine string
	if m.searching {
		var inputText string
		if m.query == "" {
			placeholder := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).Render("Type to filter...")
			inputText = "\u2588 " + placeholder
		} else {
			inputText = m.query + "\u2588"
		}
		inputLine = lipgloss.NewStyle().
			BorderStyle(lipgloss.Border{Left: "\u258c"}).
			BorderLeft(true).
			BorderForeground(styles.Primary).
			BorderBackground(bg).
			PaddingLeft(1).
			Background(bg).
			Foreground(styles.TextPrimary).
			Render(inputText)
	} else if m.query != "" {
		// Show the active filter even after exiting search mode.
		inputLine = lipgloss.NewStyle().
			Background(bg).
			Foreground(styles.TextMuted).
			Italic(true).
			Render("filter: " + m.query)
	} else {
		inputLine = lipgloss.NewStyle().
			Background(bg).
			Foreground(styles.TextMuted).
			Render("Press / to search")
	}

	// Determine which window of the filtered list to render. Shared with
	// ClickRow hit-testing via visibleWindow.
	total := len(m.filtered)
	startIdx, endIdx := m.visibleWindow(termHeight)

	// Width budget per row: leave room for the scrollbar gutter when present.
	showScrollbar := total > maxVisible
	rowWidth := innerWidth
	if showScrollbar {
		rowWidth = innerWidth - 1
	}

	// Reserve enough left-column width to hold the widest key label so
	// descriptions line up. Cap at half the row to keep descriptions readable.
	maxKeyWidth := 0
	for _, e := range m.entries {
		if w := lipgloss.Width(e.Key); w > maxKeyWidth {
			maxKeyWidth = w
		}
	}
	if maxKeyWidth > rowWidth/2 {
		maxKeyWidth = rowWidth / 2
	}

	var thumbStart, thumbEnd int
	if showScrollbar {
		thumbHeight := maxVisible * maxVisible / total
		if thumbHeight < 1 {
			thumbHeight = 1
		}
		denom := total - maxVisible
		if denom < 1 {
			denom = 1
		}
		thumbStart = startIdx * (maxVisible - thumbHeight) / denom
		if thumbStart < 0 {
			thumbStart = 0
		}
		if thumbStart > maxVisible-thumbHeight {
			thumbStart = maxVisible - thumbHeight
		}
		thumbEnd = thumbStart + thumbHeight
	}

	thumbStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.Primary)
	trackStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.Border)

	var rows []string
	for i := startIdx; i < endIdx; i++ {
		idx := m.filtered[i]
		e := m.entries[idx]

		keyLabel := e.Key
		if lipgloss.Width(keyLabel) > maxKeyWidth {
			keyLabel = truncate.StringWithTail(keyLabel, uint(maxKeyWidth), "\u2026")
		}
		descBudget := rowWidth - maxKeyWidth - 2 // gap between key and desc
		if descBudget < 1 {
			descBudget = 1
		}
		desc := e.Desc
		if lipgloss.Width(desc) > descBudget {
			desc = truncate.StringWithTail(desc, uint(descBudget), "\u2026")
		}

		// Row layout: [indicator/space:1] + [keyStyled:maxKeyWidth] + [space:1] + [descStyled:descBudget]
		// Sums to rowWidth exactly.
		var row string
		if i == m.selected {
			indicator := lipgloss.NewStyle().Background(bg).Foreground(styles.Accent).Render("\u258c")
			keyStyled := lipgloss.NewStyle().
				Background(bg).
				Foreground(styles.Accent).
				Bold(true).
				Width(maxKeyWidth).
				Render(keyLabel)
			descStyled := lipgloss.NewStyle().
				Background(bg).
				Foreground(styles.Primary).
				Bold(true).
				Width(descBudget).
				Render(desc)
			row = indicator + keyStyled + " " + descStyled
		} else {
			keyStyled := lipgloss.NewStyle().
				Background(bg).
				Foreground(styles.Accent).
				Width(maxKeyWidth).
				Render(keyLabel)
			descStyled := lipgloss.NewStyle().
				Background(bg).
				Foreground(styles.TextPrimary).
				Width(descBudget).
				Render(desc)
			row = " " + keyStyled + " " + descStyled
		}

		if showScrollbar {
			rel := i - startIdx
			var sb string
			if rel >= thumbStart && rel < thumbEnd {
				sb = thumbStyle.Render("\u2588")
			} else {
				sb = trackStyle.Render("\u2502")
			}
			row += sb
		}
		rows = append(rows, row)
	}

	if total == 0 && m.query != "" {
		rows = append(rows, lipgloss.NewStyle().
			Background(bg).
			Foreground(styles.TextMuted).
			Italic(true).
			Render("No matching keybindings"))
	}

	controlsFooter := lipgloss.NewStyle().
		Background(bg).
		Foreground(styles.TextMuted).
		Render("/ search   esc/q close")

	footer := controlsFooter
	if m.footer != "" {
		// Attribution sits at the very bottom of the modal, centered
		// across the inner width and below the controls line.
		attribution := lipgloss.NewStyle().
			Background(bg).
			Foreground(styles.TextMuted).
			Width(innerWidth).
			Align(lipgloss.Center).
			Render(m.footer)
		footer = controlsFooter + "\n\n" + attribution
	}

	content := title + "\n" + inputLine + "\n\n" + strings.Join(rows, "\n") + "\n\n" + footer

	// Re-paint modal bg+fg after every ANSI reset so trailing/unstyled
	// cells don't leak the dimmed app behind the overlay.
	content = messages.ReapplyBgAfterResets(content, messages.BgANSI()+messages.FgANSI())

	return lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).
		BorderForeground(styles.Primary).
		BorderBackground(bg).
		Background(bg).
		Padding(1, 1).
		Width(overlayWidth).
		Render(content)
}
