#!/usr/bin/env python3
"""Compare Laya (local) vs the known Qwen LoRA results on the same two test sets.

Usage:
  # Start Unsloth Desktop, enable Decision API under Settings → API, then:
  python3 training/eval_laya.py

  # If Unsloth runs on a different host or port:
  LAYA_BASE_URL=http://192.168.1.10:8888 python3 training/eval_laya.py

  # If keyless access is disabled and you set an API key:
  LAYA_API_KEY=sk-unsloth-xxx python3 training/eval_laya.py

Requires requests:  pip install requests

Reports per-set accuracy and dangerous misses (ALERT labelled AUTO),
then prints a side-by-side comparison table vs the Qwen LoRA baseline.

Qwen LoRA baseline (last retrain, 2026-10):
  synthetic-test  118/120  1 dangerous miss
  curated-real     16/16   0 dangerous miss
"""

import json
import os
import re
import sys

try:
    import requests
except ImportError:
    sys.exit("requests not installed — run: pip install requests")

BASE_URL = os.environ.get("LAYA_BASE_URL", "http://localhost:8000")
API_KEY  = os.environ.get("LAYA_API_KEY", "")
ENDPOINT = BASE_URL.rstrip("/") + "/v1/systemone"

INSTRUCTION = (
    "Does this Canton validator release require the operator to take manual action "
    "before upgrading — such as a new required environment variable, a mandatory "
    "config change, a manual pre/post-upgrade step, or a documented workaround "
    "without which the node fails?"
)

# Laya (ModernBERT-large) has 512–1024 token context; keep notes short.
MAX_CHARS = 1500

# Same curated set as eval.py — real-shaped Canton release notes.
CURATED = [
    ("0.8.4", "Upgrade Canton to 3.5.19.", "AUTO"),
    ("0.8.3", "Upgrade Canton to 3.5.12, update the Docker base image, fix a Scan proxy bug.", "AUTO"),
    ("0.6.9", "All Helm charts now support overriding full image names using new optional values. All web UI Docker images updated to the nginx-unprivileged alpine-slim base image.", "AUTO"),
    ("0.8.0", "Summary of breaking changes: breaking API changes, protocol changes, Daml model changes: yes. Scan App will no longer return featured app rights in the choice context; the SVs will vote to make this effective after the release.", "AUTO"),
    ("0.7.5", "The client IP for per-client-IP HTTP rate limiting is now extracted from a configurable, ordered list of headers, rate-limiting.client-ip-headers, which defaults to [x-forwarded-for, x-real-ip].", "AUTO"),
    ("0.7.3", "Upgrade Canton to fix a CantonBFT issue surfaced on devnet. To recover devnet, Supervalidators must temporarily set the environment variable ADDITIONAL_CONFIG_VIEW_CHANGE_TIMEOUT. Remove it again after devnet is recovered.", "AUTO"),
    ("0.7.0", "PostgreSQL 18 is now officially supported. Warning: PostgreSQL 14 will reach end of life on November 12, 2026; you should upgrade before that date. Remove the deprecated /transactions endpoint.", "AUTO"),
    ("0.6.6", "The following deprecated endpoints have been removed from the public API: /v0/top-validators-by-validator-faucets. Switch docker base images to reduce attack surface.", "AUTO"),
    ("0.6.1", "Canton upgrade to Canton 3.5. Note that unlike the upgrade to 3.4 this is still just applied like any other upgrade.", "AUTO"),
    ("0.6.10", "Added support for secretOverrides for Helm charts. To use this, you must have the corresponding mutating webhook installed in your cluster.", "AUTO"),
    # Optional-component warnings — must stay AUTO for standard validator operators.
    ("0.9.0", "Some optional components display a startup warning on this release. No action is required for standard validator setups. The warning will be resolved in a future patch.", "AUTO"),
    ("0.9.1", "Warning: operators using the optional DarSyncer integration must update their DarSyncer configuration before upgrading; the core validator is unaffected.", "AUTO"),
    ("0.8.9", "Note: if you have enabled the optional scan aggregator sidecar, restart it after upgrade. The core validator does not require it and is unaffected.", "AUTO"),
    ("0.7.4", "The enable-app-activity-record-and-traffic-ingestion and serve-app-activity-records-and-traffic configuration options have been removed. If you set them, remove them from your config before upgrading or startup fails.", "ALERT"),
    ("0.5.14", "Some validators fail to boot after upgrade. Workaround: set the environment variable EXTRA_ENABLE=true until the next patch.", "ALERT"),
    ("boot-env", "Operators must set the new required environment variable VALIDATOR_TOKEN before upgrading, or the validator will not start.", "ALERT"),
]


