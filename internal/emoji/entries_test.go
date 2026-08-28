package emoji

import (
	"sort"
	"testing"
)

func TestBuildEntriesIncludesSortedCanonicalStandardEmojiWithoutAliases(t *testing.T) {
	entries := BuildEntries(nil)
	if len(entries) == 0 {
		t.Fatal("BuildEntries(nil) returned no standard emoji")
	}

	names := make([]string, 0, len(entries))
	byName := make(map[string]string, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
		byName[entry.Name] = entry.Display
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("entries are not sorted: %v", names)
	}
	if got := byName["rocket"]; got != "🚀" {
		t.Fatalf(":rocket: display = %q, want 🚀", got)
	}
	for _, alias := range []string{"+1", "thumbsup"} {
		if _, ok := byName[alias]; ok {
			t.Fatalf("provider alias %q unexpectedly present in canonical entries", alias)
		}
	}
}

func TestBuildEntriesOverlaysProviderCustomEntries(t *testing.T) {
	const customURL = "https://mattermost.example/emoji/rocket.png"
	entries := BuildEntries(map[string]string{
		"party_parrot": "https://mattermost.example/emoji/parrot.gif",
		"rocket":       customURL,
	})

	byName := make(map[string]string, len(entries))
	for _, entry := range entries {
		byName[entry.Name] = entry.Display
	}
	if got := byName["rocket"]; got != placeholderGlyph {
		t.Fatalf("custom :rocket: display = %q, want placeholder %q", got, placeholderGlyph)
	}
	if got := byName["party_parrot"]; got != placeholderGlyph {
		t.Fatalf("custom :party_parrot: display = %q, want placeholder %q", got, placeholderGlyph)
	}
}

func TestProviderShortcodesRemainLiteralWithoutCustomURL(t *testing.T) {
	want := []string{":+1:", ":thumbsup:", ":rocket:"}
	for _, shortcode := range want {
		if got := Sprint(shortcode); got != shortcode {
			t.Errorf("Sprint(%q) = %q, want literal shortcode", shortcode, got)
		}
		if got := ResolveShortcodesInText(shortcode); got != shortcode {
			t.Errorf("ResolveShortcodesInText(%q) = %q, want literal shortcode", shortcode, got)
		}
		name := shortcode[1 : len(shortcode)-1]
		if url, ok := URLForShortcode(name, nil); ok || url != "" {
			t.Errorf("URLForShortcode(%q, nil) = (%q, %v), want no provider URL", name, url, ok)
		}
	}
}

func TestShortcodeResolutionUsesExplicitProviderCustomURL(t *testing.T) {
	const customURL = "https://mattermost.example/emoji/rocket.png"
	if got, ok := URLForShortcode("rocket", map[string]string{"rocket": customURL}); !ok || got != customURL {
		t.Fatalf("URLForShortcode custom rocket = (%q, %v), want (%q, true)", got, ok, customURL)
	}

	entries := BuildEntries(map[string]string{"rocket": customURL})
	i := sort.Search(len(entries), func(i int) bool { return entries[i].Name >= "rocket" })
	if i == len(entries) || entries[i] != (EmojiEntry{Name: "rocket", Display: placeholderGlyph}) {
		t.Fatalf("custom rocket did not overlay canonical entry: %#v", entries)
	}
}
