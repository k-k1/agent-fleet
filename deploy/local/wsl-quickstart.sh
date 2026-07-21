#!/usr/bin/env bash
# Backward-compat wrapper: startup scripts were unified into run-dev.sh
# (`run-dev.sh wsl` is the real thing). Behavior is unchanged — starts the CP on
# the host as a single user with no auth or tenants (AUTH=dev), and runs
# workspaces in local Docker (AF_RUNTIME=local). rtk is always baked into every
# image build; JDKs come from a shared bind mount (skip with WS_JDK=0).
# Setup steps (installing native dockerd, dependencies): deploy/local/README-wsl.md.
#
# Usage (same as before; env like WS_JDK / WS_SMOKE still applies):
#   deploy/local/wsl-quickstart.sh
#   WS_JDK=0 deploy/local/wsl-quickstart.sh
# To wipe data: deploy/local/run-dev.sh reset [--all]
exec "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/run-dev.sh" wsl "$@"
