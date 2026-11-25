#!/usr/bin/env bash
# Tests of scripts/get-services.sh with a fake dotnet that "publishes" a few files (no SDK, no big builds).
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

setup() {
    sandbox
    SVC="$IL_ROOT/bin/services"
    cp "$TEST_REPO/scripts/get-services.sh" "$IL_ROOT/scripts/get-services.sh"
    local p
    for p in dotnet/diffrhythm2/src/DiffRhythm2.Service/DiffRhythm2.Service.csproj \
             dotnet/diffrhythm2/src/DiffRhythm2.Cli/DiffRhythm2.Cli.csproj \
             dotnet/lion-clap/src/LionClap.Service/LionClap.Service.csproj \
             dotnet/video-vision/src/VideoVision.Service/VideoVision.Service.csproj \
             dotnet/triposr/src/TripoSR.Service/TripoSR.Service.csproj \
             dotnet/stable-fast-3d/src/StableFast3D.Service/StableFast3D.Service.csproj \
             dotnet/heartmula/src/HeartMuLa.Net.Server/HeartMuLa.Net.Server.csproj; do
        fake_repo_file "$p" "<Project/>"
    done
    # publish: -o <dir> gets a library every service shares, one that has the same name and size but another content in each
    # service, a small file and a file of its own. CALLS counts the publishes.
    stub dotnet 'out=""; proj=""; while [[ $# -gt 0 ]]; do case "$1" in -o) out="$2"; shift ;; publish) ;; *.csproj) proj="$1" ;; esac; shift; done
echo "$proj" >>"'"$T_DIR"'/calls"
mkdir -p "$out"
head -c 1500000 /dev/zero | tr "\0" "s" >"$out/libshared.so"
svc="$(basename "$(dirname "$(dirname "$(dirname "$proj")")")")"
head -c 1500000 /dev/zero | tr "\0" "${svc:0:1}" >"$out/libsame_size.so"
head -c 1500000 /dev/zero | tr "\0" "u" >"$out/lib_${svc}_only.so"
echo "small" >"$out/small.json"'
}
inode() { stat -c %i "$1"; }

# --- a full build: the shared library is kept once, nothing else is touched ---------------------
setup
out="$(bash "$IL_ROOT/scripts/get-services.sh" 2>&1)"; st=$?
assert_status "full build" 0 "$st"
assert_contains "says what it saved" "$out" "identical libraries are kept once"
a="$(inode "$SVC/diffrhythm2/libshared.so")"
assert_eq "heartmula shares it" "$a" "$(inode "$SVC/heartmula/libshared.so")"
assert_eq "lionclap shares it" "$a" "$(inode "$SVC/lionclap/libshared.so")"
assert_eq "video-vision shares it" "$a" "$(inode "$SVC/videovision/libshared.so")"
assert_eq "same name and size, other content: not linked" "no" "$([[ "$(inode "$SVC/heartmula/libsame_size.so")" == "$(inode "$SVC/lionclap/libsame_size.so")" ]] && echo yes || echo no)"
assert_eq "its content is kept" "hhhh" "$(head -c 4 "$SVC/heartmula/libsame_size.so")"
assert_eq "a small file is not linked" "no" "$([[ "$(inode "$SVC/heartmula/small.json")" == "$(inode "$SVC/lionclap/small.json")" ]] && echo yes || echo no)"
assert_eq "a file of its own is there" "1" "$(ls "$SVC/lionclap" | grep -c only)"
assert_eq "no folder is left half-built" "" "$(ls "$SVC" | grep '\.new$')"

# --- one service rebuilt: its new files are linked again and the others are as they were -------
before="$(inode "$SVC/diffrhythm2/libshared.so")"
: >"$T_DIR/calls"
out="$(bash "$IL_ROOT/scripts/get-services.sh" --only lionclap 2>&1)"; st=$?
assert_status "rebuild one" 0 "$st"
assert_eq "one publish" "1" "$(wc -l <"$T_DIR/calls" | tr -d ' ')"
assert_eq "the shared library is still one file" "$before" "$(inode "$SVC/lionclap/libshared.so")"
assert_eq "the others are untouched" "$before" "$(inode "$SVC/heartmula/libshared.so")"
assert_eq "a hard link is never written through" "sssss" "$(head -c 5 "$SVC/heartmula/libshared.so")"

# --- running it again changes nothing -------------------------------------------------------
out="$(bash "$IL_ROOT/scripts/get-services.sh" --share-only 2>&1)"; st=$?
assert_status "share only" 0 "$st"
assert_contains "already shared" "$out" "already shared"
cleanup_sandbox

# --- --no-share keeps separate copies; --share-only then links them without building -------------
setup
out="$(bash "$IL_ROOT/scripts/get-services.sh" --no-share 2>&1)"; st=$?
assert_status "no share" 0 "$st"
assert_not_contains "says nothing about sharing" "$out" "kept once"
assert_eq "separate copies" "no" "$([[ "$(inode "$SVC/heartmula/libshared.so")" == "$(inode "$SVC/lionclap/libshared.so")" ]] && echo yes || echo no)"
: >"$T_DIR/calls"
out="$(bash "$IL_ROOT/scripts/get-services.sh" --share-only 2>&1)"; st=$?
assert_status "share only after" 0 "$st"
assert_eq "nothing was built" "0" "$(wc -l <"$T_DIR/calls" | tr -d ' ')"
assert_eq "now they are one file" "$(inode "$SVC/heartmula/libshared.so")" "$(inode "$SVC/lionclap/libshared.so")"
assert_contains "reports the saving" "$out" "MB saved"
cleanup_sandbox

# --- no dotnet and no --share-only: the script says so -----------------------------------------
setup
rm -f "$T_BIN/dotnet"
out="$(bash "$IL_ROOT/scripts/get-services.sh" 2>&1)"; st=$?
assert_status "no dotnet" 1 "$st"
assert_contains "says why" "$out" "dotnet is not installed"
cleanup_sandbox

pass_summary
