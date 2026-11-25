#!/usr/bin/env bash
# Tests of scripts/compose-init.sh with a fake Docker and a fake /proc/meminfo: the server's memory cap follows the RAM of the machine.
source "$(dirname "${BASH_SOURCE[0]}")/lib.sh"

setup() {
    sandbox
    mkdir -p "$IL_ROOT/scripts"
    cp "$TEST_REPO/scripts/compose-init.sh" "$IL_ROOT/scripts/compose-init.sh"
    cp "$TEST_REPO/.env.example" "$IL_ROOT/.env.example"
    for tool in od tr openssl id; do p="$(PATH="$T_REAL_PATH" command -v "$tool" 2>/dev/null || true)"; [[ -n "$p" ]] && ln -sf "$p" "$T_BIN/$tool"; done
    stub docker 'case "$1" in --version) echo "Docker version 29.0.0, build x";; compose) [ "$2" = version ] && { [ "${3:-}" = "--short" ] && echo 2.40.0 || echo "Docker Compose version v2.40.0"; };; info) echo "Runtimes: nvidia runc";; esac; exit 0'
}
meminfo() { printf 'MemTotal:       %s kB\nMemFree:        1000 kB\n' "$1" >"$T_DIR/meminfo"; export PROC_MEMINFO="$T_DIR/meminfo"; }
run() { (cd "$IL_ROOT" && bash scripts/compose-init.sh --no-gpu 2>&1); }

# --- a 32 GB machine (31 GiB of MemTotal) -----------------------------------------------
setup; meminfo 32500000
out="$(run)"; st=$?
assert_status "runs" 0 "$st"
assert_eq "the cap is 24g" "SERVER_MEM_LIMIT=24g" "$(grep '^SERVER_MEM_LIMIT=' "$IL_ROOT/.env")"
assert_contains "it says so" "$out" "SERVER_MEM_LIMIT=24g"
cleanup_sandbox

# --- exactly 40 GiB: still capped -------------------------------------------------------
setup; meminfo 41943040
out="$(run)"
assert_eq "40 GiB is capped" "SERVER_MEM_LIMIT=24g" "$(grep '^SERVER_MEM_LIMIT=' "$IL_ROOT/.env")"
cleanup_sandbox

# --- a 64 GB machine: the default stays -------------------------------------------------
setup; meminfo 65800000
out="$(run)"
want="$(grep '^SERVER_MEM_LIMIT=' "$TEST_REPO/.env.example")"
assert_eq "the default stays" "$want" "$(grep '^SERVER_MEM_LIMIT=' "$IL_ROOT/.env")"
assert_contains "it says it left it" "$out" "left at its default"
cleanup_sandbox

# --- an .env that exists is never changed -----------------------------------------------
setup; meminfo 32500000
cp "$IL_ROOT/.env.example" "$IL_ROOT/.env"; sed -i 's|^SERVER_MEM_LIMIT=.*|SERVER_MEM_LIMIT=40g|' "$IL_ROOT/.env"
out="$(run)"
assert_eq "the existing .env keeps the user's value" "SERVER_MEM_LIMIT=40g" "$(grep '^SERVER_MEM_LIMIT=' "$IL_ROOT/.env")"
cleanup_sandbox

# --- the RAM cannot be read -------------------------------------------------------------
setup; export PROC_MEMINFO="$T_DIR/nothing-here"
out="$(run)"
assert_contains "says it could not read the RAM" "$out" "could not be read"
cleanup_sandbox

# --- VERSION: the release this checkout is, only when it is exactly one --------------------
setup; meminfo 65800000
cp "$TEST_REPO/scripts/version.sh" "$IL_ROOT/scripts/version.sh"
out="$(VERITMO_VERSION=v0.1.0 run)"
assert_eq "an exact release pins the images" "VERSION=0.1.0" "$(grep '^VERSION=' "$IL_ROOT/.env")"
assert_contains "it says so" "$out" "VERSION=0.1.0"
cleanup_sandbox

setup; meminfo 65800000
cp "$TEST_REPO/scripts/version.sh" "$IL_ROOT/scripts/version.sh"
out="$(VERITMO_VERSION=v0.1.0-3-gabc1234 run)"
assert_eq "three commits after a release is not a release" "" "$(grep '^VERSION=' "$IL_ROOT/.env")"
assert_contains "it says it left it" "$out" "VERSION left unset"
cleanup_sandbox

pass_summary
