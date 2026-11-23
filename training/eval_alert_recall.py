#!/usr/bin/env python3
"""ALERT-recall benchmark: can the model detect operator-action releases the safety-net
rule does NOT cover? Real Splice history is almost all AUTO with a single notes-visible
ALERT (0.7.4), so the real-notes benchmark barely tests ALERT recall. These are
semi-real cases written in Splice release-notes style, covering the operator-action
patterns we actually care about.

Mirrors the production Go path (internal/classify/laya.go): instruction with the SV
qualifier + filterByExcluded + the configKeysRemoved escalation. Reports model-alone and
model+rule decisions so we can see what the model catches vs what only the rule saves.

Run with the fine-tuned sidecar up:
  LAYA_BASE_URL=http://localhost:8003 training/.venv/bin/python training/eval_alert_recall.py
"""
import os
import re
import sys

try:
    import requests
except ImportError:
    sys.exit("requests not installed")

BASE = os.environ.get("LAYA_BASE_URL", "http://localhost:8003").rstrip("/")
ENDPOINT = BASE + "/v1/systemone"

INSTR = (
    "Does this Canton validator release require the operator to take manual action before "
    "upgrading — such as a new required environment variable, a mandatory config change, a "
    "manual pre/post-upgrade step, or a documented workaround without which the node fails? "
    "Answer true only for standard validator deployments, not for Super Validator operators "
    "or optional features."
)
EXCLUDE = ["wallet-gateway", "wallet gateway", "portfolio", "portfolio ui", "wallet gateway ui"]
# Mirrors operatorActionSignals in internal/classify/classify.go.
RULES = [
    re.compile(r"(?i)config(?:uration)?\s+(?:option|key|setting|parameter)s?\b[^.]*?\bremoved\b"),
    re.compile(r"(?i)\bnew\s+(?:required\s+)?(?:environment variable|env var)s?\b"),
    re.compile(r"(?i)\bworkaround\b[^.]*?\bset\b|\buntil the next (?:patch|release)\b"),
]

# (id, notes, want, pattern)
CASES = [
    # --- ALERT: operator must act (patterns the rule does NOT cover) ---
    ("required-env",
     "Upgrade Canton to 3.6.2. This release requires the new environment variable "
     "VALIDATOR_LEDGER_API_TOKEN to be set in your validator .env before upgrading; "
     "validators started without it will fail to boot.", "ALERT", "new required env"),
    ("boot-workaround",
     "Upgrade Canton to 3.6.3. Some validators fail to start after this upgrade due to a "
     "sequencer handshake regression. Workaround: set ADDITIONAL_CONFIG_STARTUP_DELAY=30s in "
     "your validator config until the next patch.", "ALERT", "boot workaround"),
    ("config-migration",
     "The validator participant database connection configuration has changed. Operators must "
     "migrate their db-config block to the new structure in values.yaml before upgrading or the "
     "participant will not start.", "ALERT", "mandatory config migration"),
    ("manual-step",
     "Before upgrading, operators must run the one-time ACS snapshot migration script "
     "scripts/migrate-acs.sh against the participant database; skipping it leaves the store in "
     "an inconsistent state.", "ALERT", "manual pre-upgrade step"),
    ("required-secret",
     "Operators must create a new Kubernetes secret splice-validator-onboarding-secret and "
     "reference it from values.yaml before upgrading; the validator app will crash-loop "
     "without it.", "ALERT", "new required secret"),
    ("config-removed",
     "Upgrade Canton to 3.5.20. The deprecated enable-legacy-ingestion configuration option "
     "has been removed. If you set it, remove it from your config before upgrading.", "ALERT",
     "config removed (rule covers)"),
    # --- AUTO: scary-but-safe distractors ---
    ("breaking-api-auto",
     "Breaking API changes: the public /v0/dso endpoint has been removed. API consumers must "
     "migrate to the scan app endpoint. Validators upgrade normally with no config change.",
     "AUTO", "API-consumer change"),
    ("sv-only-env",
     "To recover devnet, Super Validators must temporarily set ADDITIONAL_CONFIG_VIEW_CHANGE_"
     "TIMEOUT. Standard validator operators need no action.", "AUTO", "SV-only env"),
    ("optional-gateway",
     "The validator deployment can now also deploy the Canton Wallet Gateway via the new -g "
     "flag. This requires LEDGER_API_AUTH_AUDIENCE and VALIDATOR_AUTH_AUDIENCE to be equal. "
     "Standard deployments without this optional feature are unaffected.", "AUTO",
     "optional feature"),
    ("canton-bump",
     "Upgrade Canton to 3.5.21. Bug fixes and performance improvements. No configuration "
     "changes required.", "AUTO", "plain bump"),
]


def ask(notes):
    r = requests.post(ENDPOINT, json={"state": notes, "model": "laya",
                      "questions": {"q": {"type": "noul", "instructions": INSTR}}}, timeout=30)
    r.raise_for_status()
    return r.json()["answers"]["q"]["noul"]


def filt(t):
    return ". ".join(p for p in t.split(". ") if not any(x in p.lower() for x in EXCLUDE))


def main():
    print(f"ALERT-recall benchmark ({ENDPOINT})\n")
    hdr = f"{'case':18s} {'want':6s} {'noul':>5s}  {'model':6s} {'+rule':6s}  pattern"
    print(hdr); print("-" * len(hdr))
    m_ok = mr_ok = m_dang = mr_dang = 0
    for cid, notes, want, pat in CASES:
        p = ask(filt(notes)[:1500])
        model = "ALERT" if p > 0.5 else "AUTO"
        rule = model
        if model == "AUTO" and any(r.search(notes) for r in RULES):
            rule = "ALERT"
        m_ok += model == want
        mr_ok += rule == want
        if want == "ALERT" and model == "AUTO":
            m_dang += 1
        if want == "ALERT" and rule == "AUTO":
            mr_dang += 1
        mmark = "ok" if model == want else ("DANGER" if want == "ALERT" else "FP")
        rmark = "ok" if rule == want else ("DANGER" if want == "ALERT" else "FP")
        print(f"{cid:18s} {want:6s} {p:.2f}  {mmark:6s} {rmark:6s}  {pat}")
    n = len(CASES)
    print("-" * len(hdr))
    print(f"model alone : {m_ok}/{n} correct, {m_dang} dangerous miss(es)")
    print(f"model + rule: {mr_ok}/{n} correct, {mr_dang} dangerous miss(es)")


if __name__ == "__main__":
    main()
