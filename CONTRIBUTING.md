# Contributing

Thanks for your interest in **worksync**! This project is in active v0
development — every contribution, from bug reports to design feedback, is
welcome.

## Ground rules

- The authoritative design lives in
  [`docs/worksync-v0-design.md`](docs/worksync-v0-design.md). Implementations
  should cite the relevant design sections.
- The project targets Go 1.24+. Keep dependencies minimal and CGO-free
  (the state DB uses a pure-Go SQLite driver).
- Never store secrets: no SSH keys, no repository passwords in the tree.
  Credentials come from the user's SSH agent and `WORKSYNC_RESTIC_PASSWORD`
  (or a 0600 keyring file).

## Development workflow

1. Fork the repository and create a feature branch.
2. Make your change, with tests. Integration tests use fake
   `podman`/`restic`/`ssh`/`sftp` binaries so they run anywhere.
3. Verify locally:

   ```sh
   make fmt       # gofmt
   make vet       # go vet ./...
   make test      # go test ./...
   ```

4. Open a pull request against `main`. CI runs the same checks.

## Commit messages

Follow the conventional style: `verb: short summary`, e.g.
`feat: add worksync init --image flag`. Reference design sections where
relevant (e.g. `design §12.1`).

## Reporting issues

Include: the `worksync doctor` output, the `worksync.yaml` you used, the
full command and its output, and whether a real or fake binary was involved.
