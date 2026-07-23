# Changelog

All notable changes to this project will be documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Breaking Changes

- Replaced `--health` and `--health-timeout` with repeatable `--healthcheck` and `--healthcheck-timeout` options
  ([#25]).

### Added

- Added a `retrieve` command for extracting snapshots from local or remote repositories, with support for excluding
  individual archives ([#26]).
- Added repeatable HTTP and Ethereum JSON-RPC sync health checks for backups ([#25]).
- Added `--freeze-container-ids` to stop and restart multiple containers during a backup ([#23]).
- Added cosign signatures and verification instructions for release checksums and container images ([#35]).
- Added reproducible build metadata and published container image digests ([#35]).

### Changed

- Backups now preserve symlinks and skip unsupported file types ([#32]).
- Container images now use Wolfi as the base image ([#35]).
- Updated to Go 1.26.5 (security); source builds now require Go 1.26+ ([#36]).

### Removed

- Removed AWS CLI from container images ([#35]).

---

_Looking for the changelog for an older version? Check <https://github.com/hemilabs/dave/releases>_

[Unreleased]: https://github.com/hemilabs/dave/compare/v0.1.3...HEAD

[#23]: https://github.com/hemilabs/dave/pull/23
[#25]: https://github.com/hemilabs/dave/pull/25
[#26]: https://github.com/hemilabs/dave/pull/26
[#32]: https://github.com/hemilabs/dave/pull/32
[#35]: https://github.com/hemilabs/dave/pull/35
[#36]: https://github.com/hemilabs/dave/pull/36
