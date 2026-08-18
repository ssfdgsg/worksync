# Changelog

All notable changes to this project will be documented in this file.
The format is based on [Keep a Changelog](https://keepachangelog.com/),
and this project adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- v0 milestone surface: M0 spec freeze, M1 CLI + state DB, M2 lifecycle
  (rootless Podman runtime, macOS Podman Machine backend), M4 local commits
  (environment OCI archive + restic volume snapshots, content-addressed
  descriptors), M5 remote push/pull/fetch over SSH/SFTP.
- Commands: `init`, `up`, `status`, `shell`, `exec`, `stop`, `start`, `rm`,
  `ports`, `expose`, `unexpose`, `commit`, `log`, `tag`, `rollback`, `diff`,
  `remote`, `push`, `pull`, `fetch`, `doctor`.
- Renamed from `workbox` to `worksync`.
