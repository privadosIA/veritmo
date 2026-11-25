#!/usr/bin/env bash
# scripts/get-chrome.sh
#
# Installs Chrome for Testing (the browser that draws motion-graphic scenes and overlays) into bin/chrome-for-testing/ and a launcher at
# bin/chromium, which is what the server finds on its PATH. It is downloaded from Google's own server onto YOUR machine: Chrome for Testing
# is Google software ("All rights reserved", under the Chrome Terms of Service, and it carries the Widevine CDM, which may not be
# distributed without a licence from Google), so no image or archive of this project ships it. The download is checked against a pinned
# SHA-256.
#
#   bash scripts/get-chrome.sh
#   CHROME_VERSION=159.0.1.2 CHROME_SHA256=<sum of that zip> bash scripts/get-chrome.sh      # another version needs its checksum
#
# Safe to re-run: when that version is already there nothing is downloaded. Without it, the motion-graphic scenes are simply
# unavailable (the Environment screen says so); a Chrome or Chromium of your package manager works as well (scripts/install.sh --only browser).
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
BIN_DIR="${BIN_DIR:-$(cd "${SCRIPT_DIR}/.." && pwd)/bin}"
CHROME_VERSION="${CHROME_VERSION:-158.0.8127.36}"
PINNED_SHA256="dba9cc838e1ce269c33ea68170881127bd15d078fc3ca5891c90802819a4d7c7"
if [[ "${CHROME_VERSION}" == "158.0.8127.36" ]]; then CHROME_SHA256="${CHROME_SHA256:-${PINNED_SHA256}}"; else CHROME_SHA256="${CHROME_SHA256:-}"; fi
URL="${CHROME_URL:-https://storage.googleapis.com/chrome-for-testing-public/${CHROME_VERSION}/linux64/chrome-linux64.zip}"
DEST="${BIN_DIR}/chrome-for-testing"

[[ -n "${CHROME_SHA256}" ]] || { echo "[get-chrome] ✗  CHROME_VERSION=${CHROME_VERSION} is not the pinned one: give its zip's checksum in CHROME_SHA256." >&2; exit 2; }

if [[ -x "${DEST}/chrome-linux64/chrome" && "$(cat "${DEST}/VERSION" 2>/dev/null)" == "${CHROME_VERSION}" && -x "${BIN_DIR}/chromium" ]]; then
    echo "[get-chrome] ✓  Chrome for Testing ${CHROME_VERSION} is already at ${DEST}"
    exit 0
fi

command -v unzip >/dev/null 2>&1 || { echo "[get-chrome] ✗  unzip is needed (install it with your package manager)." >&2; exit 2; }
mkdir -p "${BIN_DIR}"
TMP="$(mktemp -d)"
trap 'rm -rf "${TMP}"' EXIT

echo "[get-chrome] Downloading Chrome for Testing ${CHROME_VERSION} from Google (its own terms apply: chrome://terms) ..."
curl -fL "${URL}" -o "${TMP}/chrome.zip"
got="$(sha256sum "${TMP}/chrome.zip" | awk '{print $1}')"
if [[ "${got}" != "${CHROME_SHA256}" ]]; then
    echo "[get-chrome] ✗  checksum mismatch (expected '${CHROME_SHA256}', got '${got}'): nothing installed." >&2
    exit 1
fi

rm -rf "${DEST}.new" && mkdir -p "${DEST}.new"
unzip -q "${TMP}/chrome.zip" -d "${DEST}.new"
echo "${CHROME_VERSION}" > "${DEST}.new/VERSION"
rm -rf "${DEST}" && mv "${DEST}.new" "${DEST}"

# The launcher: inside a container the container is the sandbox (Chrome's own needs user namespaces, which Docker's default profile does
# not give); on a host Chrome keeps its sandbox.
cat > "${BIN_DIR}/chromium" <<'LAUNCHER'
#!/bin/sh
# Written by scripts/get-chrome.sh
dir="$(cd "$(dirname "$0")" && pwd)"
flags=""
[ -e /.dockerenv ] && flags="--no-sandbox"
exec "$dir/chrome-for-testing/chrome-linux64/chrome" $flags "$@"
LAUNCHER
chmod 755 "${BIN_DIR}/chromium"

echo "[get-chrome] ✓  Installed ${DEST} and ${BIN_DIR}/chromium"
if command -v ldd >/dev/null 2>&1; then
    missing="$(ldd "${DEST}/chrome-linux64/chrome" 2>/dev/null | awk '/not found/ {print $1}' | sort -u | tr '\n' ' ')"
    if [[ -n "${missing}" ]]; then
        echo "[get-chrome] !  Chrome needs system libraries this machine lacks: ${missing}" >&2
        echo "[get-chrome]    (install your package manager's chromium, or the packages that provide them)" >&2
    fi
fi
