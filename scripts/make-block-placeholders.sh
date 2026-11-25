#!/usr/bin/env bash
# scripts/make-block-placeholders.sh
#
# Generates, with the project's own image engine (bin/sd), the cover pictures that some HyperFrames blocks (the carousels) list and the registry does not ship.
# They are abstract artwork (no people, no text, no logos) named like the files they stand for, kept in hyperframes/placeholders/ (or $HYPERFRAMES_DIR/placeholders/);
# `veritmo blocks placeholders` then copies them into the blocks that need them. Optional and slow (about 12 s a picture on a 12 GB card); without it
# the blocks get procedural pictures instead.
#
#   bash scripts/make-block-placeholders.sh [GPU]        # the card number, default 1
#   VERITMO_SD_MODEL=models/other.safetensors bash scripts/make-block-placeholders.sh
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "${SCRIPT_DIR}/.." && pwd)"
GPU="${1:-1}"
MODEL="${VERITMO_SD_MODEL:-${ROOT}/models/Juggernaut-XL_v9_RunDiffusionPhoto_v2.safetensors}"
OUT="${HYPERFRAMES_DIR:-${ROOT}/hyperframes}/placeholders"
[[ -x "${ROOT}/bin/sd" ]] || { echo "[make-block-placeholders] ✗  bin/sd is not built" >&2; exit 1; }
[[ -f "${MODEL}" ]] || { echo "[make-block-placeholders] ✗  no model at ${MODEL}" >&2; exit 1; }
mkdir -p "${OUT}"

NAMES=(artist-bob-seger artist-dire-straits artist-faces artist-little-feat artist-north-mississippi-allstars artist-sea-level artist-taj-mahal artist-tool artist-van-morrison
       pl-10-songs pl-bangers pl-bhindi-bhagee pl-chb-2 pl-chb pl-homework pl-sleeping pl-y2k
       track-back-together track-bitter-with-the-sweet track-cleaning-windows track-dixie-chicken-live-at-the-rainbow-theatre-london-u track-dweller-on-the-threshold track-far-east-man track-funky-shit)
STYLES=("warm amber and deep blue oil paint waves" "dusty red desert dunes at dusk, flat graphic shapes" "neon pink and teal retro grid sunset"
        "black and white high contrast ink splatter" "green forest mist, layered paper cut" "ocean blue and white foam, watercolor"
        "golden hour wheat field, impressionist brush" "dark chrome liquid metal, red light" "autumn leaves in orange and brown, soft focus"
        "pastel pink and mint geometric pattern" "electric yellow and black bold stripes" "turquoise and coral tropical leaves"
        "violet night sky with scattered stars" "burnt orange and cream mid-century shapes" "sage green and beige minimal arcs"
        "midnight blue with silver moonlight clouds" "iridescent bubblegum holographic gradient" "crimson and gold art deco fan pattern"
        "sunset over calm sea, warm gradient bands" "mossy stone and ferns, muted palette" "cool grey concrete with a single red circle"
        "stormy purple clouds with lightning glow" "soft peach and lavender dreamy blur" "bold primary colours collage of torn paper")
made=0
for i in "${!NAMES[@]}"; do
    f="${OUT}/${NAMES[$i]}.jpg"
    [[ -s "${f}" ]] && continue
    tmp="$(mktemp --suffix=.png)"
    CUDA_VISIBLE_DEVICES="${GPU}" "${ROOT}/bin/sd" -m "${MODEL}" -p "abstract album cover artwork, ${STYLES[$i]}, painterly, no text" \
        -n "text, letters, words, watermark, logo, people, faces, hands" --steps 20 --cfg-scale 6 -W 768 -H 768 --seed $((1000 + i)) -o "${tmp}" >/dev/null 2>&1 \
        || { echo "[make-block-placeholders] ✗  ${NAMES[$i]} failed" >&2; rm -f "${tmp}"; continue; }
    ffmpeg -y -v error -i "${tmp}" -q:v 3 "${f}"; rm -f "${tmp}"
    made=$((made + 1)); echo "[make-block-placeholders] ✓  ${NAMES[$i]}"
done
echo "[make-block-placeholders] ${made} pictures made in ${OUT}"
