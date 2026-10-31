# template-go-module

> [!NOTE]
> This repository is vibe coded with [Claude](https://claude.com/product/claude-code).

A template for a tested, linted and publishable Go library module: one
sample package, `gofmt`/`goimports`, `golangci-lint` v2, `govulncheck`,
`go test -race` behind a 100% statement-coverage gate, Renovate's `gomod`
manager, and a release that is just a git tag, checked afterwards against
`proxy.golang.org`. It is itself an instance of
[`template-base`](https://github.com/craftainer/template-base) (so it gets the
devcontainer, CI, release workflow and template sync from there) and is
itself a template: a module created from it receives fixes through template
sync. See [`docs/TEMPLATE.md`](docs/TEMPLATE.md) for the conventions shared
by every instance, and
[`docs/adrs/0001-go-module-template.md`](docs/adrs/0001-go-module-template.md)
for why it exists.

## Creating a module from this template

1. On GitHub, use "Use this template" on this repository (or
   `gh repo create <org>/<name> --template craftainer/template-go-module`).
2. Rename the module. The module path is the repository URL,
   `github.com/<owner>/<repo>` (for example
   `github.com/craftainer-labs/lib-operator-go`), and the repository must be
   public for the module to resolve on `proxy.golang.org`. These places move
   together:
   - `go.mod`: the `module` line;
   - `greet/greet_test.go`: the import path (and replace the sample package
     with the module's real ones);
   - this file's title and description.

   `go.mod`'s `go` line is the oldest Go release your consumers may use;
   leave it at the template's value until the code needs more. Its
   `toolchain` line is what this repository builds with and must equal
   `GO_VERSION` in `scripts/develop.d/10-go.sh`.
3. Point template sync at this template. `.github/template-sync-state.json`
   here records the `template-base` tag this template itself last synced
   from; the copy in a new module must instead name this repository and the
   tag it was created at (the latest release of
   `craftainer/template-go-module`):

   ```json
   {"template_repo": "craftainer/template-go-module", "synced_tag": "vX.Y.Z"}
   ```

   Add a `TEMPLATE_SYNC_TOKEN` secret too (see `.github/workflows/README.md`,
   "Template sync"). Files the module owns beyond the manifest's `ignore`
   tier (directories without a `.go` file, such as `config/`) go in
   `.github/template-sync-manifest.local.yml` under `ignore:`.
4. Open the repository in the devcontainer, then run
   `prek run --all-files --hook-stage manual`; everything should pass before
   you write any code.
5. Keep `LICENSE` (GPL-3.0) or change it deliberately.

## Checks and the coverage gate

`prek run --all-files --hook-stage manual` runs formatting, `golangci-lint
run` (configured in `.golangci.yml`), `go mod tidy -diff`, `govulncheck ./...`
and `scripts/go-coverage.sh`, which runs every test under the race detector
and fails below 100% statement coverage across all packages (`-coverpkg=./...`,
so an untested package counts as 0%). `COVERAGE_MIN=90 scripts/go-coverage.sh`
lowers it for one run; a module that needs a different gate sets the variable
in its own hook entry, with the reason beside it. Fix a lint finding at its
cause; a `//nolint` needs the linter's name and a reason (`nolintlint`
enforces both).

## Releasing

`release.yml` (Actions, "Release") computes the next version, runs
`make release-check`, and creates the tag and the GitHub release (with a source
archive and an SBOM). Nothing is uploaded: a Go module is published by its
tag. Right after each release, run the check that the tag resolves on
`proxy.golang.org` (a manual dispatch, until template-base can run it inside
the release):

```bash
gh workflow run verify-published.yml -f tag=vX.Y.Z
```

**The `/vN` guard.** For a major version of 2 or more, Go requires the module
path to end in `/vN`, so `make release-check` fails a `v2.0.0` release unless
`go.mod` says `module github.com/<owner>/<repo>/v2` (and a `v0` or `v1`
release whose path has a `/vN` suffix). Make that change, and update the
module's own import paths, in a commit before the release. The first `v2`
tag is not recoverable by re-tagging, which is why this fails before the tag
exists.

**Retract runbook.** A published version can't be withdrawn from the proxy or
the checksum database, and deleting the tag or the release doesn't remove it.
When a release is broken:

1. Fix the problem on `main`, or revert it.
2. Add a `retract` directive to `go.mod`, with the reason in the comment:

   ```go
   retract v1.2.3 // panics on empty input; use v1.2.4 or later
   ```

   (A range works too: `retract [v1.2.0, v1.2.3]`.)
3. Release a new patch version (`full`, bump `patch`). `go get` and
   `go list -m -u` now warn about the retracted version and skip it when
   choosing the latest; it stays downloadable by exact version.
4. Never move or re-push a published tag to another commit: the checksum
   database remembers the first one, and consumers then get a security error.

If `verify-published.yml` fails, the release is still out: check the module
path and that the repository is public, then dispatch it again with the same
tag.

## License

Licensed under the [GNU General Public License v3.0](LICENSE).
