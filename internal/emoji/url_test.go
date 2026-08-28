package emoji

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

type urlFixture struct {
	Base    string            `json:"base"`
	Entries []urlFixtureEntry `json:"entries"`
}

type urlFixtureEntry struct {
	Name       string `json:"name"`
	Codepoints []rune `json:"codepoints"`
	URL        string `json:"url"`
}

func loadURLFixture(t *testing.T) urlFixture {
	t.Helper()
	path := filepath.Join("testdata", "slack_urls.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	var f urlFixture
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatalf("parse fixture: %v", err)
	}
	if len(f.Entries) == 0 {
		t.Fatalf("fixture has no entries")
	}
	return f
}

func TestBuildStandardEmojiURL(t *testing.T) {
	fixture := loadURLFixture(t)
	for _, e := range fixture.Entries {
		got := BuildStandardEmojiURL(e.Codepoints)
		if got != e.URL {
			t.Errorf("BuildStandardEmojiURL(%q codepoints=%v) = %q, want %q",
				e.Name, e.Codepoints, got, e.URL)
		}
	}
}

func TestCodepointsForShortcode_Unknown(t *testing.T) {
	if _, ok := CodepointsForShortcode("definitely_not_an_emoji_name_xyz"); ok {
		t.Errorf("CodepointsForShortcode(unknown): ok=true, want false")
	}
}

func TestBuildCustomEmojiURL(t *testing.T) {
	customs := map[string]string{
		"party_parrot": "https://emoji.example.com/party_parrot.gif",
		"company_logo": "https://emoji.example.com/company_logo.png",
		"yay":          "alias:party_parrot", // alias to a custom
		"chain_a":      "alias:chain_b",
		"chain_b":      "alias:chain_c",
		"chain_c":      "https://emoji.example.com/chain_c.png",
		"loop_a":       "alias:loop_b",
		"loop_b":       "alias:loop_a",
	}

	cases := []struct {
		name    string
		wantURL string
		wantOK  bool
	}{
		// Direct custom: URL returned verbatim.
		{"party_parrot", "https://emoji.example.com/party_parrot.gif", true},
		{"company_logo", "https://emoji.example.com/company_logo.png", true},

		// alias:<custom>: resolves through to the custom's URL.
		{"yay", "https://emoji.example.com/party_parrot.gif", true},

		// Multi-hop alias chain.
		{"chain_a", "https://emoji.example.com/chain_c.png", true},

		// Alias cycle: detected, returns ok=false.
		{"loop_a", "", false},

		// Unknown name: ok=false.
		{"never_defined", "", false},
	}
	for _, c := range cases {
		got, ok := BuildCustomEmojiURL(c.name, customs)
		if ok != c.wantOK || got != c.wantURL {
			t.Errorf("BuildCustomEmojiURL(%q) = (%q, %v), want (%q, %v)",
				c.name, got, ok, c.wantURL, c.wantOK)
		}
	}
}

func TestURLForShortcode(t *testing.T) {
	customs := map[string]string{
		"party_parrot": "https://emoji.example.com/party_parrot.gif",
		"thumbsup":     "https://emoji.example.com/our_thumbs.png",
	}
	cases := []struct {
		name    string
		wantURL string
		wantOK  bool
	}{
		// Workspace custom wins over kyokomi for the same name.
		{"thumbsup", "https://emoji.example.com/our_thumbs.png", true},

		// Custom-only name.
		{"party_parrot", "https://emoji.example.com/party_parrot.gif", true},

		// Unknown.
		{"never_defined", "", false},
	}
	for _, c := range cases {
		got, ok := URLForShortcode(c.name, customs)
		if ok != c.wantOK || got != c.wantURL {
			t.Errorf("URLForShortcode(%q) = (%q, %v), want (%q, %v)",
				c.name, got, ok, c.wantURL, c.wantOK)
		}
	}
}
