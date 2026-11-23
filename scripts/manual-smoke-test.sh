#!/usr/bin/env bash
#
# Manual smoke test for validator-update-agent on a real Docker engine — no Canton
# validator needed. Exercises the current agent end to end:
#   seed-scan discovery → 2/3 SV consensus → :auto seed → pull/retag/recreate,
#   plus idempotency, no-consensus, the UPDATE_MODE=ai fail-safe alert, and CLI apply.
#
# How the stand-in works: a throwaway local registry holds traefik/whoami retagged as
# a managed image (validator-app:v1.10.1/v1.10.2). A one-file HTTP server mocks the SV
# seed scan (/api/scan/v0/scans → scan.localhost) and the derived /info endpoint
# (info.localhost — both resolve to loopback). A second server mocks a disagreeing SV,
# and a third captures Slack webhook posts.
#
# Run step by step and watch the output:
#   scripts/manual-smoke-test.sh up           # 1.  registry + image + container :auto=v1.10.1 + fake SV
#   scripts/manual-smoke-test.sh dryrun        # 2.  observe only (seeds :auto, applies nothing)
#   scripts/manual-smoke-test.sh run           # 3.  real update → v1.10.2
#   scripts/manual-smoke-test.sh verify        # 4.  :auto moved, container on v1.10.2
#   scripts/manual-smoke-test.sh idem          # 5.  run again → up-to-date, no recreate
#   scripts/manual-smoke-test.sh noconsensus   # 6.  2 SVs disagree → do nothing
#   scripts/manual-smoke-test.sh alert         # 7.  UPDATE_MODE=ai, classifier unreachable → Slack alert
#   scripts/manual-smoke-test.sh apply         # 8.  CLI apply → force-apply v1.10.2
#   scripts/manual-smoke-test.sh down          # 9.  tear everything down
#
#   scripts/manual-smoke-test.sh all           # run 1-9 in sequence
set -euo pipefail

WORK=/tmp/agentsmoke
REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
REG_PORT=5099
REG=127.0.0.1:${REG_PORT}
IMG=${REG}/validator-app
OLD_VER=v1.10.1
NEW_VER=v1.10.2
WHOAMI_OLD=traefik/whoami:${OLD_VER}
WHOAMI_NEW=traefik/whoami:${NEW_VER}
SV_PORT=8099
SV2_PORT=8100
HOOK_PORT=8102

# Docker Desktop on macOS puts the socket under ~/.docker/run, not /var/run.
if [ -z "${DOCKER_HOST:-}" ] && [ ! -S /var/run/docker.sock ] && [ -S "$HOME/.docker/run/docker.sock" ]; then
	export DOCKER_HOST="unix://$HOME/.docker/run/docker.sock"
fi

# Baseline agent env: one seed scan, long poll so a run is a single tick before it sleeps.
export SV_SEED_SCAN_URL="http://localhost:${SV_PORT}"
export POLL_INTERVAL=99h

say()  { printf '\n\033[1;36m== %s\033[0m\n' "$*"; }
note() { printf '\033[0;33m%s\033[0m\n' "$*"; }

# run_agent LOGFILE [agent args…] — run the built agent for AGENT_SECS, then stop it.
# Extra env is taken from the caller's exported environment.
run_agent() {
	local log="$1"; shift
	( cd "$WORK" && ./agent "$@" ) >"$log" 2>&1 &
	local pid=$!
	sleep "${AGENT_SECS:-12}"
	kill "$pid" 2>/dev/null || true
	wait "$pid" 2>/dev/null || true
	cat "$log"
}

write_sv_script() {
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
        self.send_response(200); self.send_header('Content-Type', 'application/json'); self.end_headers()
        self.wfile.write(data)
    def log_message(self, *a): pass
http.server.HTTPServer(('0.0.0.0', PORT), H).serve_forever()
PY
}

write_scans() { # $@ = SV ports to advertise
	local entries="" port
	for port in "$@"; do
		[ -n "$entries" ] && entries="$entries,"
		entries="$entries{\"publicUrl\":\"http://scan.localhost:${port}\",\"svName\":\"sv${port}\"}"
	done
	printf '{"scans":[{"scans":[%s]}]}\n' "$entries" > "$WORK/scans.json"
}

