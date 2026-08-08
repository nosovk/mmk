package main

import (
	"io/fs"
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

func TestBuildAndReleaseIdentitySurfaces(t *testing.T) {
	root := filepath.Join("..", "..")

	assertContains(t, filepath.Join(root, "go.mod"), "module github.com/nosovk/mmk")
	assertFileContainsAll(t, filepath.Join(root, "Makefile"), []string{
		"BINARY=mmk",
		"-o $(BUILD_DIR)/$(BINARY) ./cmd/mmk",
	})
	assertFileContainsAll(t, filepath.Join(root, ".goreleaser.yaml"), []string{
		"project_name: mmk",
		"main: ./cmd/mmk",
		"binary: mmk",
		"package_name: mmk",
		"owner: nosovk",
		"name: mmk",
	})

	if info, err := os.Stat(filepath.Join(root, "cmd", "mmk")); err != nil || !info.IsDir() {
		t.Errorf("cmd/mmk must exist as a directory: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "cmd", "slk")); !os.IsNotExist(err) {
		t.Errorf("cmd/slk must not exist, stat error = %v", err)
	}

	flake := readIdentityFile(t, filepath.Join(root, "flake.nix"))
	for _, want := range []string{`pname = "mmk";`, "packages.default = mmk;", "packages.mmk = mmk;"} {
		if !strings.Contains(flake, want) {
			t.Errorf("flake.nix missing %q", want)
		}
	}
	if strings.Contains(flake, "packages.slk") {
		t.Error("flake.nix still exports packages.slk")
	}

	gitignore := readIdentityFile(t, filepath.Join(root, ".gitignore"))
	for _, want := range []string{"/mmk", "mmk-debug.log"} {
		if !strings.Contains(gitignore, want) {
			t.Errorf(".gitignore missing %q", want)
		}
	}
	assertForbidden(t, ".gitignore", gitignore, regexp.MustCompile(`(?m)^/slk$|^slk-debug\.log$`))
}

func TestGoSourcesPreserveIdentityAndUpstreamReferences(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := regexp.MustCompile(malformedUpstreamIssuePattern() + `|\bmmk\s+"github\.com/nosovk/mmk/internal/slack"|github\.com/gammons/slk/internal|cmd/slk|SLK_|slk-debug\.log`)
	for _, sourceRoot := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, sourceRoot), func(path string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || path == filepath.Join(root, "cmd", "mmk", "main_test.go") {
				return nil
			}
			assertForbidden(t, path, readIdentityFile(t, path), forbidden)
			return nil
		})
		if err != nil {
			t.Fatalf("scan %s Go sources: %v", sourceRoot, err)
		}
	}
}

func malformedUpstreamIssuePattern() string {
	return `gammons/mmk#[0-9]+`
}

func TestMalformedUpstreamIssuePattern(t *testing.T) {
	pattern := regexp.MustCompile(malformedUpstreamIssuePattern())
	for _, issue := range []string{"gammons/mmk#1", "gammons/mmk#42", "gammons/mmk#12345"} {
		if !pattern.MatchString(issue) {
			t.Errorf("pattern does not reject %q", issue)
		}
	}
	for _, valid := range []string{"gammons/slk#5", "github.com/nosovk/mmk/issues/5", "gammons/mmk"} {
		if pattern.MatchString(valid) {
			t.Errorf("pattern incorrectly rejects %q", valid)
		}
	}
}

func TestActiveDocumentationIdentity(t *testing.T) {
	root := filepath.Join("..", "..")
	wikiFiles, err := filepath.Glob(filepath.Join(root, "wiki", "*.md"))
	if err != nil {
		t.Fatalf("glob active wiki pages: %v", err)
	}
	activeDocs := append(wikiFiles,
		filepath.Join(root, "README.md"),
		filepath.Join(root, "docs", "STATUS.md"),
	)
	forbidden := regexp.MustCompile(`github\.com/gammons/mmk|github\.com/gammons/slk/cmd|cmd/slk|(?:^|[ /])slk_|\bSLK_[A-Z_]+|slk-debug\.log|\.(?:config|cache)/slk|\.local/share/slk|XDG_[A-Z_]+/slk|(?m)^# slk Implementation Status$|(?m)^slk/$`)
	for _, name := range activeDocs {
		assertForbidden(t, name, readIdentityFile(t, name), forbidden)
	}
}

func TestReleaseHostingAssumptionIsDocumented(t *testing.T) {
	readme := readIdentityFile(t, filepath.Join("..", "..", "README.md"))
	for _, want := range []string{
		"github.com/nosovk/mmk",
		"automatically generated",
		"GITHUB_TOKEN",
		"gammons/slk",
		"cannot publish",
		"PAT",
		"GitHub App",
		"workflow change",
	} {
		if !strings.Contains(readme, want) {
			t.Errorf("README release notes missing %q", want)
		}
	}
}

func assertContains(t *testing.T, name, want string) {
	t.Helper()
	if body := readIdentityFile(t, name); !strings.Contains(body, want) {
		t.Errorf("%s missing %q", name, want)
	}
}

func assertFileContainsAll(t *testing.T, name string, wants []string) {
	t.Helper()
	body := readIdentityFile(t, name)
	for _, want := range wants {
		if !strings.Contains(body, want) {
			t.Errorf("%s missing %q", name, want)
		}
	}
}

func assertForbidden(t *testing.T, name, body string, pattern *regexp.Regexp) {
	t.Helper()
	for lineNumber, line := range strings.Split(body, "\n") {
		if stale := pattern.FindString(line); stale != "" {
			t.Errorf("%s:%d contains forbidden identity %q", name, lineNumber+1, stale)
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
