package newmessagepicker

import (
	"fmt"
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/muesli/reflow/truncate"
	"github.com/nosovk/mmk/internal/ui/messages"
	"github.com/nosovk/mmk/internal/ui/overlay"
	"github.com/nosovk/mmk/internal/ui/styles"
)

// View renders just the modal box. Use ViewOverlay for the
// dimmed-backdrop composite that the App actually paints.
func (m Model) View(termWidth int) string {
	return m.renderBox(termWidth)
}

// BoxSize returns the rendered modal box's outer dimensions. This modal's
// footer can wrap at narrow widths, so we measure the actual render rather
// than computing height analytically. renderBox has no side effects.
// termHeight is accepted for interface symmetry.
func (m Model) BoxSize(termWidth, termHeight int) (int, int) {
	box := m.renderBox(termWidth)
	return lipgloss.Width(box), lipgloss.Height(box)
}

// ViewOverlay renders the modal centered on a dimmed copy of the
// current screen. background is the already-rendered base screen.
func (m Model) ViewOverlay(termWidth, termHeight int, background string) string {
	if !m.visible {
		return background
	}
	box := m.renderBox(termWidth)
	if box == "" {
		return background
	}
	return overlay.DimmedOverlay(termWidth, termHeight, background, box, 0.5)
}

func (m Model) renderBox(termWidth int) string {
	if !m.visible {
		return ""
	}

	overlayWidth := boxWidth(termWidth)
	innerWidth := overlayWidth - 4
	bg := styles.Background

	title := lipgloss.NewStyle().
		Bold(true).
		Background(bg).
		Foreground(styles.Primary).
		Render("New message")

	input := m.renderInputRow(innerWidth, bg)
	rows := m.renderResultRows(innerWidth, bg)
	footer := m.renderFooter(innerWidth, bg)

	content := title + "\n" + input + "\n\n" + strings.Join(rows, "\n") + "\n\n" + footer

	// Re-paint modal bg+fg after every ANSI reset emitted by inner
	// styled spans, otherwise trailing spaces inherit the dimmed-
	// app background. Same fix channelfinder uses.
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

// renderInputRow renders the "To: [pill] [pill] |query█" row.
func (m Model) renderInputRow(innerWidth int, bg color.Color) string {
	prefix := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).Render("To: ")

	var b strings.Builder
	b.WriteString(prefix)

	pillStyle := lipgloss.NewStyle().
		Background(styles.Primary).
		Foreground(styles.Background).
		Padding(0, 1)

	for _, u := range m.users {
		if _, ok := m.selected[u.ID]; !ok {
			continue
		}
		b.WriteString(pillStyle.Render(u.DisplayName))
		b.WriteString(" ")
	}

	queryStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.TextPrimary)
	if m.query == "" && len(m.selected) == 0 {
		placeholder := lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).Render("type a name…")
		b.WriteString(placeholder)
	} else {
		b.WriteString(queryStyle.Render(m.query))
		b.WriteString(queryStyle.Render("█"))
	}

	row := b.String()
	if lipgloss.Width(row) > innerWidth {
		row = truncate.StringWithTail(row, uint(innerWidth), "…")
	}
	return row
}

// renderResultRows produces up to 10 visible result rows with a
// scrollbar when the list overflows. Highlighted row gets a left bar
// and the Primary foreground.
func (m Model) renderResultRows(innerWidth int, bg color.Color) []string {
	total := len(m.filtered)

	if total == 0 {
		var msg string
		if m.query != "" {
			msg = fmt.Sprintf("No users match %q", m.query)
		} else {
			msg = "No users available"
		}
		return []string{
			lipgloss.NewStyle().
				Background(bg).
				Foreground(styles.TextMuted).
				Italic(true).
				Render(msg),
		}
	}

	startIdx, endIdx := m.visibleWindow()
	visible := endIdx - startIdx

	showScrollbar := total > visible
	contentWidth := innerWidth - 1 // 1 col for the highlight indicator
	if showScrollbar {
		contentWidth--
	}

	var thumbStart, thumbEnd int
	if showScrollbar {
		thumbHeight := visible * visible / total
		if thumbHeight < 1 {
			thumbHeight = 1
		}
		denom := total - visible
		if denom < 1 {
			denom = 1
		}
		thumbStart = startIdx * (visible - thumbHeight) / denom
		if thumbStart < 0 {
			thumbStart = 0
		}
		if thumbStart > visible-thumbHeight {
			thumbStart = visible - thumbHeight
		}
		thumbEnd = thumbStart + thumbHeight
	}
	thumbStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.Primary)
	trackStyle := lipgloss.NewStyle().Background(bg).Foreground(styles.Border)

	var rows []string
	for i := startIdx; i < endIdx; i++ {
		u := m.users[m.filtered[i]]
		isHighlight := i == m.highlight

		row := m.renderRow(u, contentWidth, isHighlight, bg)
		if showScrollbar {
			rel := i - startIdx
			if rel >= thumbStart && rel < thumbEnd {
				row += thumbStyle.Render("\u2588")
			} else {
				row += trackStyle.Render("\u2502")
			}
		}
		rows = append(rows, row)
	}
	return rows
}

// renderRow renders a single user row: display name, @handle, [ext]
// tag if external, and a trailing "✓" if the user is in the pill bar.
func (m Model) renderRow(u User, width int, highlight bool, bg color.Color) string {
	name := u.DisplayName
	if u.IsExternal {
		name += " [ext]"
	}

	handle := ""
	if u.Username != "" && u.Username != u.DisplayName {
		handle = lipgloss.NewStyle().
			Background(bg).
			Foreground(styles.TextMuted).
			Render(" @" + u.Username)
	}

	var nameStyle lipgloss.Style
	if highlight {
		nameStyle = lipgloss.NewStyle().Background(bg).Foreground(styles.Primary).Bold(true)
	} else {
		nameStyle = lipgloss.NewStyle().Background(bg).Foreground(styles.TextPrimary)
	}
	nameRendered := nameStyle.Render(name)

	check := ""
	if _, sel := m.selected[u.ID]; sel {
		check = lipgloss.NewStyle().Background(bg).Foreground(styles.Accent).Render(" ✓")
	}

	line := nameRendered + handle + check
	if lipgloss.Width(line) > width {
		line = truncate.StringWithTail(line, uint(width), "…")
	}
	if pad := width - lipgloss.Width(line); pad > 0 {
		line += strings.Repeat(" ", pad)
	}

	if highlight {
		indicator := lipgloss.NewStyle().Background(bg).Foreground(styles.Accent).Render("▌")
		return indicator + line
	}
	return " " + line
}

// renderFooter is the key-hints + N/8 counter row.
func (m Model) renderFooter(innerWidth int, bg color.Color) string {
	left := lipgloss.NewStyle().
		Background(bg).
		Foreground(styles.TextMuted).
		Render("space toggle  enter open  esc cancel")

	counterText := fmt.Sprintf("%d / %d", len(m.selected), MaxRecipients)
	var counterStyle lipgloss.Style
	if len(m.selected) >= MaxRecipients {
		counterStyle = lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted).Italic(true)
		counterText += "  MPIM limit reached"
	} else {
		counterStyle = lipgloss.NewStyle().Background(bg).Foreground(styles.TextMuted)
	}
	right := counterStyle.Render(counterText)

	gap := innerWidth - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		gap = 1
	}
	return left + strings.Repeat(" ", gap) + right
}
