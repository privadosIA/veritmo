#!/usr/bin/env bash
# Tests of scripts/music-containers.sh with a fake docker that records its calls.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

setup() {
    sandbox
    mkdir -p "$IL_ROOT/scripts" "$IL_ROOT/docker/heartmula"
    cp "$TEST_REPO/scripts/music-containers.sh" "$IL_ROOT/scripts/"
    CALLS="$T_DIR/calls"; export CALLS
    stub docker 'echo "$*" >>"$CALLS"; case "$1" in inspect) echo running;; image) [[ -n "${NO_IMAGE:-}" ]] && exit 1; exit 0;; esac'
}
run() { out="$(bash "$IL_ROOT/scripts/music-containers.sh" "$@" 2>&1)"; st=$?; }

setup
run build heartmula
assert_status "build" 0 "$st"
assert_contains "builds the folder of the engine" "$(cat "$CALLS")" "build -t veritmo/heartmula:1 $IL_ROOT/docker/heartmula"
cleanup_sandbox

setup
run up heartmula 1
assert_status "no weights is refused" 1 "$st"
assert_contains "says where" "$out" "no weights in"
mkdir -p "$IL_ROOT/models/music/heartmula"
NO_IMAGE=1 run up heartmula
assert_status "no image is refused" 1 "$st"
assert_contains "says how to build" "$out" "scripts/music-containers.sh build heartmula"
run up heartmula 1
assert_status "up" 0 "$st"
assert_contains "one card, the loopback port, the weights read-only" "$(cat "$CALLS")" "--gpus device=1 -p 127.0.0.1:8787:8787 -v $IL_ROOT/models/music/heartmula:/ckpt:ro"
assert_contains "tells what to set" "$out" '"heartmula_host": "http://127.0.0.1:8787"'
run up heartmula x
assert_status "a card that is not a number" 1 "$st"
run down diffrhythm2
assert_status "down" 0 "$st"
assert_contains "removes it" "$(cat "$CALLS")" "rm -f vt-diffrhythm2"
run status
assert_contains "status lists both" "$out" "diffrhythm2  container: running"
run up nothing
assert_status "an unknown engine" 1 "$st"
run weights heartmula
assert_status "only diffrhythm2 fetches" 1 "$st"
cleanup_sandbox

pass_summary
