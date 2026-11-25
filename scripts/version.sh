#!/usr/bin/env bash
# scripts/version.sh
#
# Prints the version a build is stamped with: the release tag the checkout is on or after (`v0.1.0`, or `v0.1.0-3-gabc1234`
# three commits later), and `dev` when there is no release tag or no git (a source archive). The Go builds pass it with
#
#   go build -ldflags "-X veritmo/internal/platform/edition.Version=$(bash scripts/version.sh)" ./cmd/veritmo
#
# VERITMO_VERSION in the environment wins, for builds that know it (CI, a packager).
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
if [[ -n "${VERITMO_VERSION:-}" ]]; then echo "${VERITMO_VERSION}"; exit 0; fi
git -C "$ROOT" describe --tags --match 'v[0-9]*' 2>/dev/null || echo dev
