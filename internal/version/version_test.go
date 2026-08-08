package version

import (
	"strings"
	"testing"
)

func TestModalFooter(t *testing.T) {
	got := ModalFooter("v1.2.3")

	// Version is interpolated.
	if !strings.Contains(got, "mmk v1.2.3") {
		t.Errorf("footer missing version: %q", got)
	}
	// Attribution wording.
	if !strings.Contains(got, "Made with") {
		t.Errorf("footer missing 'Made with': %q", got)
	}
	if !strings.Contains(got, "\u2764") {
		t.Errorf("footer missing heart glyph: %q", got)
	}
	if !strings.Contains(got, "Grant Ammons") {
		t.Errorf("footer missing author: %q", got)
	}
	// URL is present and OSC-8 wrapped (clickable), with visible label.
	if !strings.Contains(got, "https://grant.dev") {
		t.Errorf("footer missing url: %q", got)
	}
	if !strings.Contains(got, "\x1b]8;;https://grant.dev\x1b\\") {
		t.Errorf("footer url not OSC-8 wrapped: %q", got)
	}
	// URL is parenthesized.
	if !strings.Contains(got, "(") || !strings.Contains(got, ")") {
		t.Errorf("footer url not parenthesized: %q", got)
	}
}

func TestModalFooterUsesGivenVersion(t *testing.T) {
	got := ModalFooter("dev")
	if !strings.Contains(got, "mmk dev") {
		t.Errorf("expected 'mmk dev' in %q", got)
	}
}
