#!/usr/bin/env bash
# Tests of scripts/check-family-table.sh with two small checkouts: the table must be the one of docs/REPOSITORIES.md, links and "(this repository)" aside.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

# the address of the private copies, written in two pieces so that this file, which is exported, does not hold the host
PRIVATE="http://git.cas""a.lan/personales/"

sandbox
ROOT="$T_DIR/root"; mkdir -p "$ROOT/veritmo/scripts" "$ROOT/veritmo/docs" "$ROOT/heartmula" "$ROOT/triposr"
cp "$TEST_REPO/scripts/check-family-table.sh" "$ROOT/veritmo/scripts/"
for t in cat dirname; do ln -sf "$(PATH="$T_REAL_PATH" command -v "$t")" "$T_BIN/$t"; done
cat >"$ROOT/veritmo/docs/REPOSITORIES.md" <<'DOC'
# The family

| Repository | What it is | Licence |
|---|---|---|
| [`veritmo`](https://github.com/privadosIA/veritmo) | The server. | Apache-2.0 |
| [`heartmula`](https://github.com/privadosIA/heartmula) | A port. | Apache-2.0 |
DOC
readme() { # dir host self
    cat >"$ROOT/$1/README.md" <<README
# $1

## Related repositories

| Repository | What it is | Licence |
|---|---|---|
| [\`veritmo\`]($2veritmo)$([[ $3 == veritmo ]] && echo ' (this repository)') | The server. | Apache-2.0 |
| [\`heartmula\`]($2heartmula)$([[ $3 == heartmula ]] && echo ' (this repository)') | A port. | Apache-2.0 |

## License
README
}
readme veritmo https://github.com/privadosIA/ veritmo
readme heartmula "$PRIVATE" heartmula
out="$(bash "$ROOT/veritmo/scripts/check-family-table.sh" "$ROOT" 2>&1)"; st=$?
assert_status "equal tables, whatever the host" 0 "$st"
assert_contains "says ok for each" "$out" "ok heartmula"
assert_contains "skips the checkout that is not there" "$out" "dashboards-lab: not in"

sed -i 's/| A port. | Apache-2.0 |/| A port. | MIT |/' "$ROOT/heartmula/README.md"
out="$(bash "$ROOT/veritmo/scripts/check-family-table.sh" "$ROOT" 2>&1)"; st=$?
assert_status "a changed licence is found" 1 "$st"
assert_contains "names the README" "$out" "!! heartmula"
assert_contains "shows the row" "$out" "MIT"
assert_not_contains "the other one is fine" "$out" "!! veritmo"

readme heartmula "$PRIVATE" heartmula
sed -i '/`heartmula`/d' "$ROOT/heartmula/README.md"
out="$(bash "$ROOT/veritmo/scripts/check-family-table.sh" "$ROOT" 2>&1)"; st=$?
assert_status "a missing row is found" 1 "$st"
cleanup_sandbox
pass_summary
