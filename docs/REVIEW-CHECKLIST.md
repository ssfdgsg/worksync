# Code Review Checklist (worksync + all projects)

Every code review — by a human or an agent — must verify ALL of the following
before approving a change. This is a standing, cross-project rule; the
worksync repo enforces the mechanical parts via `.githooks/pre-commit`.

## 1. E2E tests
- Does the change touch `internal/transport`, `internal/snapshot`, `internal/coord`,
  `internal/runtime`, or remote push/pull/rollback flows?
  - If yes, run the E2E suite (`docs/E2E-GUIDE.md`): up / commit / rollback,
    and a push → fresh-clone fetch → pull roundtrip.
- Verify rootfs and tracked workspace BOTH restore (the dual-marker acceptance
  criterion). A transport change without an E2E run is a review blocker.

## 2. Secret redaction
- No passwords, API keys, tokens, private keys, or connection strings in
  committed code, fixtures, or docs.
- restic password NEVER goes to a remote store (deliberately not synced; see
  `internal/transport/remote/store.go` pullResticObjects comment).
- Check staged content for the patterns in `.githooks/pre-commit` (AKIA,
  PRIVATE KEY, ghp_, sk-, api_key/password/token assignments, …).
- Verify `.gitignore` covers local secrets (`.env`, `*.pem`, `*.key`,
  `**/restic/password`).

## 3. Unit tests
- `go test ./...` (or the project's equivalent) must pass.
- New/changed behavior should have a regression test (e.g. `remote_test.go`).

## 4. go vet
- `go vet ./...` must be clean.

## 5. Formatting
- `gofmt -l internal/ cmd/` must be empty (or the language's formatter).

## Agent review protocol
1. Run the mechanical gates first (test/vet/format/secret scan).
2. For transport/snapshot changes, state explicitly whether E2E was run and
   what it verified — a "not run" is a blocking gap, not a note.
3. Search diffs for hardcoded secrets (grep patterns from the hook) even if
   the hook passed, since the hook only scans staged content.
