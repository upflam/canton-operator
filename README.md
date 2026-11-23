# Canton Operator

> A lightweight agent that keeps a Canton validator's containers aligned with the version reported by Super Validator consensus, without relying on a central version authority.

## Why

A Canton validator runs several containers: the participant, validator app, and web UIs. These are usually managed through the operator's existing `docker compose` setup.

When a new version is released, operators need to pull the new images and recreate the affected containers. Doing this manually for every release is repetitive and can leave nodes running different versions.

`validator-update-agent` automates that process without introducing a central version authority. It polls the Super Validators and selects a target version only when at least two-thirds of the configured SV set report the same version.

## How it works

On each cycle, the agent:

1. **Gets the version from SV consensus.** Each Super Validator exposes an `/info` endpoint that reports the version it is running. The agent discovers the SV set from `SV_SEED_SCAN_URL` and selects a version when **≥ 2/3 of the SVs agree**.

2. **Checks the release notes (when `UPDATE_MODE=ai`).** An AI classifier (fine-tuned decision model, Python sidecar) reads the release notes and checks for required operator actions, such as a new environment variable, config change, or manual migration step; a deterministic rule backs it for the clear-cut must-act signals. If operator action is required, or classification fails, the agent alerts instead of updating. With `UPDATE_MODE=always` the agent skips this check and applies every consensus version.

3. **Applies the update.** If the release can be updated automatically and the current time is within the configured rollout window, the agent pulls the target images and recreates the containers through the Docker API while keeping the existing container configuration. If an update fails, the cycle stops and the operator is notified through Slack. There is no automatic rollback.

```text
one cycle
  poll SV /info  ──►  < 2/3 agree ............... do nothing
                      outside rollout window .... defer
                      classifier says alert ..... alert
                      otherwise ................. pull → retag :auto → recreate
                                                  (participant → validator → rest)
```

The agent auto-discovers which Canton/Splice containers are running on the host — no component list to configure. It manages any of the following when present:

| Group | Components |
| --- | --- |
| core | `canton-participant`, `validator-app`, `wallet-web-ui`, `ans-web-ui` |
| wallet-gateway | `wallet-gateway` |
| portfolio | `splice-portfolio` |

If the host runs more than one validator (several Compose projects sharing the same images), the agent discovers and updates every one of them, each from the image it is actually running. This assumes all validators on the host track the same SV set, and therefore the same version; running validators that must stay on different versions on a single host is not supported, because the shared local `:auto` tag can only point at one image.

The current prototype supports Docker only. The rollout code uses a `rollout.Engine` interface (`-mode`) so a Kubernetes/Helm implementation can be added later.

## Quickstart

Setup is one-time. After that the agent runs on its own.

### 1. Point your validator config at the `:auto` tag (strongly recommended)

The agent manages a local `:auto` tag per image and seeds it automatically: on its first poll it discovers your running containers and tags each one's current image as `:auto`. Because `:auto` is a local tag (**not** on any registry), the order matters:

1. **Deploy the agent first** (step 2) and let it run one cycle — it creates the local `:auto` tags.
2. **Then** switch your validator `.env` / compose to `:auto` and re-up:

```sh
IMAGE_TAG=auto                 # WALLET_GATEWAY_VERSION / PORTFOLIO_VERSION too, if you run them
docker compose up -d
```

**Why this matters:** the agent recreates containers directly, but if your config still pins an old version, any `docker compose up -d` you run (even for an unrelated reason) recreates the validator at that pinned version — a downgrade. Running an older release against a database a newer release already migrated can corrupt it. Pointing the tag at `:auto` means your `docker compose up` always uses the version the agent manages, so an accidental run is safe. Switch the ref to `:auto` only *after* the agent has created the local tag — doing it first makes compose try to pull `:auto` from the registry and fail with `manifest unknown`.

### 2. Deploy the agent

Run the agent as a separate Compose project next to the validator:

```sh
cp examples/compose.yaml .
docker compose up -d
```

No configuration is required: the agent auto-detects its seed scan URL from the running validator container. Create a `.env` (see `examples/.env.example`) only to set `SLACK_WEBHOOK_URL`/`ROLLOUT_WINDOW` or to override `SV_SEED_SCAN_URL`.

The agent needs access to the Docker daemon. You can mount the Docker socket directly, although a socket proxy with a minimal allowlist is preferable in production. The image runs as nonroot and cannot read a raw mounted socket, so the example sets `user: "0:0"`; a socket proxy lets you keep the agent nonroot.

Before enabling real updates, you can run the agent with `--dry-run` first:

```sh
docker compose run --rm agent --dry-run
```

### 3. Done

The agent polls the SVs, seeds a local `:auto` tag from each currently running container on its first cycle, and from that point on applies eligible updates automatically. When it recreates a container it always sets the image reference to `:auto`, so any future `docker compose up -d` stays aligned with whatever version the agent last applied.

Slack notifications are sent for failed updates, classifier alerts, and successful updates when `SLACK_WEBHOOK_URL` is configured.

### Approve from Slack

When a release is classified as needing a human review, the agent alerts instead of applying automatically. Set `SLACK_APP_TOKEN` to attach an **Approve** button to that alert: clicking it applies the flagged version (bypassing the classifier and the rollout window). This uses Slack Socket Mode, so no inbound port is exposed. If no one approves within 48 hours, the agent applies the release on the next cycle whose time falls within the rollout window (unlike the button, the 48-hour fallback still respects `ROLLOUT_WINDOW`). Without `SLACK_APP_TOKEN`, the alert is informational only.

On `SIGINT` or `SIGTERM`, the agent finishes the current cycle and then exits.

## Configuration

Configuration is provided through environment variables. None are required — every value has a default or is auto-detected.