step_up() {
	say "1. stand-in stack — local registry, validator-app:auto = ${OLD_VER}, fake SV serving ${NEW_VER}"
	mkdir -p "$WORK"
	( cd "$REPO" && go build -o "$WORK/agent" ./cmd/agent )
	note "DOCKER_HOST=${DOCKER_HOST:-<default /var/run/docker.sock>}"

	docker rm -f agentsmoke-registry >/dev/null 2>&1 || true
	docker run -d -p "127.0.0.1:${REG_PORT}:5000" --name agentsmoke-registry registry:2 >/dev/null
	sleep 1
	docker pull "$WHOAMI_OLD" >/dev/null
	docker pull "$WHOAMI_NEW" >/dev/null
	docker tag "$WHOAMI_OLD" "${IMG}:${OLD_VER}" && docker push "${IMG}:${OLD_VER}" >/dev/null
	docker tag "$WHOAMI_NEW" "${IMG}:${NEW_VER}" && docker push "${IMG}:${NEW_VER}" >/dev/null
	docker tag "${IMG}:${OLD_VER}" "${IMG}:auto"  # local :auto, seeds the "current" version

	cat > "$WORK/compose.yaml" <<YAML
name: agentsmoke
services:
  validator-app:
    image: ${IMG}:auto
YAML
	docker compose -f "$WORK/compose.yaml" up -d >/dev/null
	docker compose -f "$WORK/compose.yaml" ps --format '{{.Name}} {{.Image}}'

	write_sv_script
	write_scans "$SV_PORT"
	printf '{ "deploymentInfo": { "sv": { "version": "%s" } } }\n' "$NEW_VER" > "$WORK/info.json"
	lsof -ti ":${SV_PORT}" 2>/dev/null | xargs kill -9 2>/dev/null || true
	( python3 "$WORK/sv.py" "$SV_PORT" "$WORK" >"$WORK/sv.log" 2>&1 & echo $! >"$WORK/sv.pid" )
	sleep 1
	note "SV scans: $(curl -s "http://localhost:${SV_PORT}/api/scan/v0/scans")"
}

step_dryrun() {
	say "2. dry run — observe only, nothing changes"
	AGENT_SECS=8 run_agent "$WORK/dry.log" --dry-run >/dev/null
	grep -iE "consensus|updating|dry-run" "$WORK/dry.log" || true
	note "expect: 'core: ${NEW_VER} — updating 1 container(s)' then '[dry-run] would pull, retag :auto, recreate'"
}

step_run() {
	say "3. real run — pull ${NEW_VER}, retag :auto, recreate"
	run_agent "$WORK/run.log" >/dev/null
	grep -iE "updating|updated|error|fail" "$WORK/run.log" || true
	note "expect: 'validator-app updated to ${NEW_VER}'"
}

step_verify() {
	say "4. verify — :auto moved, container runs ${NEW_VER}"
	local auto new cid
	auto=$(docker image inspect "${IMG}:auto" --format '{{.Id}}')
	new=$(docker image inspect "${IMG}:${NEW_VER}" --format '{{.Id}}')
	cid=$(docker compose -f "$WORK/compose.yaml" ps -q validator-app)
	echo "  :auto image id    = $auto"
	echo "  ${NEW_VER} image id = $new"
	docker inspect "$cid" --format '  container Config.Image={{.Config.Image}}  running={{.Image}}'
	if [ "$auto" = "$new" ]; then note "PASS — :auto now points at ${NEW_VER}"; else note "FAIL — :auto did not move"; fi
}

step_idem() {
	say "5. idempotency — run again, should be up-to-date, no recreate"
	AGENT_SECS=8 run_agent "$WORK/idem.log" >/dev/null
	grep -iE "all containers at|updating" "$WORK/idem.log" || true
	note "expect: 'core: all containers at ${NEW_VER}' — no 'updating'"
}

