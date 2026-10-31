#!/usr/bin/env bash
# Reads the module path from go.mod (the one place that does, shared by the
# Makefile and the release scripts) and prints one derived value:
#   path        the module path, e.g. github.com/acme/lib/v2
#   name        the repository-style name, without a /vN suffix: lib
#   major       the /vN suffix's N, or empty when there is none
#   proxy-path  the path as proxy.golang.org spells it: every uppercase
#               letter becomes "!" plus its lowercase (module case encoding)
# Run from anywhere; GO_MOD overrides the go.mod location (tests).
set -euo pipefail

gomod="${GO_MOD:-$(dirname "$0")/../go.mod}"
# `module <path>` or `module "<path>"` (go.mod allows a quoted path).
path="$(awk '$1 == "module" { gsub(/"/, "", $2); print $2; exit }' "$gomod")"
if [[ -z "$path" ]]; then
    echo "no module directive in $gomod" >&2
    exit 1
fi

case "${1:-}" in
    path) echo "$path" ;;
    name)
        base="$path"
        if [[ "$path" =~ ^(.*)/v[0-9]+$ ]]; then base="${BASH_REMATCH[1]}"; fi
        echo "${base##*/}"
        ;;
    major)
        if [[ "$path" =~ /v([0-9]+)$ ]]; then echo "${BASH_REMATCH[1]}"; fi
        ;;
    proxy-path) echo "$path" | LC_ALL=C awk '{ out = ""; for (i = 1; i <= length($0); i++) { c = substr($0, i, 1); out = out (c ~ /[A-Z]/ ? "!" tolower(c) : c) } print out }' ;;
    *)
        echo "usage: $0 path|name|major|proxy-path" >&2
        exit 2
        ;;
esac
