#!/usr/bin/env bash
# scripts/get-hyperframes-runtime.sh
#
# Builds, once, the runtime of HyperFrames (Apache-2.0) from its source at a pinned commit, so that blocks and components that mount
# sub-compositions are drawn by the real thing. The output, one script, goes to third_party/hyperframes-runtime/ (not committed) with
# its SHA-256. Optional: without it blocks are drawn with the server's own small runtime (most of them work); with it the components of
# the registry can be drawn too. Needs Node (20 or newer), npm, git and the network, only for this step; the server does not need them.
#
#   bash scripts/get-hyperframes-runtime.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
REPO="https://github.com/heygen-com/hyperframes"
COMMIT="dbc9c18262fd9c773491990d04f2aef6b32cea1d"
CACHE="${XDG_CACHE_HOME:-$HOME/.cache}/veritmo/hyperframes"
DEST_DIR="${ROOT}/third_party/hyperframes-runtime"
DEST="${DEST_DIR}/hyperframe.runtime.iife.js"
# What the build was made and checked with: the dependencies of packages/core at that commit and esbuild.
ESBUILD="0.25.0"; POSTCSS="8.5.33"; PRETEXT="0.0.5"; ACORN="8.19.0"; ACORN_WALK="8.3.5"
EXPECTED_SHA256="642d2bca903bbc4cc7949d3bd209f42347ae76eacc5c3ce3c151d8c67ced39f7"   # the output is the same on every build (checked twice)

die() { echo "[get-hyperframes-runtime] ✗  $*" >&2; exit 1; }
for c in node npm git; do command -v "$c" >/dev/null || die "$c is needed to build the runtime (only for this step)"; done
node -e 'process.exit(parseInt(process.versions.node) >= 20 ? 0 : 1)' || die "Node 20 or newer is needed"

if [[ -s "${DEST}" && -s "${DEST}.sha256" ]] && [[ "$(sha256sum "${DEST}" | cut -d' ' -f1)" == "$(cut -d' ' -f1 "${DEST}.sha256")" ]]; then
    echo "[get-hyperframes-runtime] ✓  already built: ${DEST}"; exit 0
fi

case "$(uname -m)" in x86_64) plat=linux-x64 ;; aarch64|arm64) plat=linux-arm64 ;; *) die "no esbuild binary for $(uname -m)" ;; esac

if [[ -d "${CACHE}/.git" ]] && [[ "$(git -C "${CACHE}" rev-parse HEAD 2>/dev/null)" == "${COMMIT}" ]]; then
    echo "[get-hyperframes-runtime] ✓  the clone is at ${COMMIT:0:7}: ${CACHE}"
else
    rm -rf "${CACHE}"; mkdir -p "${CACHE}"
    echo "[get-hyperframes-runtime] Fetching HyperFrames ${COMMIT:0:7} ..."
    git -C "${CACHE}" init -q; git -C "${CACHE}" remote add origin "${REPO}"
    git -C "${CACHE}" fetch -q --depth 1 origin "${COMMIT}" || die "cannot fetch ${REPO} at ${COMMIT}"
    git -C "${CACHE}" checkout -q FETCH_HEAD
fi

work="$(mktemp -d)"; trap 'rm -rf "${work}"' EXIT
echo "[get-hyperframes-runtime] Installing the build tools (esbuild ${ESBUILD}, postcss, acorn, pretext) ..."
( cd "${work}" && npm init -y >/dev/null 2>&1 \
  && npm install --ignore-scripts --no-audit --no-fund --save-exact "esbuild@${ESBUILD}" "@esbuild/${plat}@${ESBUILD}" \
       "postcss@${POSTCSS}" "@chenglou/pretext@${PRETEXT}" "acorn@${ACORN}" "acorn-walk@${ACORN_WALK}" >/dev/null 2>&1 ) \
  || die "cannot install the build tools with npm"

mkdir -p "${DEST_DIR}"
HF_CLONE="${CACHE}" ESBUILD_JS="${work}/node_modules/esbuild" ESBUILD_BIN="${work}/node_modules/@esbuild/${plat}/bin/esbuild" \
  NM="${work}/node_modules" OUT="${DEST}.tmp" node "${SCRIPT_DIR}/hyperframes-runtime-build.cjs" || die "the build failed"
got="$(sha256sum "${DEST}.tmp" | cut -d' ' -f1)"
if [[ -n "${EXPECTED_SHA256}" && "${got}" != "${EXPECTED_SHA256}" ]]; then
    rm -f "${DEST}.tmp"; die "the runtime built has sha256 ${got}, not the pinned ${EXPECTED_SHA256}: nothing installed"
fi
mv "${DEST}.tmp" "${DEST}"; echo "${got}  hyperframe.runtime.iife.js" > "${DEST}.sha256"
echo "[get-hyperframes-runtime] ✓  runtime built: ${DEST}"
echo "[get-hyperframes-runtime]    sha256 ${got}"
