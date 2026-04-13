# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

## [0.5.0] - 2026-04-13

### Added

- Complete `brain serve` Run API: `POST/GET/DELETE /v1/runs`, cancellation, status query, and HTTP smoke coverage.
- Built-in specialist sidecars for `central`, `code`, `verifier`, `fault`, and `browser`, including orchestrator retry and health-check coverage.
- `tool_profiles` / `active_tools` with scope-aware filtering for `chat`, `run`, delegated sidecars, and `brain tool list/describe/test --scope`.
- Diff preview, interactive sandbox approval, and ToolObserver output propagation in the chat workflow.
- GitHub Releases packaging workflow with multi-platform archives, checksums, release notes extraction, and artifact attestations.
- Release helper scripts under `scripts/release/` for local dry-runs and reproducible packaging.
- Custom source-available licensing: free for personal use, separate paid license required for organizational and commercial use.

### Changed

- `code.search` now prefers `rg`/ripgrep for fast text and regex search, while keeping the pure Go walker as fallback.
- Windows terminal input now uses a real console raw backend instead of line-mode fallback.
- Official release packages place `brain` and all built-in sidecars in the same directory so delegated execution works out of the box.

### Fixed

- `brain serve` cancellation now remains `cancelled` instead of reverting to `failed` after background execution exits.
- `loop.Runner` now maps provider-call `context canceled` to `StateCanceled`.
- Cross-platform build issues on Darwin, FreeBSD, and Windows caused by raw terminal and signal handling differences.
- Windows sidecar discovery now checks same-directory `.exe` binaries, matching the packaged release layout.

[unreleased]: https://github.com/leef-l/brain/compare/v0.5.0...HEAD
[0.5.0]: https://github.com/leef-l/brain/releases/tag/v0.5.0