step_noconsensus() {
	say "6. no consensus — 2 SVs disagree (below 2/3 threshold) → do nothing"
	mkdir -p "$WORK/sv2"
	printf '{ "deploymentInfo": { "sv": { "version": "v9.9.9" } } }\n' > "$WORK/sv2/info.json"
	lsof -ti ":${SV2_PORT}" 2>/dev/null | xargs kill -9 2>/dev/null || true
	( python3 "$WORK/sv.py" "$SV2_PORT" "$WORK/sv2" >"$WORK/sv2.log" 2>&1 & echo $! >"$WORK/sv2.pid" )
	sleep 1
	write_scans "$SV_PORT" "$SV2_PORT"   # seed now advertises both SVs
	AGENT_SECS=10 run_agent "$WORK/noc.log" >/dev/null
	grep -iE "no 2/3 consensus|waiting for consensus|updating" "$WORK/noc.log" || true
	kill "$(cat "$WORK/sv2.pid")" 2>/dev/null || true
	write_scans "$SV_PORT"               # restore single-SV consensus for later steps
	note "expect: a 'no 2/3 consensus' skip — no 'updating' (1 of 2 agree < 2/3)"
}

step_alert() {
	say "7. UPDATE_MODE=ai with an unreachable classifier → fail-safe Slack alert"
	docker tag "${IMG}:${OLD_VER}" "${IMG}:auto"
	docker compose -f "$WORK/compose.yaml" up -d --force-recreate >/dev/null  # back to ${OLD_VER} so an update is pending
	lsof -ti ":${HOOK_PORT}" 2>/dev/null | xargs kill -9 2>/dev/null || true
	cat > "$WORK/hook.py" <<'PY'
import http.server, sys
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        n = int(self.headers.get('content-length', 0))
        sys.stderr.write("WEBHOOK " + self.rfile.read(n).decode() + "\n"); sys.stderr.flush()
        self.send_response(200); self.end_headers()
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(sys.argv[1])), H).serve_forever()
PY
	( python3 "$WORK/hook.py" "$HOOK_PORT" >"$WORK/hook.log" 2>&1 & echo $! >"$WORK/hook.pid" )
	sleep 1
	UPDATE_MODE=ai AI_CLASSIFIER_URL="http://127.0.0.1:1" SLACK_WEBHOOK_URL="http://localhost:${HOOK_PORT}/" \
		AGENT_SECS=10 run_agent "$WORK/alert.log" >/dev/null
	kill "$(cat "$WORK/hook.pid")" 2>/dev/null || true
	grep -iE "classifier error|defaulting to alert" "$WORK/alert.log" || true
	note "webhook received:"; grep -a "WEBHOOK" "$WORK/hook.log" || note "(none — check alert.log)"
	note "expect: alert text contains 'attention' + 'apply ${NEW_VER}'"
}

step_apply() {
	say "8. CLI apply — force-apply ${NEW_VER} (bypasses classifier + rollout window)"
	docker tag "${IMG}:${OLD_VER}" "${IMG}:auto"
	docker compose -f "$WORK/compose.yaml" up -d --force-recreate >/dev/null
	note "running: ./agent apply ${NEW_VER}"
	( cd "$WORK" && ./agent apply "$NEW_VER" ) && note "PASS — exit 0" || note "FAIL — non-zero exit"
	step_verify
}

step_down() {
	say "9. teardown"
	for p in sv sv2 hook; do
		[ -f "$WORK/$p.pid" ] && kill "$(cat "$WORK/$p.pid")" 2>/dev/null || true
	done
	lsof -ti ":${SV_PORT}" ":${SV2_PORT}" ":${HOOK_PORT}" 2>/dev/null | xargs kill -9 2>/dev/null || true
	[ -f "$WORK/compose.yaml" ] && docker compose -f "$WORK/compose.yaml" down >/dev/null 2>&1 || true
	docker rm -f agentsmoke-registry >/dev/null 2>&1 || true
	docker rmi -f "${IMG}:${OLD_VER}" "${IMG}:${NEW_VER}" "${IMG}:auto" >/dev/null 2>&1 || true
	rm -rf "$WORK"
	note "cleaned $WORK + stand-in registry/images"
}

case "${1:-}" in
	up)          step_up ;;
	dryrun)      step_dryrun ;;
	run)         step_run ;;
	verify)      step_verify ;;
	idem)        step_idem ;;
	noconsensus) step_noconsensus ;;
	alert)       step_alert ;;
	apply)       step_apply ;;
	down)        step_down ;;
	all)
		step_up; step_dryrun; step_run; step_verify; step_idem
		step_noconsensus; step_alert; step_apply; step_down
		say "ALL DONE" ;;
	*)
		grep '^#' "$0" | sed 's/^# \{0,1\}//'
		exit 1 ;;
esac