def ask_laya(notes: str) -> float:
    """Return noul probability [0-1]. Raises on error."""
    if len(notes) > MAX_CHARS:
        notes = notes[:MAX_CHARS] + " …(truncated)"
    headers = {"Content-Type": "application/json"}
    if API_KEY:
        headers["Authorization"] = f"Bearer {API_KEY}"
    resp = requests.post(
        ENDPOINT,
        json={
            "state": notes,
            "model": "laya",
            "questions": {
                "requires_manual_action": {"type": "noul", "instructions": INSTRUCTION}
            },
        },
        headers=headers,
        timeout=10,
    )
    resp.raise_for_status()
    return resp.json()["answers"]["requires_manual_action"]["noul"]


# Mirrors operatorActionSignals in internal/classify/classify.go — model-agnostic
# safety-net escalation applied on the notes text.
_RULES = [
    re.compile(r"(?i)config(?:uration)?\s+(?:option|key|setting|parameter)s?\b[^.]*?\bremoved\b"),
    re.compile(r"(?i)\bnew\s+(?:required\s+)?(?:environment variable|env var)s?\b"),
    re.compile(r"(?i)\bworkaround\b[^.]*?\bset\b|\buntil the next (?:patch|release)\b"),
]


def classify_laya(notes: str) -> str:
    """Fail-safe: any error → ALERT. Applies the production safety-net rule."""
    try:
        decision = "ALERT" if ask_laya(notes) > 0.5 else "AUTO"
    except Exception as e:
        print(f"  inference error (fail-safe → ALERT): {e}", flush=True)
        return "ALERT"
    if decision == "AUTO" and any(r.search(notes) for r in _RULES):
        return "ALERT"
    return decision


def extract_notes_from_prompt(prompt: str) -> str:
    m = re.search(r'"""\n(.*?)\n"""', prompt, re.DOTALL)
    return m.group(1).strip() if m else prompt


def score_curated() -> tuple[int, int, int]:
    correct = dangerous = total = 0
    for version, notes, want in CURATED:
        got = classify_laya(notes)
        total += 1
        ok = got == want
        correct += ok
        if want == "ALERT" and got == "AUTO":
            dangerous += 1
        status = "ok  " if ok else "MISS"
        print(f"  {status} {version}: want={want} got={got}")
    return correct, dangerous, total


def score_synthetic() -> tuple[int, int, int]:
    path = os.path.join(os.path.dirname(__file__), "data", "test.jsonl")
    correct = dangerous = total = 0
    for line in open(path):
        r = json.loads(line)
        notes = extract_notes_from_prompt(r["messages"][0]["content"])
        want = r["messages"][1]["content"].strip()
        got = classify_laya(notes)
        total += 1
        correct += got == want
        if want == "ALERT" and got == "AUTO":
            dangerous += 1
    return correct, dangerous, total


def check_laya_running():
    try:
        requests.get(BASE_URL, timeout=3)
    except Exception:
        sys.exit(
            f"Cannot reach Laya at {BASE_URL}\n"
            "Start Unsloth Desktop and enable Decision API under Settings → API."
        )


def main():
    check_laya_running()
    print(f"== Laya eval ({ENDPOINT}) ==\n")

    print("--- curated-real ---")
    c_correct, c_danger, c_total = score_curated()
    print(f"curated-real: {c_correct}/{c_total} correct, {c_danger} dangerous miss(es)\n")

    print("--- synthetic-test ---")
    s_correct, s_danger, s_total = score_synthetic()
    print(f"synthetic-test: {s_correct}/{s_total} correct, {s_danger} dangerous miss(es)\n")

    print("=" * 54)
    print(f"{'Set':<18} {'Qwen LoRA':>16} {'Laya':>16}")
    print("-" * 54)
    print(f"{'curated-real':<18} {'16/16  0 dm':>16} {f'{c_correct}/{c_total}  {c_danger} dm':>16}")
    print(f"{'synthetic-test':<18} {'118/120  1 dm':>16} {f'{s_correct}/{s_total}  {s_danger} dm':>16}")
    print("=" * 54)
    print("dm = dangerous misses (ALERT labelled AUTO — the key safety metric)")

    if c_danger == 0 and s_danger <= 1:
        print("\nVerdict: Laya matches or beats Qwen on dangerous misses → viable alternative")
    else:
        print("\nVerdict: Laya has more dangerous misses than Qwen → keep Qwen for now")


if __name__ == "__main__":
    main()
