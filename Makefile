# template-go-module's implementation of template-base's release contract
# (docs/TEMPLATE.md, "Release: a Makefile contract"). release.yml sets
# RELEASE_VERSION / RELEASE_TAG / RELEASE_ARCH. A Go module is source, built
# once on one arch. The git tag IS the release: there is nothing to upload,
# so publish and publish-packages are no-ops, and verify-published asks
# proxy.golang.org whether the tag resolves.

.PHONY: release-arches build sbom release-assets publish publish-packages verify-published release-check finalize

# Module name (go.mod's `module` path without /vN) and the release version.
# Looked up lazily, once, on first use, so `release-arches` never reads go.mod.
MODULE_INFO := scripts/module-info.sh
NAME = $(eval NAME := $$(shell $(MODULE_INFO) name))$(NAME)
ASSET = dist/$(NAME)-$(RELEASE_VERSION)
REQUIRE_VERSION = $(if $(RELEASE_VERSION),,$(error RELEASE_VERSION is not set))

# renovate: datasource=docker depName=anchore/syft
SYFT_VERSION := v1.52.0

release-arches:
	@echo '["amd64"]'

# Compiles every package (a compile error fails the release before anything is
# tagged public) and archives the source at the commit being released. The
# runner's Go switches to go.mod's `toolchain` version by itself (GOTOOLCHAIN=auto).
build:
	$(REQUIRE_VERSION)
	go build ./...
	rm -rf dist
	mkdir -p dist
	git archive --format=tar.gz --prefix=$(NAME)-$(RELEASE_VERSION)/ -o $(ASSET)-source.tar.gz HEAD

# Syft over the archived source: go.mod and go.sum list the dependencies the
# tag resolves to.
sbom:
	$(REQUIRE_VERSION)
	rm -rf dist/.sbom-src
	mkdir -p dist/.sbom-src
	tar -xzf $(ASSET)-source.tar.gz -C dist/.sbom-src --strip-components=1
	docker run --rm -u "$$(id -u):$$(id -g)" --tmpfs /tmp:rw,mode=1777 -e HOME=/tmp -v "$(CURDIR)/dist:/dist" \
		anchore/syft:$(SYFT_VERSION) "dir:/dist/.sbom-src" \
		--source-name $(NAME) --source-version $(RELEASE_VERSION) -o "spdx-json=/$(ASSET).spdx.json"
	rm -rf dist/.sbom-src

release-assets:
	$(REQUIRE_VERSION)
	@set -e; for f in '$(ASSET)-source.tar.gz' '$(ASSET).spdx.json'; do \
		test -f "$$f" || { echo "missing release asset: $$f" >&2; exit 1; }; \
	done; ls dist

# Per arch, image registries only: this template ships no image.
publish:
	@echo "Go module -- no image to publish (the git tag is the release)."

# A Go module is published by its git tag, which release.yml creates with the
# GitHub release; the proxy fetches it on first request. Nothing to upload,
# and no *_PUBLISH variable applies.
publish-packages:
	@echo "Go module -- the git tag is the release, nothing to upload."

# Dispatched by hand after each release (.github/workflows/verify-published.yml,
# a stopgap until template-base can opt this contract target in), or locally:
#   make verify-published RELEASE_TAG=v1.2.3
# Polls proxy.golang.org with backoff. See scripts/release-verify-published.sh.
verify-published:
	scripts/release-verify-published.sh

# Runs once after the arch leg, inside the devcontainer, and gates the GitHub
# release: the /vN module-path guard, build, go mod verify and tidy. See
# scripts/release-check.sh.
release-check:
	$(REQUIRE_VERSION)
	scripts/release-check.sh

# Nothing to combine: a Go module is built once.
finalize:
	@echo "Go module -- nothing to finalize."
