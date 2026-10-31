# scripts/

- `develop.sh` — `../.devcontainer/Dockerfile`'s `develop`-stage setup: installs `prek`,
  the Claude Code CLI, `snip` and the GitHub CLI (`gh`), the only tooling this template requires
  regardless of instance language. An instance adds its own language
  runtime/tooling install on top, either by extending this script or
  copying its shape into a second script. Prefer the hook below over
  either. Every download goes through its `fetch` helper (bounded curl
  retries with backoff; GitHub URLs authenticated when the optional
  `github_token` BuildKit secret is present) and the installers
  that download for themselves run through `retry` (the installer script
  itself is fetched once, by `fetch`, so the two layers of retries do not
  multiply; a failed download names the installer and URL before the script
  aborts, an empty one is rejected, and `fetch` itself retries DNS and
  connect failures (curl exit 6/7) and truncated transfers (18/52/55/56) up
  to 4 times; timeouts are left to curl's own `--retry`), so CI's shared-IP
  HTTP 429s don't fail the image build. Local builds need no token.
- `post-create.sh` — the devcontainer's `postCreateCommand`: git
  `safe.directory`, `prek install`, then every `post-create.d/*.sh`.
- `module-info.sh` — reads the module path from `go.mod` (the one place
  that does) and prints `path`, `name`, `major` (the `/vN` suffix's N) or
  `proxy-path` (the path in proxy.golang.org's case encoding); the
  Makefile and the release scripts call it.
- `go-coverage.sh` — `go test -race` over every package with
  `-coverpkg=./...`, failing under `COVERAGE_MIN` percent statement coverage
  (default 100); the `go-test-coverage` hook runs it.
- `check-module-path.sh`, `release-check.sh`,
  `release-verify-published.sh` — the `Makefile`'s release contract
  (`docs/TEMPLATE.md`, "Go module template notes"): `check-module-path.sh` is
  the `/vN` guard (a major >= 2 needs a `/vN` module path, v0/v1 none),
  `release-check.sh` adds build, `go mod verify` and a tidy check before the
  release, and `release-verify-published.sh` polls proxy.golang.org for the
  new tag after it. `test_release_contract.py` covers them and the
  Makefile's no-op targets without Go, against a local stand-in for the
  proxy.
- `develop.d/10-go.sh`, `post-create.d/10-go-download.sh` — the pinned,
  sha256-verified Go toolchain, golangci-lint and govulncheck install, and
  the `go mod download` after create. `develop.d/` and `post-create.d/` are
  the instance hook directories; see `docs/TEMPLATE.md`'s "Instance extension
  points".

## Do

- Pass a token to the build as a BuildKit secret (`github_token`), never
  a `--build-arg`/`ARG`: an ARG is baked into the image history.
- Keep this script's own tools pinned to an exact version (a `# renovate:`
  ARG in `../.devcontainer/Dockerfile`) and checksum-verified where the upstream project
  publishes checksums, matching the pattern each install step here already
  follows.

## Don't

- Add a language runtime or stack-specific tool here — that belongs in the
  instance's own layer (`develop.d/`), not the base template's.