| Variable | Default | Purpose |
| --- | --- | --- |
| `SV_SEED_SCAN_URL` | auto-detected | Discovers the SV set from one seed scan (`<seed>/api/scan/v0/scans`, `scan.` → `info.`). When unset, it is read from the running validator container's `SPLICE_APP_VALIDATOR_SCAN_ADDRESS`; the agent waits for that container if it is not up yet. Set it to override. |
| `SLACK_WEBHOOK_URL` | — | Slack webhook for alerts and update notifications. Empty means log only. |
| `SLACK_APP_TOKEN` | — | Slack App-Level Token (`xapp-…`, scope `connections:write`). Enables Socket Mode so alert notifications carry an **Approve** button; no inbound port required. Empty means notifications only. |
| `ROLLOUT_WINDOW` | — | Daily update window in `HH:MM-HH:MM` format, interpreted in `ROLLOUT_WINDOW_TZ`. Empty means updates can run at any time. Windows may cross midnight. |
| `ROLLOUT_WINDOW_TZ` | `UTC` | IANA timezone for `ROLLOUT_WINDOW` (e.g. `America/New_York`). Independent of the container's clock, so set it to your local zone to avoid reading the window in UTC by mistake. The resolved window is logged at startup. |
| `POLL_INTERVAL` | `5m` | Poll interval as a Go duration (`5m`, `10m`, `1h`). Minimum 5m; shorter values are clamped unless running with `--dry-run`. |

`-mode=docker|kubernetes` selects the rollout engine. By default, the agent auto-detects the mode.

Only Docker is currently implemented. Selecting `kubernetes` returns an error instead of falling back to Docker.

`--dry-run` logs intended actions without changing containers. Useful before first deployment.

## Non-core components

SV consensus governs the core four components. The remaining components are resolved from their own sources:

- **wallet-gateway** and **portfolio** (`splice-portfolio`) — version taken from their registry's latest tag.

All components auto-update when a newer version is found. The agent detects which of these containers are running and manages only those — no configuration needed.

## Laya classifier

Laya is a Python sidecar that reads the release notes before an update and answers one question:

> Does this release require the operator to take action before or during the upgrade?

The classifier looks for things such as required environment variables, config changes, and manual migration steps.

It does not treat every "breaking change" as requiring operator action. Protocol changes, for example, may be breaking without requiring any manual changes on the validator.

Laya is a fine-tuned decision model (`ameerhmz5/laya-modernbert-decision-90pct`). It runs as a separate container (`splice-laya`) defined in `compose.yaml` and communicates with the agent over HTTP.

A deterministic safety-net rule backs the model for the high-confidence must-act signals the notes state outright — a removed config key, a new required environment variable, or a documented boot workaround. The rule can only escalate auto → alert, never the reverse, so it cannot make an unsafe update slip through.

If the classifier cannot determine that an update is safe to apply automatically, it returns an alert.

For auto-applied releases, the classifier also produces a headline summary from the release notes for the Slack notification.

One limitation is that required operator steps are sometimes announced on Slack but not included in the release notes. The classifier cannot detect information that is not present in the release notes it reads.

> **`UPDATE_MODE`** (`always` | `ai`) controls how updates are gated. `always` applies every consensus version without inspecting release notes; `ai` lets the classifier read the notes and alert when the operator must act first. The shipped `compose.yaml` sets `UPDATE_MODE=ai` (and starts the sidecar); the agent binary's own default when the variable is unset is `always`. To run without the classifier, set `UPDATE_MODE=always` and `--scale laya=0`. The classifier endpoint is configured with `AI_CLASSIFIER_URL` (and optional `AI_CLASSIFIER_API_KEY`).

## Trust model

The target validator version is selected from SV consensus rather than from a central release authority.

There are two main checks:

- **SV consensus (≥ 2/3)** determines the target version.
- **Release-notes classifier** (`UPDATE_MODE=ai`) alerts instead of updating when operator action may be required.

There is no automatic rollback. If an update fails, the agent stops the current cycle and alerts the operator.

The **SV list** is an important part of the trust model. If an attacker can control the list, they can influence which version the agent considers to have consensus.

The list is discovered through `SV_SEED_SCAN_URL`. Operators should verify the seed scan endpoint used by the agent.

The **agent image** is a separate trust boundary. In production, pin the image by digest and, where possible, verify a signed image with a tool such as cosign.

SV consensus determines which validator version to run; image verification protects the agent that performs the update.

## Build & test

```sh
make build    # small static binary (or: go build ./cmd/agent)
make test     # go test ./... — no Docker/network required
make docker   # build agent image (~9 MB)
```

Unit tests cover consensus handling, update ordering, classifier behavior, and rollout windows using a fake rollout engine. Docker and network access are not required for the test suite.

Project layout:

```text
cmd/agent
internal/
  release
  classify
  rollout
  rollout/docker
  rollout/kube
  window
  notify
  dockerx
  agent
```

To test the update flow against a real Docker engine without running a validator, see [`docs/local-smoke-test.md`](docs/local-smoke-test.md).

For a devnet test, see [`docs/devnet-test.md`](docs/devnet-test.md).

## Known limitations

- **Kubernetes/Helm is not supported yet.** The rollout interface exists, but the current implementation is Docker-only. `-mode=kubernetes` returns an error.

- **The AI classifier can produce extra alerts.** When it cannot determine that an update is safe, it alerts instead of applying the update. Set `UPDATE_MODE=always` to disable this check and apply every consensus version.

- **The 2/3 threshold is based on SV count, not voting weight.**

- **`:auto` is a local Docker tag.** `docker compose pull` and `pull_policy: always` cannot resolve it from a remote registry. The agent detects this configuration and warns the operator.

---

> **Status:** prototype — Docker-only, unit-tested, end-to-end verified on MainNet devnet.
