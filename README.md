# lib-operator-go

> [!NOTE]
> This repository is vibe coded with [Claude](https://claude.com/product/claude-code).

Shared Go library for the craftainer-labs Kubernetes operators
(`operator-db`, `operator-s3`, `operator-mqtt`, `operator-cache`,
`operator-oidc`): the reconcile framework, tenancy naming and the
service-binding Secret writer. Module path
`github.com/craftainer-labs/lib-operator-go`. It is an instance of
[`template-go-module`](https://github.com/craftainer/template-go-module); see
[`docs/TEMPLATE.md`](docs/TEMPLATE.md) for the conventions shared by every
instance.

**Status: skeleton.** The package layout and the interface declarations exist;
the implementation follows once the API shapes are accepted. The conventions
every operator follows are proposed in
[`docs/adrs/0002-operator-crd-conventions.md`](docs/adrs/0002-operator-crd-conventions.md)
and the draft CRDs of all five groups are in
[`docs/crd-drafts.md`](docs/crd-drafts.md).

| Package | Purpose |
| --- | --- |
| `pkg/lifecycle` | `External`, `Rotator` and `Claim` interfaces and `Reconcile` |
| `pkg/server` | `Resolve`: server lookup that enforces `allowedNamespaces` |
| `pkg/binding` | `WriteSecret`: `servicebinding.io/<type>` Secrets |
| `pkg/naming` | `Tenant`: deterministic, collision-safe external names |
| `pkg/conditions` | `Ready`, `Synced`, `ServerReachable` |

## Checks and the coverage gate

`prek run --all-files --hook-stage manual` runs formatting, `golangci-lint
run` (configured in `.golangci.yml`), `go mod tidy -diff`, `govulncheck ./...`
and `scripts/go-coverage.sh`, which runs every test under the race detector
and fails below 100% statement coverage across all packages (`-coverpkg=./...`,
so an untested package counts as 0%). Fix a lint finding at its cause; a
`//nolint` needs the linter's name and a reason (`nolintlint` enforces both).

## Releasing

`release.yml` (Actions, "Release") computes the next version, runs
`make release-check`, and creates the tag and the GitHub release. A Go module
is published by its tag. Right after each release, check that the tag resolves
on `proxy.golang.org`:

```bash
gh workflow run verify-published.yml -f tag=vX.Y.Z
```

A major version of 2 or more needs a `/vN` module-path suffix before the tag
exists (`make release-check` fails otherwise). A published version can't be
withdrawn: fix it, add a `retract` directive to `go.mod` with the reason, and
release a new patch version. Never move a published tag. The full runbook is
in [`docs/TEMPLATE.md`](docs/TEMPLATE.md).

## License

Licensed under the [Apache License 2.0](LICENSE).
