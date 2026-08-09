package mattermost

import (
	"strings"
	"testing"
)

func TestCanonicalServerRootEquivalentSpellings(t *testing.T) {
	inputs := []string{
		"https://Chat.Example.com",
		"https://chat.example.com/",
		"https://chat.example.com/api/v4",
		"https://chat.example.com/api/v4/",
		"https://chat.example.com:443/api/v4",
		"https://chat.example.com/teams/../api/v4",
	}

	var firstRoot, firstID string
	for _, input := range inputs {
		root, err := CanonicalServerRoot(input)
		if err != nil {
			t.Fatalf("CanonicalServerRoot(%q): %v", input, err)
		}
		id := ServerID(root)
		if firstRoot == "" {
			firstRoot, firstID = root, id
		}
		if root != firstRoot || id != firstID {
			t.Errorf("%q => (%q, %q), want (%q, %q)", input, root, id, firstRoot, firstID)
		}
	}
	if firstRoot != "https://chat.example.com" {
		t.Errorf("canonical root = %q", firstRoot)
	}
	if !strings.HasPrefix(firstID, "chat-example-com-") || len(firstID) < 40 {
		t.Errorf("server ID = %q, want safe host slug plus collision-resistant hash", firstID)
	}
}

func TestCanonicalServerRootNormalizesDNSRootDotAndIDNA(t *testing.T) {
	inputs := []string{
		"https://example.com./mattermost",
		"https://bücher.example/mattermost",
		"https://xn--bcher-kva.example/mattermost",
	}
	wants := []string{
		"https://example.com/mattermost",
		"https://xn--bcher-kva.example/mattermost",
		"https://xn--bcher-kva.example/mattermost",
	}
	for i, input := range inputs {
		got, err := CanonicalServerRoot(input)
		if err != nil {
			t.Fatalf("CanonicalServerRoot(%q): %v", input, err)
		}
		if got != wants[i] {
			t.Errorf("CanonicalServerRoot(%q) = %q, want %q", input, got, wants[i])
		}
	}
	if ServerID(wants[1]) != ServerID(wants[2]) {
		t.Fatal("Unicode and punycode hostnames produced different IDs")
	}
}

func TestCanonicalServerRootRejectsInvalidIDNA(t *testing.T) {
	if _, err := CanonicalServerRoot("https://a..example.com"); err == nil {
		t.Fatal("accepted invalid IDNA hostname")
	}
}

func TestCanonicalServerRootKeepsDeploymentSubpathsDistinct(t *testing.T) {
	rootA, err := CanonicalServerRoot("https://example.com/mattermost/api/v4")
	if err != nil {
		t.Fatal(err)
	}
	rootB, err := CanonicalServerRoot("https://example.com/other/api/v4")
	if err != nil {
		t.Fatal(err)
	}
	if rootA != "https://example.com/mattermost" {
		t.Errorf("root A = %q", rootA)
	}
	if rootA == rootB || ServerID(rootA) == ServerID(rootB) {
		t.Fatal("distinct deployment subpaths collapsed")
	}
}

func TestNewClientUsesCanonicalServerRoot(t *testing.T) {
	client, err := NewClient("https://Chat.Example.com/mattermost/api/v4/", "token")
	if err != nil {
		t.Fatal(err)
	}
	if got := client.baseURL.String(); got != "https://chat.example.com/mattermost/api/v4" {
		t.Errorf("base URL = %q", got)
	}
}
