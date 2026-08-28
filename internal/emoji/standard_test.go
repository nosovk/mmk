package emoji

import (
	"sort"
	"testing"
)

func TestBuildEntriesIncludesCanonicalStandardEmoji(t *testing.T) {
	entries := BuildEntries(nil)
	for _, entry := range entries {
		if entry.Name == "rocket" {
			if entry.Display != "🚀" {
				t.Fatalf("rocket display=%q want %q", entry.Display, "🚀")
			}
			return
		}
	}
	t.Fatal("BuildEntries(nil) does not include canonical :rocket:")
}

func TestBuildEntriesAreSortedWithoutSlackAliases(t *testing.T) {
	entries := BuildEntries(nil)
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name)
		if entry.Name == "+1" || entry.Name == "thumbsup" {
			t.Fatalf("provider alias %q must not be bundled", entry.Name)
		}
	}
	if !sort.StringsAreSorted(names) {
		t.Fatalf("entries are not sorted: %v", names)
	}
}

func TestBuildEntriesResolvesCustomAliasToCanonicalStandardPreview(t *testing.T) {
	entries := BuildEntries(map[string]string{"launch": "alias:rocket"})
	for _, entry := range entries {
		if entry.Name == "launch" {
			if entry.Display != "🚀" {
				t.Fatalf("launch display=%q want %q", entry.Display, "🚀")
			}
			return
		}
	}
	t.Fatal("BuildEntries did not include custom alias :launch:")
}

func TestSprintPreservesStandardAliasesAndUnknownShortcodes(t *testing.T) {
	const input = ":+1: :thumbsup: :rocket: :provider_custom:"
	if got := Sprint(input); got != input {
		t.Fatalf("Sprint(%q)=%q want literal text", input, got)
	}
}
