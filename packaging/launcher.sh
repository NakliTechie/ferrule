#!/bin/bash
# Ferrule.app's whole implementation. The bundle is a wrapper around a verb that stands on
# its own: `ferrule open` starts the daemon if nothing is listening, waits for the port,
# and shows the panel. Keeping the logic in the binary means the bundle and a terminal run
# the same code, and that code is testable.
#
# The binary lives in Resources, not beside this script. macOS filesystems are
# case-insensitive by default, so `Ferrule` and `ferrule` in one directory are one file —
# copying the binary next to the launcher silently overwrote it and produced a bundle that
# launched nothing.
set -euo pipefail
here="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
exec "$here/../Resources/ferrule" open "$@"
