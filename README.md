# worksync

[![Go Version](https://img.shields.io/badge/Go-1.24+-00ADD8?logo=go&logoColor=white)](go.mod)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](LICENSE)

**Portable development environments for the terminal** — like Git, but for
your dev environment. `worksync` turns a YAML spec into a rootless Podman
container, freezes the environment and selected volumes into immutable,
content-addressed snapshot commits, and moves those commits between machines
over SSH/SFTP.

```text
up → modify → commit → push → pull → up
```

**[简体中文](README.zh-CN.md)**

## Highlights

- **Declarative project spec** (`worksync.yaml`) — one file describes the
  runtime, container image, ports and volumes; `worksync init` scaffolds it.
- **Environment commits** — `podman commit` of the container rootfs, exported
  as an OCI archive; every blob digest is verified before storage.
- **Volume snapshots** — selected volumes are snapshotted with restic into an
  encrypted repository.
- **Remote sync** — an SSH/SFTP content-addressed store with compare-and-swap
  ref files and deduplicated uploads; credentials come from your SSH agent and
  worksync never stores keys.
- **No daemon** — only `podman`, `restic`, `ssh`/`sftp` are invoked; nothing
  is installed on the host.

## Requirements

- Go 1.24+ to build: `go build ./cmd/worksync`
- [Podman](https://podman.io) (rootless). On macOS, `podman machine`.
- [restic](https://restic.net) for volume snapshots.
- `ssh`/`sftp` from OpenSSH for remote push/pull.
- SSH agent with your key loaded for remote operations.

## Quick start

```sh
worksync init            # scaffold worksync.yaml in the current directory
worksync up              # create and start the dev container
worksync shell           # open an interactive shell
# ... modify your workspace ...
worksync commit -m "wip: add feature"   # freeze env + volumes
worksync log             # show commit history
worksync rollback latest # restore a previous commit

# move to another machine
worksync remote add origin ssh://user@host/~/worksync-store
worksync push origin     # upload objects + ref (deduplicated)
# on the other machine
worksync pull origin     # download and apply
worksync up              # recreate the container
```

## Commands

| Command | Purpose |
| --- | --- |
| `init` | scaffold `worksync.yaml` |
| `up` | create/start the container (idempotent; rebuilds on config drift) |
| `status` | project/container/volumes/ports state |
| `shell`, `exec -- cmd` | run inside the container |
| `stop`, `start`, `rm` | lifecycle (volumes are kept unless confirmed) |
| `ports`, `expose`, `unexpose` | published ports |
| `commit -m MSG` | freeze environment + selected volumes |
| `log`, `tag NAME`, `rollback C` | commit history and restore |
| `diff` | rootfs changes since the image |
| `remote add NAME URL` | register an `ssh://` remote |
| `push`, `pull`, `fetch` | transfer commits to/from a remote |
| `doctor` | diagnose the environment |

Global flags: `--json` (machine-readable output on query commands) and
`--debug`.

## Project spec

```yaml
schemaVersion: 1
name: demo
runtime:
  engine: podman
  backend: auto          # native-podman on Linux, podman-machine on macOS
  rootless: true
container:
  image: node:24
  workdir: /workspace
  user: dev
  command: ["/opt/worksync/bin/worksync-agent", "idle"]
  environment:
    NODE_ENV: development
ports:
  - name: web
    target: 3000
    published: auto       # allocated and recorded; reused across restarts
    listen: 127.0.0.1
    protocol: tcp
volumes:
  workspace:
    source: { type: host, path: . }
    target: /workspace
    policy: tracked
  home:
    target: /home/dev
    policy: persistent
commit:
  environment: true
  volumes: [workspace, home]
snapshot:
  mode: stop             # stop | command | none
  pre:  ["pg_dump > /backup/db.sql"]
  post: []
remote:
  default: origin
```

## Storage layout

- `$XDG_DATA_HOME/worksync/` (or `~/Library/Application Support/worksync`)
  - `state.db` — SQLite (WAL) state machine
  - `projects/<id>/` — per-project derived state (remotes)
  - `commits/<hex>.json` — immutable descriptors (content-addressed)
  - `oci/<hex>/` — environment OCI archive + verified blobs
  - `restic/` — encrypted local volume repository
  - `locks/`, `staging/` — lock files and atomic-write scratch
  - `data/<project>/` — managed volume data (workspaces/volumes/...)

Environment overrides: `WORKSYNC_DATA_DIR`, `WORKSYNC_CONFIG_DIR`.

## Security

- Restic repositories are always encrypted; the password lives in
  `WORKSYNC_RESTIC_PASSWORD` or a 0600 keyring file.
- No SSH keys are ever stored: remotes use your agent.
- Every mutating command is journaled; stale entries are recovered when the
  project lock is re-acquired.
- Commands run through `podman`, `restic`, `ssh`/`sftp` only; no daemon is
  installed.

## Development

- Design document: [`docs/worksync-v0-design.md`](docs/worksync-v0-design.md)
- `make test`, `make vet`, `make fmt` — see [CONTRIBUTING.md](CONTRIBUTING.md)

## Status

v0 milestone: M0 spec freeze, M1 CLI + state, M2 lifecycle, M4 local commits,
M5 remote push/pull/fetch are implemented and covered by unit and integration
(fake-binary) tests. M6 hardening is in progress.

## License

[MIT](LICENSE)