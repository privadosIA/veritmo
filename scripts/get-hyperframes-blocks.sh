#!/usr/bin/env bash
# scripts/get-hyperframes-blocks.sh
#
# Brings in ALL the HyperFrames blocks (210 at the pinned commit) and components (238) and everything they need to be drawn: the libraries they load from a
# CDN (scripts/get-hyperframes-libs.sh, run first) and the blocks themselves, which are cloned from the HyperFrames repository at a pinned
# commit into ~/.cache/veritmo/hyperframes and imported whole into the catalog (hyperframes/blocks/, or $HYPERFRAMES_DIR) with
# bin/veritmo. Optional: without it no block is available and everything else works as always.
#
#   bash scripts/get-hyperframes-blocks.sh              # libraries + blocks, no checks
#   HF_CHECK=1 bash scripts/get-hyperframes-blocks.sh   # also run the minimum checks on each block (needs Chrome; a few minutes)
#
# The checks can be run later: bin/veritmo blocks check. Safe to re-run: blocks the user edited are kept, the clone is reused.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO="https://github.com/heygen-com/hyperframes"
COMMIT="dbc9c18262fd9c773491990d04f2aef6b32cea1d"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/veritmo/hyperframes"
ORCH="${ROOT}/bin/veritmo"

[[ -x "${ORCH}" ]] || { echo "[get-hyperframes-blocks] ✗  ${ORCH} is not built: run scripts/install.sh --only veritmo first" >&2; exit 1; }
command -v git >/dev/null || { echo "[get-hyperframes-blocks] ✗  git is needed" >&2; exit 1; }

bash "${SCRIPT_DIR}/get-hyperframes-libs.sh"

if [[ -d "${CACHE}/.git" ]] && [[ "$(git -C "${CACHE}" rev-parse HEAD 2>/dev/null)" == "${COMMIT}" ]]; then
    echo "[get-hyperframes-blocks] ✓  the clone is already at ${COMMIT:0:7}: ${CACHE}"
else
    rm -rf "${CACHE}"
    mkdir -p "${CACHE}"
    echo "[get-hyperframes-blocks] Fetching HyperFrames ${COMMIT:0:7} ..."
    git -C "${CACHE}" init -q
    git -C "${CACHE}" remote add origin "${REPO}"
    git -C "${CACHE}" fetch -q --depth 1 origin "${COMMIT}" || { echo "[get-hyperframes-blocks] ✗  cannot fetch ${REPO} at ${COMMIT}" >&2; exit 1; }
    git -C "${CACHE}" checkout -q FETCH_HEAD
fi

args=(blocks import-hyperframes --version "${COMMIT:0:7}")
[[ -n "${HF_CHECK:-}" ]] || args+=(--no-check)
"${ORCH}" "${args[@]}" --components "${CACHE}/registry/components" "${CACHE}/registry/blocks"
echo "[get-hyperframes-blocks] ✓  blocks and components imported (bin/veritmo blocks list)"
[[ -d "${HYPERFRAMES_DIR:-${ROOT}/hyperframes}/placeholders" ]] || echo "[get-hyperframes-blocks]    the carousels got procedural pictures; for generated ones: bash scripts/make-block-placeholders.sh (needs bin/sd), then bin/veritmo blocks placeholders"
[[ -s "${ROOT}/third_party/hyperframes-runtime/hyperframe.runtime.iife.js" ]] || echo "[get-hyperframes-blocks]    the components that mount sub-compositions need the runtime: bash scripts/get-hyperframes-runtime.sh (needs Node)"
