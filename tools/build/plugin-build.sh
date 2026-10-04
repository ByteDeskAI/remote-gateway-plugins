#!/usr/bin/env bash
# Shared plugin build runner: see plugin-build.py and docs/CONTRACT.md.
# Vendored from bytedesk-remote-gateway/.teamcity/scripts/plugin-build-v1.sh.
set -euo pipefail
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Plugins may require private ByteDeskAI Go modules. Rewrite the org HTTPS
# prefix to carry the agent's read token through GIT_CONFIG_* variables, so the
# token stays in this process and never lands in a shared ~/.gitconfig.
token="${BYTEDESK_GIT_TOKEN:-${GITHUB_TOKEN:-}}"
if [[ -n "$token" ]]; then
  export GOPRIVATE="${GOPRIVATE:-github.com/ByteDeskAI/*}"
  export GONOSUMDB="${GONOSUMDB:-$GOPRIVATE}"
  export GIT_TERMINAL_PROMPT=0
  export GIT_CONFIG_COUNT=1
  export GIT_CONFIG_KEY_0="url.https://x-access-token:${token}@github.com/ByteDeskAI/.insteadOf"
  export GIT_CONFIG_VALUE_0="https://github.com/ByteDeskAI/"
  printf 'plugin-build: private ByteDeskAI Go modules authenticated\n'
else
  printf 'plugin-build: no BYTEDESK_GIT_TOKEN; private ByteDeskAI modules resolve only from the local module cache\n'
fi
unset token
exec python3 "$SCRIPT_DIR/plugin-build.py" "$@"
