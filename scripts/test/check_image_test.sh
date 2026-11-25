#!/usr/bin/env bash
# Tests of scripts/check-image.sh with small images built FROM scratch (no network, nothing is run): a model, Stable Fast 3D and Chrome
# are refused and named, a library over 200 MB is not, and a clean image passes. Skipped without a working Docker.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

if ! docker info >/dev/null 2>&1; then echo "skipped: no working docker"; echo "ok: 0 assertions (skipped)"; exit 0; fi

D="$(mktemp -d "${TMPDIR:-/tmp}/check-image-test.XXXXXX")"
TAGS=()
cleanup() { for t in "${TAGS[@]}"; do docker rmi -f "$t" >/dev/null 2>&1; done; rm -rf "$D"; }
trap cleanup EXIT

# image NAME 'COPY lines' : builds check-image-test/NAME from the files already in $D
image() { printf 'FROM scratch\n%s\n' "$2" >"$D/Dockerfile.$1"; docker build -q -f "$D/Dockerfile.$1" -t "check-image-test/$1" "$D" >/dev/null 2>&1 && TAGS+=("check-image-test/$1"); }
check() { IMAGES_DOC="${IMAGES_DOC:-/nonexistent}" bash "$TEST_REPO/scripts/check-image.sh" "$1" 2>&1; }

mkdir -p "$D/sf3d" "$D/chrome-linux64" "$D/clean"
echo w >"$D/x.safetensors"; echo s >"$D/sf3d/readme"; echo c >"$D/chrome-linux64/chrome"; echo hi >"$D/clean/hello.txt"
head -c 12000000 /dev/zero >"$D/big.bin"; head -c 215000000 /dev/zero >"$D/libbig.so.1"; head -c 215000000 /dev/zero >"$D/huge.dat"

image clean 'COPY clean/hello.txt libbig.so.1 /app/'
out="$(check check-image-test/clean)"; st=$?
assert_status "a clean image, with a large shared library, passes" 0 "$st"
assert_contains "says it is clean" "$out" "is clean"

image model 'COPY x.safetensors /m/'
out="$(check check-image-test/model)"; st=$?
assert_status "a model fails" 1 "$st"
assert_contains "names the file" "$out" "/m/x.safetensors"

image bin 'COPY big.bin /m/'
out="$(check check-image-test/bin)"; st=$?
assert_status "a .bin over 10 MB fails" 1 "$st"
assert_contains "names it" "$out" "/m/big.bin"

image huge 'COPY huge.dat /d/'
out="$(check check-image-test/huge)"; st=$?
assert_status "a file over 200 MB fails" 1 "$st"
assert_contains "names it" "$out" "/d/huge.dat"

image sf3d 'COPY sf3d /opt/stable-fast-3d'
out="$(check check-image-test/sf3d)"; st=$?
assert_status "Stable Fast 3D fails" 1 "$st"
assert_contains "names it" "$out" "Stable Fast 3D"

image chrome 'COPY chrome-linux64 /opt/chrome-linux64'
out="$(check check-image-test/chrome)"; st=$?
assert_status "Chrome fails without permission" 1 "$st"
assert_contains "names it" "$out" "/opt/chrome-linux64/chrome"
printf '# Images\n<!-- chrome-redistribution: allowed -->\n' >"$D/IMAGES.md"
out="$(IMAGES_DOC="$D/IMAGES.md" check check-image-test/chrome)"; st=$?
assert_status "Chrome passes when docs/IMAGES.md records permission" 0 "$st"

out="$(check no-such-image-here)"; st=$?
assert_status "an unknown image is a usage error, not a pass" 2 "$st"

pass_summary
