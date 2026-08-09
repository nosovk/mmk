# Mattermost Server Registry Migration Implementation Plan

> **For Claude:** REQUIRED SUB-SKILL: Use superpowers:executing-plans to implement this plan task-by-task.

**Goal:** Move Mattermost connection metadata out of the shared `config.toml` into a dedicated, versioned, atomically updated `servers.toml` registry.

**Architecture:** `config.toml` remains owned by UI and legacy settings writers. Mattermost onboarding exclusively owns `servers.toml`, serializes transactions with `servers.lock`, stores PATs in the OS credential store, and conditionally rolls credentials back if registry persistence fails.

**Tech Stack:** Go, pelletier/go-toml v2, OS advisory file locks, Linux Secret Service, Windows Credential Manager, macOS Keychain Services.

---

### Task 1: Add Versioned Server Registry

**Files:**
- Create: `internal/config/server_registry.go`
- Create: `internal/config/server_registry_test.go`
- Modify: `internal/config/config.go`
- Delete: `internal/config/mattermost_document.go`
- Delete: `internal/config/mattermost_document_test.go`

**Step 1: Write failing registry tests**

Test missing-file defaults, version validation, ordered round trips, duplicate ID rejection, unknown-field rejection or documented handling, token-free serialization, and atomic file replacement.

**Step 2: Verify RED**

Run: `go test ./internal/config -run TestServerRegistry -v`

Expected: FAIL because the registry API does not exist.

**Step 3: Implement the registry**

Add:

```go
type ServerRegistry struct {
	Version int                `toml:"version"`
	Servers []MattermostServer `toml:"servers"`
}
```

Use version `1`, preserve server order, reject duplicate IDs and unknown fields, and atomically save through a same-directory temporary file, sync, close, rename, and directory sync where supported. Create registries with mode `0600` and preserve an existing mode only when it is already owner-only. Remove Mattermost server entries from the general `Config` type and delete the targeted general-TOML editor.

**Step 4: Verify GREEN**

Run: `go test ./internal/config -run TestServerRegistry -v`

Expected: PASS.

**Step 5: Commit**

```bash
git add internal/config
git commit -m "refactor: add dedicated Mattermost server registry"
```

### Task 2: Move Onboarding Transaction To Registry

**Files:**
- Modify: `internal/mattermost/add_server.go`
- Modify: `internal/mattermost/add_server_test.go`
- Modify: `internal/mattermost/add_server_transaction_test.go`
- Modify: `cmd/mmk/add_server.go`
- Modify: `cmd/mmk/add_server_test.go`

**Step 1: Write failing transaction tests**

Test that onboarding locks `servers.lock`, reloads `servers.toml` under the lock, preserves concurrent registry additions, updates an existing server in place, and never reads or writes general `config.toml`.

**Step 2: Verify RED**

Run: `go test ./internal/mattermost ./cmd/mmk -run 'Test(AddServerTransaction|SaveMattermost)' -v`

Expected: FAIL because onboarding still edits the general config document.

**Step 3: Implement the registry transaction**

Replace document-edit callbacks with registry load/save callbacks. Keep validation before the lock, then lock, reload, inspect previous credential, write PAT, atomically save registry, and release. Preserve conditional rollback and `ErrConcurrentCredentialChange` behavior.

**Step 4: Verify GREEN**

Run the focused tests and expect PASS.

**Step 5: Commit**

```bash
git add internal/mattermost cmd/mmk
git commit -m "refactor: store Mattermost servers separately"
```

### Task 3: Complete Credential And CI Hardening

**Files:**
- Modify: `internal/mattermost/secret_store_linux.go`
- Modify: `internal/mattermost/secret_store_windows.go`
- Modify: platform secret-store tests
- Modify: `.github/workflows/ci.yml`

**Step 1: Write failing cleanup tests**

Test that every Secret Service result buffer, including duplicate matches and delete paths, is cleared. Retain Windows native blob and temporary buffer clearing tests.

**Step 2: Verify RED**

Run focused secret-store tests and confirm uncleared buffers are observed.

**Step 3: Implement minimal cleanup**

Defer clearing all unlocked Secret Service item values immediately after search. Keep current native Keychain and WinCred behavior. Ensure CI compiles and runs non-live fake-boundary tests on Ubuntu, Windows, and macOS.

**Step 4: Verify GREEN**

Run focused, race, full, vet, build, and platform compile checks.

macOS Keychain behavior requires cgo and is exercised by the native macOS CI job. A separate Darwin `CGO_ENABLED=0` compile job verifies the explicit no-cgo fallback, but Linux cannot execute either Darwin artifact.

**Step 5: Commit**

```bash
git add internal/mattermost .github/workflows/ci.yml
git commit -m "fix: finish credential store hardening"
```
