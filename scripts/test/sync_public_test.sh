#!/usr/bin/env bash
# Tests of scripts/sync-public.sh on a small repository (no Go build: SYNC_SKIP_GO=1).
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"
SYNC="$TEST_REPO/scripts/sync-public.sh"
CHECK="$TEST_REPO/scripts/check-docs.sh"
REAL_GIT="$(command -v git)"; REAL_TAR="$(command -v tar)"; REAL_PERL="$(command -v perl)"

# fake_project: a repository on main with private files and private documentation, and a
# public-release worktree that owns its own README.
fake_project() {
    ln -sf "$REAL_GIT" "$T_BIN/git"; ln -sf "$REAL_TAR" "$T_BIN/tar"; ln -sf "$REAL_PERL" "$T_BIN/perl"
    P="$T_DIR/main"; W="$T_DIR/public"
    mkdir -p "$P/scripts" "$P/docs" "$P/internal" && cd "$P" || exit 3
    git init -q -b main . && git config user.email t@t && git config user.name t
    cp "$SYNC" scripts/sync-public.sh; cp "$CHECK" scripts/check-docs.sh
    printf 'internal/secret.go\nscripts/private-files.txt\nscripts/sync-public.sh\n' >scripts/private-files.txt
    printf 'package x\n' >internal/secret.go; printf 'package y\n' >internal/open.go
    printf '# Private readme\n' >README.md
    printf '# Guide\n\nKeep this line.\n<!-- private:start -->\nonly private\n<!-- private:end -->\nA <!--[private]-->hidden<!--[/private]-->visible sentence.\n| row | private |<!--[private-line]-->\n| row | kept |\n' >docs/guide.md
    printf '# Private readme\n' >README.md
    # the markers are written in two pieces so that this file, which is exported, does not hold one
    printf 'services:\n  a:\n    image: local/a:dev # [private''-line]\n    # [public''-line] image: ghcr.io/x/a:latest\n    build: .\n' >compose.yaml
    git add -A && git commit -q -m main-1
    git branch public-release && git worktree add -q "$W" public-release
    ( cd "$W" && printf '# Public readme\n' >README.md && git add -A && git commit -q -m pub-1 )
    export PUBLIC_DIR="$W" SYNC_SKIP_GO=1
}

run() { out="$(bash scripts/sync-public.sh "$@" 2>&1)"; st=$?; }

# --- a sync leaves out what is private and keeps what the public edition owns -------------
sandbox; fake_project
run "first sync"
assert_status "sync" 0 "$st"
assert_eq "private file is not exported" "" "$(ls "$W/internal/secret.go" 2>/dev/null)"
assert_eq "the list itself is not exported" "" "$(ls "$W/scripts/private-files.txt" 2>/dev/null)"
assert_eq "public file is exported" "package y" "$(cat "$W/internal/open.go")"
assert_eq "README stays the public one" "# Public readme" "$(cat "$W/README.md")"
doc="$(cat "$W/docs/guide.md")"
assert_contains "kept line" "$doc" "Keep this line."
assert_not_contains "block cut" "$doc" "only private"
assert_not_contains "inline cut" "$doc" "hidden"
assert_contains "inline neighbours stay" "$doc" "A visible sentence."
assert_not_contains "line cut" "$doc" "| row | private |"
assert_contains "other rows stay" "$doc" "| row | kept |"
assert_not_contains "no marker is left" "$doc" "private"
compose="$(cat "$W/compose.yaml")"
assert_contains "the public line becomes the line" "$compose" "    image: ghcr.io/x/a:latest"
assert_not_contains "the private line is cut" "$compose" "local/a"
assert_not_contains "no [public-line] marker is left" "$compose" "public-line"
assert_contains "the build stays" "$compose" "build: ."
assert_eq "one new commit" "Sync with the main branch: first sync" "$(git -C "$W" log -1 --format=%s)"
assert_eq "the commit is on public-release" "public-release" "$(git -C "$W" rev-parse --abbrev-ref HEAD)"

# --- running it again changes nothing -----------------------------------------------------
before="$(git -C "$W" rev-parse HEAD)"
run "again"
assert_status "second sync" 0 "$st"
assert_eq "no new commit" "$before" "$(git -C "$W" rev-parse HEAD)"
assert_contains "says it is up to date" "$out" "already up to date"

# --- a file removed from main is removed from the public branch ----------------------------
git rm -q internal/open.go && git commit -q -m "drop open.go"
run "drop"
assert_status "sync after removal" 0 "$st"
assert_eq "removed file is gone" "" "$(ls "$W/internal/open.go" 2>/dev/null)"
cleanup_sandbox

# --- refusals ---------------------------------------------------------------------------------
sandbox; fake_project
echo dirty >>README.md
run "x"
assert_status "dirty main" 1 "$st"
assert_contains "dirty main message" "$out" "uncommitted"
git checkout -q README.md
printf 'x\n<!-- private:start -->\nnever closed\n' >docs/bad.md && git add -A && git commit -q -m bad
run "x"
assert_status "unbalanced marker" 1 "$st"
assert_contains "unbalanced message" "$out" "unbalanced"
assert_eq "nothing was committed" "pub-1" "$(git -C "$W" log -1 --format=%s)"
git rm -q docs/bad.md && git commit -q -m fix
printf 'a <!--[private]-->never closed on this line\n' >docs/bad2.md && git add -A && git commit -q -m bad2
run "x"
assert_status "marker left" 1 "$st"
assert_contains "marker left message" "$out" "marker is left"
git rm -q docs/bad2.md && git commit -q -m fix2
run
assert_status "no message" 2 "$st"
run --dry-run
assert_status "dry run" 0 "$st"
assert_eq "dry run touches nothing" "pub-1" "$(git -C "$W" log -1 --format=%s)"
cleanup_sandbox

# --- nothing personal, no binary, no big file in the public tree ---------------------------
sandbox; fake_project
printf 'see /home/%s/notes\n' jesus >docs/trace.md && git add -A && git commit -q -m trace
run "x"
assert_status "personal path" 1 "$st"
assert_contains "personal path names the file" "$out" "docs/trace.md"
git rm -q docs/trace.md && git commit -q -m fix
printf '{"host": "cachyos-%s"}\n' tocho >internal/bench.json && git add -A && git commit -q -m host
run "x"
assert_status "host name" 1 "$st"
assert_contains "host name names the file" "$out" "internal/bench.json"
git rm -q internal/bench.json && git commit -q -m fix
mkdir -p internal/__pycache__ && printf 'x' >internal/__pycache__/a.cpython-314.pyc && git add -A && git commit -q -m pyc
run "x"
assert_status "byte-code" 1 "$st"
assert_contains "byte-code names the file" "$out" "a.cpython-314.pyc"
git rm -q -r internal/__pycache__ && git commit -q -m fix
printf '\177ELF\002\001\001 and the rest of a program' >internal/tool && git add -A && git commit -q -m elf
run "x"
assert_status "compiled program" 1 "$st"
assert_contains "compiled program names the file" "$out" "internal/tool"
git rm -q internal/tool && git commit -q -m fix
head -c 3000000 /dev/zero >internal/big.bin && git add -A && git commit -q -m big
LARGE_BYTES=1000000 run "x"
assert_status "big file" 1 "$st"
assert_contains "big file names the file" "$out" "internal/big.bin"
git rm -q internal/big.bin && git commit -q -m fix
run "x"
assert_status "clean tree again" 0 "$st"
cleanup_sandbox

pass_summary
