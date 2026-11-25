#!/usr/bin/env bash
# Checks the running stack from the outside, with the token in .env, and says what is wrong: the first start with nothing typed in a screen.
#
#   bash deploy/compose/check.sh
#
# It looks at the pieces that are up (the profiles you started) and checks that the server already knows them by their address in the environment
# (`source: env`), that the tests of the Monitoring card answer ok, that the music generators and Ollama are in use, and that nothing was edited by hand
# in Settings. Exit 0 when everything that is up is in order, 1 otherwise. Needs curl and `docker compose`.
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "$ROOT"
[[ -f .env ]] || { echo "check: no .env here (run bash scripts/compose-init.sh first)" >&2; exit 2; }
value() { grep -E "^$1=" .env | tail -1 | cut -d= -f2-; }
TOKEN="$(value VERITMO_TOKEN)"
BASE="http://127.0.0.1:$(value SERVER_PORT)"
[[ "$BASE" == *: ]] && BASE="${BASE}4666"
[[ -n "$TOKEN" ]] || { echo "check: VERITMO_TOKEN is empty in .env" >&2; exit 2; }

fails=0
pass() { printf '  ok    %s\n' "$1"; }
bad()  { printf '  FAIL  %s\n' "$1"; fails=$((fails + 1)); }
skip() { printf '  --    %s\n' "$1"; }
call() { curl -s -m 90 -H "Authorization: Bearer $TOKEN" "$@"; }
running() { docker compose ps --services --status running 2>/dev/null | grep -qx "$1"; }
has() { grep -q "$1" <<<"$2"; }

echo "the server ($BASE)"
code="$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE/api/health")"
[[ "$code" == 200 ]] && pass "GET /api/health answers 200" || bad "GET /api/health answered $code"
code="$(curl -s -o /dev/null -w '%{http_code}' -m 10 "$BASE/api/projects")"
[[ "$code" == 401 ]] && pass "a call without the token is refused (401)" || bad "a call without the token answered $code, not 401"
env_json="$(call "$BASE/api/environment")"
has '"content_mode":"censored"' "$env_json" && pass "the content mode is the default one (censored)" || bad "the content mode is not the default: $(grep -o '"content_mode":"[a-z]*"' <<<"$env_json")"

echo "the Monitoring card (addresses from the environment)"
mon="$(call "$BASE/api/settings/monitoring")"
for tool in grafana prometheus loki redis; do
    case "$tool" in
        grafana|prometheus) up=yes ;;
        *) running "$tool" && up=yes || up=no ;;
    esac
    if [[ "$up" == no ]]; then skip "$tool: its profile is not up"; continue; fi
    if ! grep -o "{[^{}]*\"id\":\"$tool\"[^{}]*}" <<<"$mon" | grep -q '"source":"env"'; then bad "$tool: its address does not come from the environment"; continue; fi
    res="$(call -X POST "$BASE/api/settings/monitoring/$tool/test")"
    has '"ok":true' "$res" && pass "$tool: address from the environment and its test answers ok" || bad "$tool: the test did not answer ok: $res"
done

echo "the music generators"
if running heartmula || running diffrhythm2; then
    eng="$(call "$BASE/api/music/engines")"
    for e in heartmula diffrhythm2; do
        running "$e" || { skip "$e: not up"; continue; }
        grep -o "\"$e\":{[^{}]*}" <<<"$eng" | grep -q '"available":true' && pass "$e is listed and available" || bad "$e is not available: $(grep -o "\"$e\":{[^{}]*}" <<<"$eng")"
    done
else
    skip "the music profile is not up"
fi

echo "Ollama"
settings="$(call "$BASE/api/settings")"
if running ollama; then
    grep -o '"ollama":{[^{}]*}' <<<"$settings" | grep -q '"source":"env"' && pass "the address of Ollama comes from the environment" || bad "the address of Ollama does not come from the environment"
    has '"ok":true' "$(call -X POST "$BASE/api/settings/ollama/test")" && pass "the test of Ollama answers ok" || bad "the test of Ollama did not answer ok"
else
    skip "the local-llm profile is not up"
fi


echo "Settings"
for k in ollama heartmula diffrhythm2; do
    grep -o "\"$k\":{[^{}]*}" <<<"$settings" | grep -q '"source":"setting"' && bad "$k was edited by hand (its source is a saved setting)"
done
[[ $fails -eq 0 ]] && pass "nothing was edited by hand"

echo
if [[ $fails -eq 0 ]]; then echo "everything that is up is in order"; exit 0; fi
echo "$fails check(s) failed"; exit 1
