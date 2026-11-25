#!/usr/bin/env bash
# Tests of docker/model3d/install-sf3d.sh: Stable Fast 3D is added to the image only with the person's explicit acceptance of its licence.
# The script stops at the licence before anything else; with the acceptance it goes on until the first thing it needs (nvcc, absent here).
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

sandbox
cp "$TEST_REPO/docker/model3d/install-sf3d.sh" "$IL_ROOT/install-sf3d.sh"
ln -sf "$(PATH="$T_REAL_PATH" command -v sh)" "$T_BIN/sh"

out="$(sh "$IL_ROOT/install-sf3d.sh" 2>&1)"; st=$?
assert_status "without the acceptance" 1 "$st"
assert_contains "names the licence" "$out" "Stability AI Community License"
assert_contains "gives its address" "$out" "https://stability.ai/community-license-agreement"
assert_contains "says how to accept" "$out" "--build-arg SF3D_LICENCE=stability-community"
assert_not_contains "it never got to the toolchain" "$out" "nvcc"

out="$(SF3D_LICENCE=mit sh "$IL_ROOT/install-sf3d.sh" 2>&1)"; st=$?
assert_status "another licence id is not the acceptance" 1 "$st"
assert_not_contains "still stopped at the licence" "$out" "nvcc"

out="$(SF3D_LICENCE=stability-community sh "$IL_ROOT/install-sf3d.sh" 2>&1)"; st=$?
assert_status "with the acceptance it goes on to the toolchain, which is not here" 1 "$st"
assert_contains "and says what it needs" "$out" "nvcc"
assert_not_contains "no licence message now" "$out" "Community License"

cleanup_sandbox
pass_summary
