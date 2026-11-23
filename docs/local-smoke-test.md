# Local smoke test

Runs the full pull/retag/recreate cycle on your laptop without a Canton validator. It uses
`traefik/whoami` (two real tags) as a stand-in, a one-file HTTP server as a fake SV `/info`,
and a fake Slack webhook — so you can watch the mechanics without touching a real node.

Everything is in [`scripts/manual-smoke-test.sh`](../scripts/manual-smoke-test.sh):

```sh
scripts/manual-smoke-test.sh all      # every scenario end to end, then tear down
# or step by step:
scripts/manual-smoke-test.sh up | sv | dryrun | run | verify | idem \
                             | migration | noconsensus | notify \
                             | alert | apply | slackbtn | down
```

Needs Docker running, plus `go` and `python3`. On macOS the script points `DOCKER_HOST` at
`~/.docker/run/docker.sock` when `/var/run/docker.sock` is absent.

## What each step shows

| step | behaviour |
|------|-----------|
| `up` | one-time operator setup: `:auto` = v1.10.1, container running |
| `sv` | fake SV `/info` now advertises the newer v1.10.2 |
| `dryrun` | consensus reached, natural-language log, nothing changed |
| `run` | pull v1.10.2 → retag `:auto` → recreate via the Docker API |
| `verify` | `:auto` now points at v1.10.2; `Config.Image` stays `:auto` |
| `idem` | a second run is a no-op — logs `all containers at v1.10.2` |
| `noconsensus` | two SVs disagree (below the 2/3 threshold) → nothing happens |
| `notify` | a successful update posts a Slack notice with the release-notes link |
| `alert` | `UPDATE_MODE=ai` with an unreachable classifier → fail-safe Slack alert with approve CLI command |
| `apply` | `./agent apply v1.10.2` force-applies bypassing classifier + window |
| `slackbtn` | HMAC-signed curl simulates Slack button click → `ApplyForced` runs |

This covers the update mechanism, not a real validator. Multi-SV consensus, readyz, and pulling
real Canton images (registry auth) are the devnet test's job — see [`devnet-test.md`](devnet-test.md).

## Watch out for

- The smoke test explicitly sets `UPDATE_MODE=always` to skip the classifier sidecar (safety here = SV consensus).
  The shipped `compose.yaml` sets `UPDATE_MODE=ai`; the code default when the variable is unset is `always`. See below for how to run the classifier.
- The agent discovers by image, so any other `traefik/whoami:auto` container on the host gets
  managed and updated too. `up` warns if it finds one; remove it for a clean run.
- Alert dedup (30 min) and 48h auto-approve are in-memory state — they reset on process restart,
  so they are best verified via unit tests (`go test ./internal/agent/... -run TestAlertDedup`).
- `slackbtn` uses `SLACK_SIGNING_SECRET=smoke-test-secret` by default. Override with
  `export SLACK_SIGNING_SECRET=<real-secret>` to test against a real Slack App.

## What this doesn't cover

**Seed-scan discovery** (`SV_SEED_SCAN_URL`) is unit-tested in `internal/release/discover_test.go` but can't run locally — the derived `info.<host>` URLs don't resolve and scan endpoints are IP-allowlisted.

**The AI classifier** (`UPDATE_MODE=ai`) runs as a Python sidecar (a fine-tuned decision model). The example `compose.yaml` starts it alongside the agent:

```sh
# start agent + classifier sidecar (images pulled from GHCR)
docker compose up -d

# run the classifier benchmark against the live sidecar + real release notes
AI_CLASSIFIER_URL=http://localhost:8000 go test ./internal/classify/ -run TestLayaRealNotes -v
```
