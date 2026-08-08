package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestApplicationIdentity(t *testing.T) {
	got := applicationIdentity()

	if got.executable != "mmk" {
		t.Errorf("executable = %q, want %q", got.executable, "mmk")
	}
	if got.configDirectory != "mmk" {
		t.Errorf("config directory = %q, want %q", got.configDirectory, "mmk")
	}
	if got.displayName != "mmk" {
		t.Errorf("display name = %q, want %q", got.displayName, "mmk")
	}
}

func TestRepositoryIdentitySurfaces(t *testing.T) {
	root := filepath.Join("..", "..")

	flake := readIdentityFile(t, filepath.Join(root, "flake.nix"))
	for _, want := range []string{`pname = "mmk";`, "packages.default = mmk;", "packages.mmk = mmk;"} {
		if !strings.Contains(flake, want) {
			t.Errorf("flake.nix missing %q", want)
		}
	}
	if strings.Contains(flake, "packages.slk") {
		t.Error("flake.nix still exports packages.slk")
	}

	wikiFiles, err := filepath.Glob(filepath.Join(root, "wiki", "*.md"))
	if err != nil {
		t.Fatalf("glob active wiki pages: %v", err)
	}
	staleIdentity := regexp.MustCompile(`github\.com/gammons/(?:slk|mmk)|getslk\.sh|cmd/slk|slk_|\bslk\b|SLK_|slk-debug|(?:config|cache|share)/slk`)
	for _, name := range wikiFiles {
		body := readIdentityFile(t, name)
		for lineNumber, line := range strings.Split(body, "\n") {
			if strings.Contains(line, "derived from [gammons/slk]") {
				continue
			}
			if stale := staleIdentity.FindString(line); stale != "" {
				t.Errorf("%s:%d contains stale identity %q", filepath.Base(name), lineNumber+1, stale)
			}
		}
	}
}

func readIdentityFile(t *testing.T, name string) string {
	t.Helper()
	body, err := os.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	return string(body)
}
