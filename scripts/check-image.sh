#!/usr/bin/env bash
# scripts/check-image.sh
#
# Looks inside a Docker image, WITHOUT running it, and fails when it holds what no published image may hold. It reads every layer
# (a model deleted in a later layer is still in the image that gets downloaded), so it is not enough to look at the final files.
#
#   bash scripts/check-image.sh <image>        # an image the local Docker has (docker save), by name or id
#
# A file is refused when it is
#   - a model: .gguf, .safetensors, .ckpt, .pt, .pth, .onnx, or .bin over 10 MB;
#   - any regular file over 200 MB that is not a library, shared or static (the CUDA libraries of NVIDIA's base images are legitimately large);
#   - of Stable Fast 3D (a path with stable-fast-3d or sf3d), whose licence has conditions;
#   - Chrome for Testing (a path with chrome-linux64), unless docs/IMAGES.md carries the line
#       <!-- chrome-redistribution: allowed -->
#     which is how the licence reading of docs/IMAGES.md records that its terms allow redistributing it.
#
# It prints the offending paths with the layer and the size, and exits 1; 0 when the image is clean; 2 on a usage error.
# Disk: `docker save` writes the image once to a temporary file (TMPDIR, /tmp by default) and removes it at the end.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
IMAGES_DOC="${IMAGES_DOC:-$ROOT/docs/IMAGES.md}"
MODEL_BIN_BYTES="${MODEL_BIN_BYTES:-10485760}"
BIG_FILE_BYTES="${BIG_FILE_BYTES:-209715200}"

[[ $# -eq 1 && "$1" != -* ]] || { sed -n '2,/^set /p' "${BASH_SOURCE[0]}" | sed '$d; s/^# \{0,1\}//'; exit 2; }
IMAGE="$1"
command -v docker >/dev/null 2>&1 || { echo "check-image: docker is not installed" >&2; exit 2; }
docker image inspect "$IMAGE" >/dev/null 2>&1 || { echo "check-image: the image '$IMAGE' is not in the local Docker" >&2; exit 2; }

CHROME_ALLOWED=0
if [[ -f "$IMAGES_DOC" ]] && grep -q '<!-- chrome-redistribution: allowed -->' "$IMAGES_DOC"; then CHROME_ALLOWED=1; fi

WORK="$(mktemp -d "${TMPDIR:-/tmp}/check-image.XXXXXX")"
trap 'rm -rf "$WORK"' EXIT
SAVE="$WORK/image.tar"
echo "check-image: reading $IMAGE ..."
docker save "$IMAGE" -o "$SAVE"

# the layers, in the order of the image's manifest (Docker's own and the OCI layout both write manifest.json)
MANIFEST="$(tar -xOf "$SAVE" manifest.json)"
LAYERS="$(printf '%s' "$MANIFEST" | sed -n 's/.*"Layers":\[\([^]]*\)\].*/\1/p' | tr ',' '\n' | tr -d '" ')"
[[ -n "$LAYERS" ]] || { echo "check-image: no layers in the manifest of $IMAGE" >&2; exit 2; }

# unpack one layer to a listing on stdout, whatever its compression
list_layer() {
    local blob="$1" magic
    magic="$(tar -xOf "$SAVE" "$blob" | head -c 4 | od -An -tx1 | tr -d ' \n')"
    case "$magic" in
        1f8b*)     tar -xOf "$SAVE" "$blob" | gzip -dc | tar -tvf - ;;
        28b52ffd)  tar -xOf "$SAVE" "$blob" | zstd -dc | tar -tvf - ;;
        *)         tar -xOf "$SAVE" "$blob" | tar -tvf - ;;
    esac
}

BAD="$WORK/bad.txt"; LIST="$WORK/layer.txt"; : > "$BAD"
n=0; total=0
while IFS= read -r layer; do
    [[ -n "$layer" ]] || continue
    n=$((n + 1))
    # A listing that fails is an error, never "clean": the guard fails closed.
    list_layer "$layer" > "$LIST" || { echo "check-image: cannot read layer $n ($layer) of $IMAGE" >&2; exit 2; }
    awk -v layer="$n" -v modelbin="$MODEL_BIN_BYTES" -v big="$BIG_FILE_BYTES" -v chrome="$CHROME_ALLOWED" '
        # tar -tv: perms owner size date time path [-> link]; only regular files count
        substr($1, 1, 1) != "-" { next }
        {
            size = $3 + 0
            path = $0; sub(/^[^ ]+ +[^ ]+ +[0-9]+ +[^ ]+ +[^ ]+ +/, "", path)
            low = tolower(path)
            why = ""
            if (low ~ /\.(gguf|safetensors|ckpt|pt|pth|onnx)$/) why = "a model file"
            else if (low ~ /\.bin$/ && size > modelbin) why = "a .bin file over " int(modelbin / 1048576) " MB"
            else if (size > big && low !~ /\.(so(\.[0-9]+)*|a)$/) why = "a file over " int(big / 1048576) " MB"
            if (low ~ /(stable-fast-3d|sf3d)/) why = (why == "" ? "" : why "; ") "part of Stable Fast 3D"
            if (chrome != 1 && low ~ /chrome-linux64/) why = (why == "" ? "" : why "; ") "Chrome for Testing, whose terms are not recorded as allowing redistribution"
            if (why != "") printf "  layer %d: /%s (%.1f MB): %s\n", layer, path, size / 1048576, why
        }' "$LIST" >> "$BAD" || { echo "check-image: cannot analyse layer $n of $IMAGE" >&2; exit 2; }
done <<< "$LAYERS"

if [[ -s "$BAD" ]]; then
    echo "check-image: $IMAGE holds what a published image may not ($(wc -l < "$BAD") paths):" >&2
    # grouped by reason, a few examples of each, so a whole directory does not drown the report
    awk '{ why = $0; sub(/^.*\): /, "", why); n[why]++; if (n[why] <= 6) ex[why] = ex[why] $0 "\n" }
         END { for (w in n) { printf "%s [%d paths]\n%s", w, n[w], ex[w]; if (n[w] > 6) printf "  ... and %d more\n", n[w] - 6 } }' "$BAD" >&2
    exit 1
fi
echo "check-image: $IMAGE is clean ($n layers)."
