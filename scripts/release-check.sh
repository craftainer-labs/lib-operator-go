#!/usr/bin/env bash
# `make release-check`: gates the GitHub release (the tag is the Go module's
# release, so there is nothing to upload afterwards). Checks only the local
# module; the check of the published release is release-verify-published.sh.
#
# Reads: RELEASE_VERSION (e.g. 2.0.0 or 1.2.3-rc.1).
#
# The /vN module-path guard lives in check-module-path.sh.
set -euo pipefail

cd "$(dirname "$0")/.."
scripts/check-module-path.sh

go build ./...
go mod verify
# go.mod / go.sum must be tidy: a release with a stale go.sum breaks consumers.
go mod tidy -diff
echo "OK: $(scripts/module-info.sh path) builds, verifies and is tidy"
