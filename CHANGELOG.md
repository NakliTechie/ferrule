# Changelog

All notable changes to Ferrule are recorded here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/); versions follow
[Semantic Versioning](https://semver.org/). The version is the git tag; there is no
manifest to bump.

## [1.2.0] — 2026-09-24

### Added
- `/v1` answers a browser page: the origin is reflected, the preflight is answered
  before the token guard (a preflight carries no token), and a public page's
  private-network request to loopback is allowed. The token is still the gate — a
  page without one gets 401. The control routes are unchanged: same machine, same
  origin. First consumer: NakliOS's Ferrule provider preset.

### Fixed
- CI: `make` runs under bash. The test target's `set -o pipefail` failed under
  Ubuntu's dash, so `ci.yml` was red on every push since 2026-09-21; releases (built on
  macOS) were unaffected.

## [1.1.0] — 2026-09-21

### Added
- A `recent_errors` op and a **What went wrong** section on the Usage screen. The
  ledger had recorded `entry.Err` for every failed request since it existed and
  shown it to nobody; the only readable copy was whatever the calling app logged.
  The provider's full words are now on the panel.
- `internal/netutil` holds the loopback check once. The control-surface guard and
  the error-trim decision both turn on it; two copies were two chances to drift.

### Changed
- An upstream's error body is trimmed for callers that are not on this machine. On
  loopback the provider's own text goes through untouched. Off the machine the
  caller gets the shape of the failure and where the detail lives; a chat app on
  the wifi has no reason to receive another account's error text. Asserted on a
  real network bind: loopback sees the account identifier, the network does not,
  and the ledger still has it.
- The first-byte bound stays at 2 minutes, now measured rather than guessed.
  Against a real NVIDIA account, served requests reach first byte in 1.2–10.4 s; a
  2000-token non-streaming generation returns in 14.3 s; a request that never
  serves takes 302 s and returns the provider's own 504. The numbers are in
  `internal/router/router.go` so nobody re-guesses.

## [1.0.1] — 2026-09-04

### Fixed
- `make demo` was broken in the release and nothing was watching it. A vault change
  that refused short secrets broke every one of the demo's fake keys. `make check`
  now boots the demo and refuses a release where it does not come up.

### Changed
- Launch assets reshot against the shipped build.

## [1.0.0] — 2026-09-04

First release. One binary; macOS (Apple Silicon app + Intel binary), Linux, Windows.

[1.2.0]: https://github.com/NakliTechie/ferrule/compare/v1.1.0...v1.2.0
[1.1.0]: https://github.com/NakliTechie/ferrule/compare/v1.0.1...v1.1.0
[1.0.1]: https://github.com/NakliTechie/ferrule/compare/v1.0.0...v1.0.1
[1.0.0]: https://github.com/NakliTechie/ferrule/releases/tag/v1.0.0
