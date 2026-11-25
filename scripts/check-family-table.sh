#!/usr/bin/env bash
# scripts/check-family-table.sh
#
# Checks that the table of the repositories of the family in the README of every repository is the one of docs/REPOSITORIES.md, links aside
# (a README may link GitHub or the private copies, and its own row says "(this repository)").
#
#   bash scripts/check-family-table.sh [ROOT]     # ROOT: the folder that holds the checkouts (default: the folder above this repository)
#
# It looks for these folders in ROOT and skips the ones that are not there: veritmo, veritmo-public (the public worktree), dashboards-lab,
# forms-lib, heartmula, diffrhythm2, lion-clap, musicgen-core, triposr, stable-fast-3d. Exit 1 when a table differs; the first differing row is shown.
set -euo pipefail

HERE="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
ROOT="${1:-$(dirname "$HERE")}"
SOURCE="$HERE/docs/REPOSITORIES.md"
[[ -f "$SOURCE" ]] || { echo "check-family-table: $SOURCE is not there" >&2; exit 2; }

# the rows of a table, one per line, with the host of the links and the "this repository" mark taken off; the header and the separator first
normalize() {
    awk '
        /^\| Repository \|/ { print; next }
        /^\|---/ { print; next }
        /^\| \[`/ {
            line = $0
            gsub(/ \(this repository\)/, "", line)
            gsub(/\(https:\/\/github\.com\/privadosIA\//, "(@", line)
            gsub(/\(http:\/\/git\.casa\.lan\/personales\//, "(@", line)
            print line
        }'
}

want="$(normalize <"$SOURCE")"
[[ -n "$want" ]] || { echo "check-family-table: no table in $SOURCE" >&2; exit 2; }

bad=0; checked=0
for dir in veritmo veritmo-public dashboards-lab forms-lib heartmula diffrhythm2 lion-clap musicgen-core triposr stable-fast-3d; do
    readme="$ROOT/$dir/README.md"
    [[ -f "$readme" ]] || { echo "  -  $dir: not in $ROOT, skipped"; continue; }
    # only the section "Related repositories": the table of another heading is not this one
    got="$(awk '/^## /{in_sec = ($0 ~ /^## Related repositories/)} in_sec' "$readme" | normalize)"
    checked=$((checked + 1))
    if [[ "$got" == "$want" ]]; then
        echo "  ok $dir"
    else
        bad=$((bad + 1))
        echo "  !! $dir: its table differs from docs/REPOSITORIES.md; first difference:"
        diff <(printf '%s\n' "$want") <(printf '%s\n' "$got") | grep -E '^[<>]' | head -2 | cut -c1-160 | sed 's/^/       /' || true
    fi
done
[[ "$checked" -gt 0 ]] || { echo "check-family-table: no README found under $ROOT" >&2; exit 2; }
[[ "$bad" -eq 0 ]] && echo "check-family-table: $checked READMEs have the table of docs/REPOSITORIES.md" || { echo "check-family-table: $bad README(s) differ" >&2; exit 1; }
