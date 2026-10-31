#!/usr/bin/env bash
# Fails when go.mod's module path does not fit the release version.
#
# Reads: RELEASE_VERSION (e.g. 2.0.0 or 1.2.3-rc.1); GO_MOD (tests).
# The /vN guard: Go resolves a major version >= 2 only through a module path
# ending in /vN (https://go.dev/ref/mod#major-version-suffixes). A v2.0.0
# tag on a module still named github.com/acme/lib is not importable as v2;
# `go get github.com/acme/lib@v2.0.0` fails with "should be v0 or v1, not
# v2", and the tag cannot be withdrawn from the proxy afterwards. Conversely
# v0 and v1 must not carry a suffix.
set -euo pipefail

cd "$(dirname "$0")/.."
: "${RELEASE_VERSION:?RELEASE_VERSION is not set}"

path="$(scripts/module-info.sh path)"
suffix="$(scripts/module-info.sh major)"
tag_major="${RELEASE_VERSION%%.*}"
if ! [[ "$tag_major" =~ ^[0-9]+$ ]]; then
    echo "error: cannot read the major version from RELEASE_VERSION=$RELEASE_VERSION" >&2
    exit 1
fi

if ((tag_major >= 2)); then
    if [[ "$suffix" != "$tag_major" ]]; then
        echo "error: releasing v$RELEASE_VERSION needs the module path to end in /v$tag_major, but go.mod says '$path'." >&2
        echo "       Change 'module' in go.mod to '$(sed -E 's#/v[0-9]+$##' <<<"$path")/v$tag_major' and update the module's own import paths, then release." >&2
        exit 1
    fi
elif [[ -n "$suffix" ]]; then
    echo "error: releasing v$RELEASE_VERSION (major $tag_major) needs a module path without a /vN suffix, but go.mod says '$path'." >&2
    exit 1
fi
echo "OK: module path '$path' matches major version $tag_major"
