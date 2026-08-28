package main

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
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

func TestVersionTextIsMattermostOnly(t *testing.T) {
	got := versionText("1.2.3", "abc123", "2026-08-18")
	for _, want := range []string{"mmk 1.2.3", "commit abc123", "built 2026-08-18"} {
		if !strings.Contains(got, want) {
			t.Errorf("version text missing %q: %q", want, got)
		}
	}
	if strings.Contains(got, "Slack") {
		t.Fatalf("version text contains Slack disclaimer: %q", got)
	}
}

func TestEmptyServerRegistryReturnsAddServerInstruction(t *testing.T) {
	err := requireMattermostServers(0)
	if err == nil || !strings.Contains(err.Error(), "mmk --add-server") {
		t.Fatalf("error = %v", err)
	}
	if err := requireMattermostServers(1); err != nil {
		t.Fatalf("non-empty registry error = %v", err)
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

func TestDarwinReleaseBuildEnablesNativeKeychainBridge(t *testing.T) {
	root := filepath.Join("..", "..")
	goreleaser := readIdentityFile(t, filepath.Join(root, ".goreleaser.yaml"))
	for _, want := range []string{"id: mmk-darwin", "CGO_ENABLED=1"} {
		if !strings.Contains(goreleaser, want) {
			t.Errorf(".goreleaser.yaml missing %q", want)
		}
	}
	workflow := readIdentityFile(t, filepath.Join(root, ".github", "workflows", "release.yml"))
	if !strings.Contains(workflow, "runs-on: macos-latest") {
		t.Error("release workflow must run on macOS to link Security.framework")
	}
}

func TestCICompilesPlatformCredentialAdaptersWithoutLiveStores(t *testing.T) {
	workflow := readIdentityFile(t, filepath.Join("..", "..", ".github", "workflows", "ci.yml"))
	for _, want := range []string{
		"runs-on: windows-latest",
		"runs-on: macos-latest",
		"CGO_ENABLED: 1",
		"go test ./internal/mattermost ./internal/config ./internal/lockfile ./cmd/mmk",
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf("ci.yml missing %q", want)
		}
	}
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

func TestGoSourcesDoNotImportRemovedRuntimePackages(t *testing.T) {
	root := filepath.Join("..", "..")
	forbidden := slackRuntimeImportPattern()
	if err := walkGoSources(root, func(path string) {
		assertForbidden(t, path, readIdentityFile(t, path), forbidden)
	}); err != nil {
		t.Fatalf("scan repository Go sources: %v", err)
	}
	for _, dir := range forbiddenSlackRuntimeDirs() {
		path := filepath.Join(root, "internal", dir)
		if _, err := os.Stat(path); err == nil {
			t.Errorf("deleted runtime directory exists: %s", path)
		} else if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("stat %s: %v", path, err)
		}
	}
}

func slackRuntimeImportPattern() *regexp.Regexp {
	return regexp.MustCompile("[\"`]github\\.com/(?:slack-go/slack(?:/[^\"`]*)?|nosovk/mmk/internal/(?:slack(?:desktop|fmt|http|url)?|usergroups)(?:/[^\"`]*)?)[\"`]")
}

func forbiddenSlackRuntimeDirs() []string {
	return []string{"slack", "slackdesktop", "slackfmt", "slackhttp", "slackurl", "usergroups"}
}

func walkGoSources(root string, visit func(path string)) error {
	return filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			if entry.Name() == ".git" || entry.Name() == ".worktrees" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) == ".go" {
			visit(path)
		}
		return nil
	})
}

func TestSlackRuntimeGuardCoversTestImportsAndDeletedPackageDirectories(t *testing.T) {
	pattern := slackRuntimeImportPattern()
	for _, source := range []string{
		`import "github.com/` + `slack-go/slack"`,
		"import `github.com/" + "slack-go/slack`",
		`import slackhttp "github.com/nosovk/mmk/internal/` + `slackhttp"`,
		`import "github.com/nosovk/mmk/internal/` + `usergroups"`,
	} {
		if !pattern.MatchString(source) {
			t.Fatalf("guard did not match %q", source)
		}
	}
	for _, source := range []string{
		`// historical attribution: github.com/` + `slack-go/slack`,
		`const path = "internal/` + `slackhttp"`,
	} {
		if pattern.MatchString(source) {
			t.Fatalf("guard matched non-import reference %q", source)
		}
	}
	want := []string{"slack", "slackdesktop", "slackfmt", "slackhttp", "slackurl", "usergroups"}
	if got := forbiddenSlackRuntimeDirs(); !reflect.DeepEqual(got, want) {
		t.Fatalf("forbiddenSlackRuntimeDirs() = %v, want %v", got, want)
	}
}

func TestWalkGoSourcesIncludesPackagesOutsideCmdAndInternal(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "tools", "guard_fixture.go")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("package tools\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []string
	if err := walkGoSources(root, func(path string) { got = append(got, path) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{path}) {
		t.Fatalf("walkGoSources() = %v, want %v", got, []string{path})
	}
}

func TestWalkGoSourcesSkipsProjectWorktrees(t *testing.T) {
	root := t.TempDir()
	regular := filepath.Join(root, "tools", "guard_fixture.go")
	worktree := filepath.Join(root, ".worktrees", "other-branch", "guard_fixture.go")
	for _, path := range []string{regular, worktree} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("package tools\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var got []string
	if err := walkGoSources(root, func(path string) { got = append(got, path) }); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, []string{regular}) {
		t.Fatalf("walkGoSources() = %v, want %v", got, []string{regular})
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
