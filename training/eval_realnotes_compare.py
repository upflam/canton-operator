#!/usr/bin/env python3
"""Real-notes benchmark for the two classifiers never tested on real notes:
Laya fine-tuned (ameerhmz5/laya-modernbert-decision-90pct) and Strands Decider 2B.

Fetches the SAME 16 live release-note sections the Go TestLLM_RealNotes_Classification
benchmark uses, so the numbers are apples-to-apples with Qwen (15/16) and Laya
zero-shot (4/16).

Run: training/.venv/bin/python training/eval_realnotes_compare.py
"""
import re
import sys
import urllib.request

NOTES_URL = "https://docs.canton.network/global-synchronizer/release-notes/splice"

# Same 16 cases + labels as internal/classify/realnotes_test.go.
CASES = [
    ("0.9.0", "AUTO"), ("0.8.4", "AUTO"), ("0.8.3", "AUTO"), ("0.7.1", "AUTO"),
    ("0.6.14", "AUTO"), ("0.6.12", "AUTO"), ("0.6.9", "AUTO"), ("0.8.0", "AUTO"),
    ("0.7.5", "AUTO"), ("0.7.4", "ALERT"), ("0.7.3", "AUTO"), ("0.7.0", "AUTO"),
    ("0.6.6", "AUTO"), ("0.6.2", "AUTO"), ("0.6.1", "AUTO"), ("0.6.10", "AUTO"),
]

# Mirrors internal/classify/laya.go: single manual-action question + core exclude terms.
LAYA_INSTRUCTION = (
    "Does this Canton validator release require the operator to take manual action before "
    "upgrading — such as a new required environment variable, a mandatory config change, a "
    "manual pre/post-upgrade step, or a documented workaround without which the node fails? "
    "Answer true only for standard validator deployments, not for Super Validator operators "
    "or optional features."
)
CORE_EXCLUDE = ["wallet-gateway", "wallet gateway", "portfolio", "portfolio ui", "wallet gateway ui"]
MAX_CHARS = 1500

STRANDS_NOUL = (
    "Does this release require the validator operator to take manual action before upgrading "
    "— such as setting a new required environment variable, deleting a removed config key, or "
    "following a documented workaround? Answer YES only for standard validator deployments, not "
    "for Super Validator operators or optional features."
)
STRANDS_CHOICE_INSTR = (
    "Classify this Canton validator release from the validator operator's perspective. AUTO if "
    "the operator can upgrade with no manual action. ALERT if the operator must act before or "
    "during the upgrade (set a required env var, delete a removed config key, run a manual step, "
    "or follow a documented workaround)."
)
STRANDS_CRITERIA = {
    "AUTO": ("No operator action required. Includes dependency bumps, bug fixes, optional features, "
             "breaking API/protocol/Daml changes the app handles automatically, and Super Validator "
             "changes the validator just follows."),
    "ALERT": ("Operator must act: a new required env var, a removed config key the operator had set, a "
              "mandatory config migration, a manual pre/post-upgrade step, or a documented workaround "
              "without which the node fails to start. SV-only requirements and optional-feature env "
              "vars do NOT count."),
}


def fetch_section(version: str) -> str:
    with urllib.request.urlopen(NOTES_URL, timeout=20) as r:
        html = r.read().decode()
    vid = version.replace(".", "-")
    marker = f'id="{vid}"'
    start = html.find(marker)
    if start < 0:
        return ""
    m = re.search(r'id="\d+-\d+[^"]*"', html[start + len(marker):])
    end = start + len(marker) + (m.start() if m else len(html) - start - len(marker))
    text = re.sub(r"<[^>]+>", " ", html[start:end])
    text = re.sub(r"[​ ]+", " ", text)
    return re.sub(r"\s+", " ", text).strip()


def filter_excluded(text: str) -> str:
    parts = text.split(". ")
    kept = [p for p in parts if not any(t in p.lower() for t in CORE_EXCLUDE)]
    return ". ".join(kept)


def score(name, notes_map, classify_fn):
    correct = dangerous = 0
    print(f"\n{'='*64}\n{name}\n{'='*64}")
    for ver, want in CASES:
        got = classify_fn(notes_map[ver])
        ok = got == want
        correct += ok
        if want == "ALERT" and got == "AUTO":
            dangerous += 1
        mark = "ok  " if ok else ("DANGER" if want == "ALERT" else "FP")
        print(f"  {mark:6} {ver:7} want={want:5} got={got}")
    print(f"  --> {correct}/{len(CASES)} correct, {dangerous} dangerous miss(es)")
    return correct, dangerous


def main():
    print("fetching 16 real release-note sections…", flush=True)
    notes_map = {ver: fetch_section(ver) for ver, _ in CASES}

    # ---- Laya fine-tuned ----
    import laya
    agent = laya.load("ameerhmz5/laya-modernbert-decision-90pct")

    def laya_classify(notes):
        notes = filter_excluded(notes)[:MAX_CHARS]
        res = agent.system_one(notes, {"q": {"type": "noul", "instructions": LAYA_INSTRUCTION}})
        p = res["answers"]["q"]["noul"]
        return "ALERT" if p > 0.5 else "AUTO"

    score("Laya FINE-TUNED (ameerhmz5/laya-modernbert-decision-90pct) — real notes", notes_map, laya_classify)

    # ---- Strands Decider 2B ----
    import platform
    from strands_decider.infer import load_engine
    from strands_decider.schema import NoulQuestion, ChoiceQuestion

    device = "mps" if platform.system() == "Darwin" else "cuda"
    engine = load_engine("StrandsAgents/strands-decider-2B-hobson-v19", device=device)
    noul_q = NoulQuestion(instructions=STRANDS_NOUL)
    choice_q = ChoiceQuestion(instructions=STRANDS_CHOICE_INSTR, criteria=STRANDS_CRITERIA)

    def strands_classify(notes):
        resp = engine.ask(notes[:MAX_CHARS], {"choice": choice_q})
        return resp.answers["choice"].choice.upper()

    score("Strands Decider 2B (choice) — real notes", notes_map, strands_classify)


if __name__ == "__main__":
    main()
