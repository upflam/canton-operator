#!/usr/bin/env bash
#
# Live Slack Socket Mode approval test.
#
# Runs the agent against a stand-in stack with an unreachable classifier so it posts
# an "Approve upgrade" button to Slack; clicking it should apply v1.10.2.
#
# Prerequisites:
#   SLACK_APP_TOKEN  — xapp-1-... (Slack App > Basic Information > App-Level Tokens,
#                      scope: connections:write)
#   SLACK_WEBHOOK_URL — incoming webhook URL (for the alert + button message)
#   SV_SEED_SCAN_URL  — optional; defaults to local fake SV (port 8099)
#
# Usage:
#   export SLACK_APP_TOKEN=xapp-1-...
#   export SLACK_WEBHOOK_URL=https://hooks.slack.com/services/...
#   scripts/slack-live-test.sh
set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
WORK=/tmp/agentslacktest
REG_PORT=5006
REG=127.0.0.1:${REG_PORT}
WHOAMI_OLD=traefik/whoami:v1.10.1
WHOAMI_NEW=traefik/whoami:v1.10.2
OLD=${REG}/validator-app:v1.10.1
NEW=${REG}/validator-app:v1.10.2
SV_PORT=8098

# ── Validate required env ──────────────────────────────────────────────────
if [ -z "${SLACK_APP_TOKEN:-}" ]; then
  echo "ERROR: SLACK_APP_TOKEN is not set." >&2
  echo "  Get one from: Slack App -> Basic Information -> App-Level Tokens (scope: connections:write)" >&2
  exit 1
fi
if [ -z "${SLACK_WEBHOOK_URL:-}" ]; then
  echo "ERROR: SLACK_WEBHOOK_URL is not set." >&2
  exit 1
fi

# ── Docker socket ──────────────────────────────────────────────────────────
if [ -z "${DOCKER_HOST:-}" ] && [ ! -S /var/run/docker.sock ] && [ -S "$HOME/.docker/run/docker.sock" ]; then
  export DOCKER_HOST="unix://$HOME/.docker/run/docker.sock"
fi

say()    { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
note()   { printf '\033[0;33m%s\033[0m\n' "$*"; }
ok()     { printf '\033[0;32m%s\033[0m\n' "$*"; }

cleanup() {
  say "teardown"
  kill "$SV_PID" 2>/dev/null || true
  docker compose -f "$WORK/compose.yaml" down --remove-orphans 2>/dev/null || true
  docker rm -f slacklivetest-registry 2>/dev/null || true
  docker rmi "${REG}/validator-app:auto" "${REG}/validator-app:v1.10.1" "${REG}/validator-app:v1.10.2" 2>/dev/null || true
  note "cleaned up"
}
trap cleanup EXIT

mkdir -p "$WORK"

# ── 1. Build agent ─────────────────────────────────────────────────────────
say "1. build agent"
( cd "$REPO" && go build -o "$WORK/agent" ./cmd/agent )
ok "built -> $WORK/agent"

# ── 2. Local registry + images ────────────────────────────────────────────
say "2. local registry + stand-in images"
docker rm -f slacklivetest-registry 2>/dev/null || true
docker run -d -p "127.0.0.1:${REG_PORT}:5000" --name slacklivetest-registry registry:2 >/dev/null
sleep 1
docker pull "$WHOAMI_OLD" >/dev/null
docker pull "$WHOAMI_NEW" >/dev/null
docker tag "$WHOAMI_OLD" "$OLD" && docker push "$OLD" >/dev/null
docker tag "$WHOAMI_NEW" "$NEW" && docker push "$NEW" >/dev/null
docker tag "$OLD" "${REG}/validator-app:auto" && docker push "${REG}/validator-app:auto" >/dev/null

# ── 3. Start validator-app stack ──────────────────────────────────────────
say "3. start validator-app:auto = v1.10.1"
cat > "$WORK/compose.yaml" <<YAML
name: slacklivetest
services:
  validator-app:
    image: ${REG}/validator-app:auto
YAML
docker compose -f "$WORK/compose.yaml" up -d
docker compose -f "$WORK/compose.yaml" ps

# ── 4. Fake SV serving v1.10.2 ────────────────────────────────────────────
say "4. fake SV advertising v1.10.2"
cat > "$WORK/sv.py" <<'PY'
import http.server, os, sys
PORT, WORK = int(sys.argv[1]), sys.argv[2]
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        f = 'scans.json' if self.path == '/api/scan/v0/scans' else 'info.json'
        try:
            data = open(os.path.join(WORK, f)).read().encode()
        except FileNotFoundError:
            self.send_response(404); self.end_headers(); return
        self.send_response(200); self.send_header('Content-Type','application/json'); self.end_headers()
        self.wfile.write(data)
    def log_message(self, *a): pass
http.server.HTTPServer(('0.0.0.0', PORT), H).serve_forever()
PY
cat > "$WORK/scans.json" <<JSON
{"scans":[{"scans":[{"publicUrl":"http://scan.localhost:${SV_PORT}","svName":"sv1"}]}]}
JSON
cat > "$WORK/info.json" <<'JSON'
{ "deploymentInfo": { "sv": { "version": "v1.10.2" } } }
JSON
lsof -ti ":${SV_PORT}" | xargs kill -9 2>/dev/null || true
python3 "$WORK/sv.py" "$SV_PORT" "$WORK" &
SV_PID=$!
sleep 1
ok "SV running on :${SV_PORT}, serving v1.10.2"

# ── 5. Run agent ──────────────────────────────────────────────────────────
say "5. starting agent (UPDATE_MODE=ai, classifier unreachable → fail-safe alert + Socket Mode)"
note "Agent will send an alert to Slack with an 'Approve upgrade' button."
note "Click the button in Slack — agent will apply v1.10.2 automatically."
note ""
note "Press Ctrl+C to stop."
note ""

(
  cd "$WORK"
  SV_SEED_SCAN_URL="http://localhost:${SV_PORT}" \
  UPDATE_MODE=ai \
  AI_CLASSIFIER_URL=http://127.0.0.1:1 \
  SLACK_APP_TOKEN="${SLACK_APP_TOKEN}" \
  SLACK_WEBHOOK_URL="${SLACK_WEBHOOK_URL}" \
  POLL_INTERVAL=5m \
  ./agent
)
