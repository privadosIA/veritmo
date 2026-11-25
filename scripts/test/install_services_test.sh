#!/usr/bin/env bash
# Tests of the tiers of the music services in scripts/install.sh (openspec plan-services-installation, 1.3). Nothing is built.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
source "$(dirname "${BASH_SOURCE[0]}")/machine.sh"

INSTALL="$TEST_REPO/scripts/install.sh"
dry() { out="$(bash "$INSTALL" --dry-run --only services "$@" </dev/null 2>&1)"; st=$?; }
ask() { local answers="$1"; shift; out="$(printf '%b' "$answers" | IL_FORCE_INTERACTIVE=1 bash "$INSTALL" --only services "$@" 2>&1)"; st=$?; }

machine() { # [gpu lines]: a complete machine with a .NET that can build and run the services
    sandbox; complete_machine
    stub dotnet 'case "$1" in --version) echo "8.0.100";; --list-runtimes) echo "Microsoft.AspNetCore.App 8.0.0 [/x]";; esac'
    fake_repo_file scripts/get-services.sh '#!/usr/bin/env bash
echo services'
}

# --- the recommendation follows the cards ------------------------------------------------
machine   # a 12 GB card and a 16 GB one
dry
assert_status "two cards" 0 "$st"
assert_contains "a 16 GB card: every service" "$out" "bash scripts/get-services.sh --only lionclap,videovision,diffrhythm2,heartmula"
cleanup_sandbox

machine
stub nvidia-smi 'echo "0, NVIDIA GeForce RTX 4080 Laptop GPU, 8.9, 12282"'
dry
assert_contains "one 12 GB card: one generator" "$out" "bash scripts/get-services.sh --only lionclap,videovision,diffrhythm2"
assert_not_contains "and not HeartMuLa" "$out" "heartmula"
cleanup_sandbox

machine
rm "$T_BIN/nvidia-smi"
dry
assert_contains "no NVIDIA card: only the recognition" "$out" "get-services.sh --only lionclap,videovision"
assert_not_contains "no generator" "$out" "diffrhythm2"
cleanup_sandbox

# --- --services decides --------------------------------------------------------------------
machine
dry --services recognition --3d none
assert_contains "recognition asked for" "$out" "get-services.sh --only lionclap,videovision"
assert_not_contains "no generator" "$out" "diffrhythm2"
dry --services=generator
assert_contains "generator asked for" "$out" "--only lionclap,videovision,diffrhythm2"
dry --services none --3d none
assert_contains "none is said" "$out" "tier none: nothing asked for"
assert_not_contains "and nothing runs" "$out" "would run"
bash "$INSTALL" --dry-run --services nonsense </dev/null >/dev/null 2>&1; st=$?
assert_status "an unknown tier" 2 "$st"
cleanup_sandbox

# --- a machine with no GPU builds the CPU flavour ---------------------------------------------------
machine
rm "$T_BIN/nvidia-smi"
# the CPU build is the one a machine with no NVIDIA card gets: install.sh adds --cpu through CPU_BUILD, which only sd's question sets,
# so the tier is what this test looks at
dry --services all
assert_contains "all asked for on a machine with no card" "$out" "--only lionclap,videovision,diffrhythm2,heartmula"
cleanup_sandbox

# --- the question: Enter takes the recommendation, a name takes that tier -----------------------------
machine
ask '\n\ny\n'
assert_contains "the question lists the tiers" "$out" "none | recognition"
assert_contains "and the recommendation" "$out" "Recommended here: all"
ask 'recognition\nnone\ny\n'
assert_contains "a name is taken" "$out" "get-services.sh --only lionclap,videovision"
assert_not_contains "and only that" "$out" "heartmula"
ask 'bogus\ngenerator\nnone\ny\n'
assert_contains "a wrong answer asks again" "$out" "none, recognition, generator or all"
assert_contains "then the right one counts" "$out" "--only lionclap,videovision,diffrhythm2"
cleanup_sandbox

