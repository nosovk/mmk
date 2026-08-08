package help

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// footerLine returns the ANSI-stripped line containing needle from a
// rendered modal, or "" if not found.
func footerLine(rendered, needle string) string {
	for _, raw := range strings.Split(rendered, "\n") {
		line := ansi.Strip(raw)
		if strings.Contains(line, needle) {
			return line
		}
	}
	return ""
}

func TestFooterRendersWhenSet(t *testing.T) {
	m := New()
	m.SetEntries(sampleEntries())
	m.SetFooter("mmk dev - footer line")
	m.Open()

	out := m.ViewOverlay(100, 40, "")
	if !strings.Contains(out, "mmk dev - footer line") {
		t.Errorf("expected footer text in rendered modal, got:\n%s", out)
	}
}

func TestFooterAbsentWhenEmpty(t *testing.T) {
	m := New()
	m.SetEntries(sampleEntries())
	m.Open()

	out := m.ViewOverlay(100, 40, "")
	if strings.Contains(out, "Made with") {
		t.Errorf("did not expect a footer line when none set, got:\n%s", out)
	}
}

func TestFooterRendersAtBottom(t *testing.T) {
	m := New()
	m.SetEntries(sampleEntries())
	m.SetFooter("mmk dev - attribution")
	m.Open()

	out := ansi.Strip(m.ViewOverlay(100, 40, ""))
	controlsAt := strings.Index(out, "esc/q close")
	footerAt := strings.Index(out, "attribution")
	if controlsAt < 0 || footerAt < 0 {
		t.Fatalf("missing controls or footer in output:\n%s", out)
	}
	if footerAt < controlsAt {
		t.Errorf("attribution should be below the controls line (at bottom); got controls@%d footer@%d", controlsAt, footerAt)
	}
}

func TestFooterIsCentered(t *testing.T) {
	m := New()
	m.SetEntries(sampleEntries())
	m.SetFooter("mmk dev - centered")
	m.Open()

	out := m.ViewOverlay(100, 40, "")
	// Both lines live in the same box, so they share the same screen
	// offset + border prefix. The controls line is left-aligned, so a
	// centered footer must start at a larger column than it.
	ctrl := footerLine(out, "/ search")
	attr := footerLine(out, "centered")
	if ctrl == "" || attr == "" {
		t.Fatalf("missing controls or footer line in:\n%s", ansi.Strip(out))
	}
	ctrlCol := strings.Index(ctrl, "/ search")
	attrCol := strings.Index(attr, "mmk dev")
	if attrCol <= ctrlCol {
		t.Errorf("expected centered footer indented more than left-aligned controls; attr@%d ctrl@%d", attrCol, ctrlCol)
	}
}
