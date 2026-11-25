#!/usr/bin/env bash
# Runs the tests of the shell scripts and the documentation checks.
#   bash scripts/test/run-all.sh
set -u
cd "$(dirname "${BASH_SOURCE[0]}")/../.." || exit 2
fail=0
for t in install_lib_test install_test check_docs_test install_docs_test sync_public_test get_whisper_test get_piper_test get_models_test get_services_test install_services_test music_containers_test check_image_test compose_init_test model3d_licence_test check_family_table_test; do
    printf '%-20s ' "$t"
    bash "scripts/test/$t.sh" 2>&1 | tail -1
    [[ "${PIPESTATUS[0]}" == 0 ]] || fail=1
done
printf '%-20s ' check-docs
bash scripts/check-docs.sh || fail=1
exit "$fail"