# --- the 3D services: their own tiers, apart from the music ones ----------------------------------------
machine   # a 12 GB card and a 16 GB one: both are 8 GB or more
dry --services recognition
assert_contains "3D follows the card: TripoSR" "$out" "--only lionclap,videovision,triposr"
assert_not_contains "and never Stable Fast 3D by itself" "$out" "stable-fast-3d"
dry --services recognition --3d triposr
assert_contains "only TripoSR" "$out" "--only lionclap,videovision,triposr"
assert_not_contains "and not Stable Fast 3D" "$out" "stable-fast-3d"
dry --services recognition --3d=none
assert_not_contains "none: no 3D" "$out" "triposr"
dry --services none --3d all --accept-licence stability-community
assert_contains "3D alone, with the acceptance" "$out" "get-services.sh --only triposr,stable-fast-3d --accept-licence stability-community"
dry --services none --3d all
assert_status "--3d all without the acceptance is a usage error" 2 "$st"
assert_contains "naming the flag" "$out" "--accept-licence stability-community"
out="$(bash "$INSTALL" --yes --only services --3d all </dev/null 2>&1)"; st=$?
assert_status "--yes --3d all without the acceptance" 2 "$st"
assert_contains "names the flag too" "$out" "--accept-licence stability-community"
out="$(bash "$INSTALL" --yes --only services --services none --3d all --accept-licence stability-community </dev/null 2>&1)"
assert_contains "--yes with the flag builds both" "$out" "get-services.sh --only triposr,stable-fast-3d --accept-licence stability-community"
bash "$INSTALL" --dry-run --accept-licence mit </dev/null >/dev/null 2>&1; st=$?
assert_status "another licence id is not accepted" 2 "$st"
bash "$INSTALL" --dry-run --3d nonsense </dev/null >/dev/null 2>&1; st=$?
assert_status "an unknown 3D tier" 2 "$st"
ask 'recognition\n\ny\n'
assert_contains "the question lists the 3D tiers" "$out" "none | triposr | all"
assert_contains "and the recommendation" "$out" "Recommended here: triposr"
assert_contains "Enter builds TripoSR only" "$out" "--only lionclap,videovision,triposr"
assert_not_contains "Enter does not build Stable Fast 3D" "$out" "stable-fast-3d"
ask 'recognition\nall\naccept\ny\n'
assert_contains "all asks for the licence" "$out" "Stability AI Community License"
assert_contains "typing accept builds both" "$out" "--only lionclap,videovision,triposr,stable-fast-3d --accept-licence stability-community"
ask 'recognition\nall\nyes\ny\n'
assert_contains "anything but accept falls back to TripoSR" "$out" "--only lionclap,videovision,triposr"
assert_not_contains "and builds no Stable Fast 3D" "$out" "stable-fast-3d"
ask 'recognition\nbogus\ntriposr\ny\n'
assert_contains "a wrong answer asks again" "$out" "none, triposr or all"
assert_contains "then the right one counts" "$out" "--only lionclap,videovision,triposr"
cleanup_sandbox

machine   # a card of less than 8 GB, and none
stub nvidia-smi 'echo "0, NVIDIA GeForce RTX 3050, 8.6, 6144"'
dry --services recognition
assert_not_contains "a 6 GB card: no 3D" "$out" "triposr"
rm "$T_BIN/nvidia-smi"
dry --services recognition
assert_not_contains "no card: no 3D" "$out" "triposr"
cleanup_sandbox

# a built music tier with 3D still to build
machine
for n in lionclap videovision; do fake_repo_file "bin/services/$n/$n.dll"; done
dry --services recognition --3d all --accept-licence stability-community
assert_contains "3D is missing" "$out" "not built: triposr stable-fast-3d"
cleanup_sandbox

# --- --yes takes the recommended tier without asking ---------------------------------------------------
machine
out="$(bash "$INSTALL" --yes --only services </dev/null 2>&1)"; st=$?
assert_contains "the recommended tier is built" "$out" "bash scripts/get-services.sh --only lionclap,videovision,diffrhythm2,heartmula"
cleanup_sandbox

# --- a tier that is already built is left alone, a bigger one is not --------------------------------------
machine
for n in lionclap videovision; do fake_repo_file "bin/services/$n/$n.dll"; done
dry --services recognition --3d none
assert_contains "already built" "$out" "tier recognition"
assert_not_contains "no plan for it" "$out" "waiting for"
assert_not_contains "nothing to run" "$out" "would run"
dry --services generator
assert_contains "the bigger tier lacks DiffRhythm2" "$out" "not built: diffrhythm2"
assert_contains "and builds it" "$out" "--only lionclap,videovision,diffrhythm2"
cleanup_sandbox

pass_summary
