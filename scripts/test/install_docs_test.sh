#!/usr/bin/env bash
# The installer and docs/INSTALL.md agree: every component's anchor is a heading of the guide,
# and every component is in the guide's component table.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
sandbox
# after the sandbox: the library moves the cache directory of the former name when it is loaded, and that must be the sandbox's HOME, not the real one
source "$TEST_REPO/scripts/lib/install-lib.sh"
mkdir -p "$T_DIR/tree"
cp -r "$TEST_REPO/docs" "$T_DIR/tree/docs"
cp "$TEST_REPO/README.md" "$T_DIR/tree/README.old"
# docs link to files at the repository root (../THIRD_PARTY.md), so the copy needs them too
cp "$TEST_REPO/THIRD_PARTY.md" "$T_DIR/tree/THIRD_PARTY.md"
cp "$TEST_REPO/CHANGELOG.md" "$T_DIR/tree/CHANGELOG.md"
# and to a few files deeper in the tree (the workflow that migrates, the API description)
for f in .gitea/workflows/migrar.yml internal/server/openapi.yaml; do
    [[ -f "$TEST_REPO/$f" ]] || continue
    mkdir -p "$T_DIR/tree/$(dirname "$f")"; cp "$TEST_REPO/$f" "$T_DIR/tree/$f"
done
{
    echo "# probe"
    for id in "${IL_COMPONENTS[@]}"; do
        echo "- [$id]($(il_anchor "$id"))"
    done
} >"$T_DIR/tree/README.md"
out="$(bash "$TEST_REPO/scripts/check-docs.sh" "$T_DIR/tree" 2>&1)"; st=$?
assert_status "every component anchor exists in INSTALL.md" 0 "$st"
assert_not_contains "no broken anchor" "$out" "broken link"

# every component appears in the guide's component table (by its anchor or title words)
guide="$(cat "$TEST_REPO/docs/INSTALL.md")"
for id in "${IL_COMPONENTS[@]}"; do
    anchor="$(il_anchor "$id")"; anchor="${anchor#docs/INSTALL.md}"
    assert_contains "component $id is linked from the table" "$guide" "]($anchor)"
done
# the installer options are all documented
for opt in --check --dry-run --yes --only --no-models --cpu-sd; do
    assert_contains "option $opt documented" "$guide" "\`$opt"
done
cleanup_sandbox
pass_summary
