# Running on devnet

This runs the agent against a real devnet validator with real SV consensus: poll → agree on a version → pull → retag `:auto` → recreate via the Docker API → validator ready. It also covers the safety branches — no consensus, classifier alert, pull failure — so you see the full picture before going to MainNet.

## Prerequisites

- A devnet validator stack running under `docker compose` (containers carry `com.docker.compose.*` labels). The agent reads its scan URL from this container, so no `SV_SEED_SCAN_URL` is needed.
- Docker and registry access for `ghcr.io/digital-asset/...` images.
- ~2 GB free RAM for the AI classifier sidecar (starts automatically with the stack when `UPDATE_MODE=ai`).

## Step 0 — Point your validator config at `:auto` (strongly recommended)

The agent seeds a local `:auto` tag on its first run — the dry-run in Step 2 does this. Because `:auto` is a local tag, switch the ref only *after* that first run; switching before makes compose try to pull `:auto` from the registry and fail with `manifest unknown`. Strongly recommended: otherwise any `docker compose up -d` re-applies your pinned (old) version — a downgrade that can corrupt the database once a newer release has migrated it.

```sh
# after the agent's first run has created the :auto tag:
IMAGE_TAG=auto
docker compose up -d
```

The agent auto-discovers which containers are running — no component list needed.

## Step 1 — Deploy the agent

Use the published image — no build needed. Copy the compose file and bring it up; no `.env` is required (the scan URL is auto-detected). Add a `.env` only for `SLACK_WEBHOOK_URL` or to override the scan URL.

```sh
cp examples/compose.yaml compose.yaml
docker compose up -d   # pulls the agent + AI classifier images from GHCR
```

On its first cycle the agent seeds a local `:auto` tag from each currently running container. From that point on it applies eligible updates automatically and always recreates containers with the `:auto` image reference, so `docker compose up -d` stays aligned with whatever version the agent last applied.

## Step 2 — Dry run first

Run the agent once with `--dry-run`. You should see SV polling, `consensus reached version=<X>`, container discovery, and `[dry-run] would pull ... recreate` for anything behind X. Nothing changes.

```sh
docker compose run --rm agent --dry-run
```

## Step 3 — Real update

Restart normally (without `--dry-run`). On the next cycle the agent applies any pending update, recreates the containers in order (participant → validator → UIs), and waits for each container's HEALTHCHECK to pass before continuing.

## Step 4 — Verify

```sh
docker ps --format '{{.Names}}\t{{.Image}}'
docker image inspect --format '{{.Id}}' <repo>/validator-app:auto   # :auto → new version ID
```

Then re-run `docker compose up -d` on the validator and confirm `docker ps` still shows the updated version — Compose resolves `:auto` to what the agent last set, so there is no version drift on restart.

## Step 5 — Safety branches

- **No consensus** — if the SVs disagree or too few respond, the agent logs "no consensus" and skips the cycle.
- **Classifier alert** (`UPDATE_MODE=ai`) — when the classifier flags an operator action (or cannot classify), the agent alerts instead of updating.
- **Failure path** — if a pull or recreate fails, the agent stops at that step and alerts. It does **not** roll back.
- **Rollout window** — set `ROLLOUT_WINDOW=HH:MM-HH:MM` (in `ROLLOUT_WINDOW_TZ`, default UTC) to restrict when updates apply; outside the window the agent defers and logs why.
- **Slack** — set `SLACK_WEBHOOK_URL` to get alerts delivered; without it they appear as `[ALERT]` log lines.

## Out of scope here

Kubernetes/Helm engine, multi-validator-per-host isolation. See the README for details.
