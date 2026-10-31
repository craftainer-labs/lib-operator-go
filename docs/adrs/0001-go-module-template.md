# 0001. Provide a Go module template whose release is a git tag

## Status

Accepted

## Context

The Kubernetes operators planned for the craftainer-labs organization share
one Go library, `lib-operator-go`, and the operators themselves are Go. The
template family has no Go toolchain, and every family library is an instance
of its language's package template, so the library would otherwise copy the
devcontainer, CI and release plumbing by hand and let the copies drift.

`template-base` is the upstream for language-agnostic scaffolding and
template sync, but ships no toolchain and a no-op release Makefile. Go
differs from the PyPI, npm and crates.io templates in one way that shapes the
release: there is no upload. A module is published by pushing a tag; the
module proxy fetches it on first request, and a published version can never be
withdrawn from the proxy or the checksum database.

`template-base`'s `release.yml` is base-owned (a sync overwrites it).
Its `verify-published` job runs only when a `PYPI_PUBLISH`, `NPM_PUBLISH` or
`CARGO_PUBLISH` variable is set, and none of them fits Go. Creating the
release with `GITHUB_TOKEN` also starts no `release:` workflows.

## Decision

We will create `craftainer/template-go-module`, an instance of
`template-base` that is itself a template, and create every Go library from
it. It contains:

- a module (`go.mod`, a sample package `greet/` with a test), a pinned Go
  toolchain installed by `scripts/develop.d/10-go.sh` (tarball checked against
  the SHA-256 in go.dev's download index) next to `golangci-lint` v2 and
  `govulncheck`, all run by `prek`; `go.mod`'s `go` line is the minimum
  supported release and its `toolchain` line equals the devcontainer's
  `GO_VERSION`, one Renovate group;
- `go test -race` over all packages with a 100% statement-coverage gate
  (`scripts/go-coverage.sh`, `COVERAGE_MIN`) and Renovate's `gomod` manager;
- a `Makefile` implementing `template-base`'s release contract with the tag
  as the release: `build` compiles and archives the source, `sbom` runs Syft,
  `publish` and `publish-packages` are no-ops, `release-check` runs a `/vN`
  module-path guard (a major of 2 or more needs the `/vN` suffix, v0 and v1
  none), `go mod verify` and a tidy check, and `verify-published` polls
  `proxy.golang.org` with backoff for the new tag;
- `.github/workflows/verify-published.yml`, a `workflow_dispatch` workflow
  (input `tag`) that runs `make verify-published`, dispatched by hand after
  each release, as a stopgap until `template-base` can opt a Go module in. It
  is not automatic: `workflow_run` is a zizmor finding that this repository
  does not suppress. It is listed in this template's sync manifest, so a later
  tombstone removes it from every module;
- a documented `retract` runbook, because a bad release can only be
  superseded;
- the GPL-3.0-only license, matching the other templates.

## Consequences

- A new module starts with the full quality bar and release path, and receives
  fixes through two sync hops (`template-base`, then this template). The
  `Makefile` and `scripts/` are template-owned, so a module never edits them.
- A mistake in a release (a missing `/vN`, a broken build) is expensive
  because a tag can't be withdrawn; the guard and `release-check` run before
  the tag exists for that reason. The cost is that a major release needs a
  preparatory commit.
- `verify-published` runs outside the contract's `verify-published` job, in a
  second workflow dispatched by hand, and only for public repositories. Until
  `template-base` gains an opt-in, forgetting it is silent and a failure there
  does not mark the `Release` run as failed.
- The tools are pinned in a hook script, not in Dockerfile `ARG`s, because the
  base `Dockerfile` is replace tier. If devcontainer features for Go land
  (sha256-verified), this template can switch to them.
- Libraries only: a module with a `main` package that ships binaries, or with
  a Kubernetes API, needs a different template on top of this one.
