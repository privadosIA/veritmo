#!/usr/bin/env bash
# The message about the Stability AI Community License, shared by scripts/get-services.sh and scripts/install.sh so that both say
# the same thing. Sourced, not run.
#   stability_licence_message "[get-services]"    # prints to stderr, each line starting with the given prefix
STABILITY_LICENCE_ID="stability-community"
stability_licence_message() {
    local p="${1:-}"
    echo "$p Stable Fast 3D is under the Stability AI Community License (https://stability.ai/community-license-agreement)." >&2
    echo "$p   Free for research and non-commercial use, and for commercial use under US\$1M of yearly revenue (register with Stability AI);" >&2
    echo "$p   above that you need a licence from Stability AI. Read it, and if you agree: --accept-licence $STABILITY_LICENCE_ID" >&2
}
