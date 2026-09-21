#!/usr/bin/env bash
#
# Regenerate the guide: docs/index.html plus the screenshots and transcripts it shows.
#
#   1. builds the binary (the shots are of the thing that ships, never a dev server)
#   2. starts a first-run daemon on an empty config dir, and the demo daemon with its
#      fake providers and replayed traffic
#   3. runs capture.py against both, then build_index.py
#
# This folder is `docs/` rather than `guide/` because GitHub Pages serves it from here; the
# same files are embedded into the binary and served by the daemon at /guide/.
#
# One-time: pip3 install playwright && python3 -m playwright install chromium
set -euo pipefail

DOCS="$(cd "$(dirname "$0")" && pwd)"
REPO="$(cd "$DOCS/.." && pwd)"
FIRSTRUN_PORT=8891
DEMO_PORT=8892

cd "$REPO"
echo "[regenerate] make build"
make build >/dev/null

pids=()
cleanup() { for p in "${pids[@]:-}"; do kill "$p" 2>/dev/null || true; done; pkill -f "ferrule-demo -port $DEMO_PORT" 2>/dev/null || true; rm -rf "${FIRSTRUN_DIR:-}"; }
trap cleanup EXIT

wait_for() { for _ in $(seq 1 60); do curl -sf -o /dev/null "$1" && return 0; sleep 1; done; echo "[regenerate] $1 never came up" >&2; return 1; }

FIRSTRUN_DIR=$(mktemp -d)
# Default bind, as a person would run it, with the documentation address handed out in
# place of this machine's. --host 127.0.0.1 would show the "bound to this computer" card
# instead of the household one, which is not the first run anybody has.
echo "[regenerate] first-run daemon on :$FIRSTRUN_PORT ($FIRSTRUN_DIR)"
FERRULE_CONFIG_DIR="$FIRSTRUN_DIR" ./ferrule serve --port $FIRSTRUN_PORT --no-detect --advertise 192.0.2.42 \
  > /tmp/ferrule-guide-firstrun.log 2>&1 &
pids+=($!)

echo "[regenerate] demo daemon on :$DEMO_PORT"
go run ./cmd/ferrule-demo -port $DEMO_PORT > /tmp/ferrule-guide-demo.log 2>&1 &
pids+=($!)

wait_for "http://127.0.0.1:$FIRSTRUN_PORT/"
wait_for "http://127.0.0.1:$DEMO_PORT/"
# The demo prints its config dir and a household key once the replay has finished.
for _ in $(seq 1 60); do grep -q OPENAI_API_KEY /tmp/ferrule-guide-demo.log && break; sleep 1; done
DEMO_DIR=$(sed -n 's/^config: //p' /tmp/ferrule-guide-demo.log)
DEMO_KEY=$(sed -n 's/.*OPENAI_API_KEY=//p' /tmp/ferrule-guide-demo.log)
[ -n "$DEMO_DIR" ] && [ -n "$DEMO_KEY" ] || { echo "[regenerate] demo did not print its config dir and key:"; tail -5 /tmp/ferrule-guide-demo.log; exit 1; }

echo "[regenerate] capture"
FERRULE_BIN="$REPO/ferrule" FIRSTRUN_URL="http://127.0.0.1:$FIRSTRUN_PORT" DEMO_URL="http://127.0.0.1:$DEMO_PORT" \
  DEMO_DIR="$DEMO_DIR" DEMO_KEY="$DEMO_KEY" python3 "$DOCS/capture.py"

echo "[regenerate] build"
python3 "$DOCS/build_index.py"
echo "[regenerate] open file://$DOCS/index.html"
