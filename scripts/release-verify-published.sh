#!/usr/bin/env bash
# `make verify-published`: confirms the module version is resolvable through
# the public module proxy. Publishing a Go module is just pushing the tag
# (the proxy fetches it on first request), so this asks the proxy for the
# version's metadata, which also makes it fetch and cache the tag:
#   GET $PROXY_URL/<module>/@v/<tag>.info  ->  {"Version":"<tag>","Time":...}
# and polls with exponential backoff while the answer is 404/410 (not
# fetched yet), 429 or 5xx. Runs on a bare runner with no secrets.
#
# Reads: RELEASE_TAG (e.g. v1.2.3).
# Tuning (tests, slow proxies): PROXY_URL (default https://proxy.golang.org),
# VERIFY_ATTEMPTS (default 8), VERIFY_SLEEP (first wait in seconds, default
# 5; doubles each attempt, capped at 60; the default window is about 4.5
# minutes).
#
# A private repository is not on the public proxy; the workflow skips it
# (see .github/workflows/verify-published.yml), and a private module
# consumer sets GOPRIVATE instead.
set -euo pipefail

cd "$(dirname "$0")/.."
: "${RELEASE_TAG:?RELEASE_TAG is not set}"
proxy="${PROXY_URL:-https://proxy.golang.org}"
attempts="${VERIFY_ATTEMPTS:-8}"
wait_s="${VERIFY_SLEEP:-5}"

mod="$(scripts/module-info.sh proxy-path)"
# The proxy case-encodes the version too (v1.0.0-RC1 -> v1.0.0-!r!c1).
enc_tag="$(printf '%s' "$RELEASE_TAG" | LC_ALL=C awk '{ out = ""; for (i = 1; i <= length($0); i++) { c = substr($0, i, 1); out = out (c ~ /[A-Z]/ ? "!" tolower(c) : c) } print out }')"
url="${proxy}/${mod}/@v/${enc_tag}.info"
body="$(mktemp)"
trap 'rm -f "$body"' EXIT

for ((i = 1; i <= attempts; i++)); do
    code="$(curl -sS -o "$body" -w '%{http_code}' --max-time 30 "$url" || true)"
    code="${code:-000}"
    if [[ "$code" == 200 ]]; then
        if grep -qF "\"Version\":\"${RELEASE_TAG}\"" "$body" || grep -qF "\"Version\": \"${RELEASE_TAG}\"" "$body"; then
            echo "OK: $url lists $RELEASE_TAG"
            exit 0
        fi
        echo "error: $url answered 200 without Version $RELEASE_TAG:" >&2
        cat "$body" >&2
        exit 1
    fi
    echo "attempt $i/$attempts: HTTP $code from $url"
    case "$code" in
        000* | 404 | 410 | 429 | 5??) ;;
        *)
            echo "error: HTTP $code is not retryable" >&2
            exit 1
            ;;
    esac
    ((i == attempts)) && break
    sleep "$wait_s"
    wait_s=$((wait_s * 2 > 60 ? 60 : wait_s * 2))
done
echo "error: $RELEASE_TAG of $(scripts/module-info.sh path) did not appear on $proxy after $attempts attempts" >&2
exit 1
