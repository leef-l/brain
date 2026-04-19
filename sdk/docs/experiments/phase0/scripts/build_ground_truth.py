#!/usr/bin/env python3
"""Phase 0 ground truth builder — 3-model × 3-run cross voting.

Replaces single-LLM drafting with multi-model consensus:
- Each element is labeled 9 times (3 models × 3 runs)
- For each of the 4 dimensions (action_intent / reversibility / risk_level / flow_role)
  we take the modal vote:
    - 5+/9 agree on one value  → adopt as ground_truth
    - tied or fragmented (<5)  → mark that dimension as 'ambiguous'
  action_intent uses sentence-embedding-free clustering: exact-match majority,
  fallback to first in tie.
- Any element whose risk_level OR reversibility comes out 'ambiguous' is
  flagged for exclusion from the experiment accuracy denominator.

Output format (labels/<page_id>.ground_truth.json):
{
  "page_id": "...",
  "url": "...",
  "elements": [
    {
      "id": 17,
      "element": { tag/role/name/bbox/... },
      "ground_truth": {
        "action_intent": "...",            // or null if ambiguous
        "reversibility": "reversible",     // or "ambiguous"
        "risk_level": "safe",
        "flow_role": "primary",
        "consensus": {                     // audit trail
          "reversibility": {"reversible": 7, "semi_reversible": 2},
          "risk_level":    {"safe": 9},
          ...
        },
        "included_in_experiment": true     // false if any critical dim is ambiguous
      },
      "votes": [ ...raw 9 votes... ]
    }
  ]
}
"""

import json
import os
import re
import sys
import time
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SAMPLES_DIR = ROOT / "samples"
LABELS_DIR = ROOT / "labels"
GT_DIR = ROOT / "labels"  # overwrite same dir, different filenames
LABELS_DIR.mkdir(exist_ok=True)

RUNS_PER_MODEL = 3
MAX_PER_PAGE = 6

ENUM_VALUES = {
    "reversibility": {"reversible", "semi_reversible", "irreversible", "conditional"},
    "risk_level":    {"safe", "safe_caution", "destructive", "external_effect"},
    "flow_role":     {"primary", "secondary", "escape", "navigation",
                      "cross_page_nav", "utility"},
}

SYSTEM = """You are labeling web UI interactive elements for a research dataset.
For each element, output JSON with EXACTLY these four fields:

- action_intent: one short sentence (<=20 words). What does the user try to do by interacting?
- reversibility: one of {reversible, semi_reversible, irreversible, conditional}
- risk_level: one of {safe, safe_caution, destructive, external_effect}
- flow_role: one of {primary, secondary, escape, navigation, cross_page_nav, utility}

Definitions (use these literally):
- reversibility:
  * reversible        = no persistent side-effect (search, filter, expand, toggle)
  * semi_reversible   = side-effect but recoverable (add-to-cart, save draft, bookmark)
  * irreversible      = persistent, non-undoable (place order, send message, delete)
  * conditional       = depends on next step (e.g. multi-step checkout with a confirm)
- risk_level:
  * safe              = no side-effect or fully local
  * safe_caution      = small-scope side-effect (submit form, login attempt, cookie prefs)
  * destructive       = delete/clear/pay/send-unrecallable
  * external_effect   = affects outside this page (sends email/SMS, calls 3rd-party API)
- flow_role:
  * primary           = main path action (the "Submit" / "Log In" button)
  * secondary         = alternative (e.g. "Log in with Google")
  * escape            = cancel/back/forgot-password
  * navigation        = in-page tab/anchor/state toggle
  * cross_page_nav    = leaves current page to another
  * utility           = helper (help, settings, language, search-filter)

Output ONLY a JSON array. One object per element, same order as input.
Do not add explanations outside the JSON.
"""


def load_providers():
    cfg = json.loads((Path.home() / ".brain/config.json").read_text(encoding="utf-8"))
    providers = cfg.get("providers", {})
    out = []
    for pid in ("squarefaceicon", "tencent", "deepseek"):
        p = providers.get(pid)
        if not p:
            continue
        out.append({
            "id": pid,
            "api_key": p.get("api_key"),
            "base_url": p.get("base_url") or p.get("endpoint"),
            "model": p.get("model"),
            "protocol": p.get("protocol", "anthropic"),
        })
    return out


def call_anthropic(provider, user_prompt):
    import anthropic
    client = anthropic.Anthropic(
        api_key=provider["api_key"],
        base_url=provider["base_url"],
    )
    resp = client.messages.create(
        model=provider["model"],
        max_tokens=4096,
        system=SYSTEM,
        messages=[{"role": "user", "content": user_prompt}],
    )
    return resp.content[0].text


def call_openai(provider, user_prompt):
    from openai import OpenAI
    client = OpenAI(api_key=provider["api_key"], base_url=provider["base_url"])
    resp = client.chat.completions.create(
        model=provider["model"],
        max_tokens=4096,
        messages=[
            {"role": "system", "content": SYSTEM},
            {"role": "user", "content": user_prompt},
        ],
    )
    return resp.choices[0].message.content


def extract_json_array(raw):
    s = raw.strip()
    if s.startswith("```"):
        s = re.sub(r"^```\w*\n", "", s)
        s = s.rstrip("`").rstrip().rstrip("`")
    # Find first [ and last ]
    start = s.find("[")
    end = s.rfind("]")
    if start == -1 or end == -1 or end < start:
        raise ValueError(f"no JSON array in response: {s[:200]}")
    return json.loads(s[start:end + 1])


def pick_elements(snapshot):
    if not snapshot:
        return []

    def score(el):
        role = (el.get("role") or el.get("tag") or "").lower()
        t = (el.get("type") or "").lower()
        name = (el.get("name") or "").lower()
        s = 0
        if t == "password" or "password" in name:
            s += 100
        if any(k in name for k in ("submit", "sign in", "log in", "login",
                                    "register", "buy", "add to cart", "add to bag",
                                    "search", "continue", "pay")):
            s += 90
        if role == "button":
            s += 50
        if el.get("tag") == "input" and t in ("text", "email", "search", "number", "tel"):
            s += 40
        if el.get("tag") == "a" and el.get("href"):
            s += 30
        if role in ("tab", "menuitem"):
            s += 20
        if not el.get("inViewport"):
            s -= 15
        return s

    ranked = sorted(snapshot, key=score, reverse=True)
    picked, role_cnt = [], {}
    for el in ranked:
        role = el.get("role") or el.get("tag") or ""
        if role_cnt.get(role, 0) >= 3:
            continue
        role_cnt[role] = role_cnt.get(role, 0) + 1
        picked.append(el)
        if len(picked) >= MAX_PER_PAGE:
            break
    return picked


def build_user_prompt(page_meta, elements, snapshot):
    def near(el):
        near_list = []
        ex, ey = el.get("x", 0), el.get("y", 0)
        cands = [(((o.get("x", 0) - ex) ** 2 + (o.get("y", 0) - ey) ** 2), o)
                 for o in snapshot if o.get("id") != el.get("id")]
        cands.sort(key=lambda t: t[0])
        return [f"  [{o.get('id')}] {o.get('role')} {o.get('name','')!r}"
                for _, o in cands[:5]]

    blocks = []
    for el in elements:
        target = (
            f"TARGET id={el.get('id')} tag={el.get('tag')} role={el.get('role')} "
            f"type={el.get('type')} name={el.get('name')!r} href={el.get('href')}"
        )
        blocks.append(target + "\nNEARBY:\n" + "\n".join(near(el)))
    return (
        f"Page: {page_meta.get('url')}\nCategory: {page_meta.get('category')}\n\n"
        "Elements to label (same order in the output array):\n\n"
        + "\n---\n".join(blocks)
    )


def call_once(provider, user_prompt):
    try:
        if provider["protocol"] == "openai":
            raw = call_openai(provider, user_prompt)
        else:
            raw = call_anthropic(provider, user_prompt)
        return extract_json_array(raw)
    except Exception as e:
        return {"__error__": repr(e)}


def vote_enum(values, enum_set):
    """Return (majority_value or 'ambiguous', counter_dict). Majority = 5+/9."""
    clean = [v for v in values if v in enum_set]
    if not clean:
        return "ambiguous", {}
    c = Counter(clean)
    top, count = c.most_common(1)[0]
    if count >= 5:
        return top, dict(c)
    return "ambiguous", dict(c)


def vote_intent(values):
    """Majority vote on normalized intent strings; fallback to representative."""
    if not values:
        return None, {}
    norm = [re.sub(r"\s+", " ", v.strip().lower().rstrip(".")) for v in values if v]
    c = Counter(norm)
    top, count = c.most_common(1)[0]
    # Find the original (non-lowered) representative for the top cluster
    for orig in values:
        if re.sub(r"\s+", " ", orig.strip().lower().rstrip(".")) == top:
            rep = orig.strip()
            break
    else:
        rep = values[0]
    return rep, {t: n for t, n in c.most_common(3)}


def process_page(providers, page_id):
    page_dir = SAMPLES_DIR / page_id
    meta = json.loads((page_dir / "_meta.json").read_text(encoding="utf-8"))
    snapshot = json.loads((page_dir / "snapshot.json").read_text(encoding="utf-8"))
    elements = pick_elements(snapshot)
    if not elements:
        return {"page_id": page_id, "url": meta.get("url"), "elements": [], "note": "no elements"}

    user_prompt = build_user_prompt(meta, elements, snapshot)

    # Collect 9 rounds of votes (3 providers × 3 runs)
    all_votes = [[] for _ in elements]  # per element → list of dicts
    for pv in providers:
        for run in range(RUNS_PER_MODEL):
            result = call_once(pv, user_prompt)
            if isinstance(result, dict) and "__error__" in result:
                print(f"    {pv['id']} run{run}: error {result['__error__'][:120]}")
                time.sleep(2)
                continue
            if not isinstance(result, list):
                print(f"    {pv['id']} run{run}: not a list")
                continue
            for idx, item in enumerate(result[:len(elements)]):
                if not isinstance(item, dict):
                    continue
                all_votes[idx].append({
                    "provider": pv["id"],
                    "model": pv["model"],
                    "run": run,
                    **{k: item.get(k) for k in ("action_intent", "reversibility",
                                                  "risk_level", "flow_role")},
                })
            time.sleep(1)

    out_elements = []
    for el, votes in zip(elements, all_votes):
        intents = [v.get("action_intent") for v in votes if v.get("action_intent")]
        rev_vals = [v.get("reversibility") for v in votes]
        risk_vals = [v.get("risk_level") for v in votes]
        role_vals = [v.get("flow_role") for v in votes]

        intent_value, intent_counts = vote_intent(intents)
        rev_value, rev_counts = vote_enum(rev_vals, ENUM_VALUES["reversibility"])
        risk_value, risk_counts = vote_enum(risk_vals, ENUM_VALUES["risk_level"])
        role_value, role_counts = vote_enum(role_vals, ENUM_VALUES["flow_role"])

        # critical dimensions — if ambiguous we exclude from experiment denominator
        included = (rev_value != "ambiguous" and risk_value != "ambiguous")

        out_elements.append({
            "id": el.get("id"),
            "element": {
                "tag": el.get("tag"), "role": el.get("role"), "type": el.get("type"),
                "name": el.get("name"), "href": el.get("href"),
                "bbox": {"x": el.get("x"), "y": el.get("y"),
                         "w": el.get("w"), "h": el.get("h")},
            },
            "ground_truth": {
                "action_intent": intent_value,
                "reversibility": rev_value,
                "risk_level": risk_value,
                "flow_role": role_value,
                "consensus": {
                    "action_intent": intent_counts,
                    "reversibility": rev_counts,
                    "risk_level": risk_counts,
                    "flow_role": role_counts,
                },
                "included_in_experiment": included,
                "vote_count": len(votes),
            },
            "votes": votes,
        })

    return {
        "page_id": page_id,
        "url": meta.get("url"),
        "category": meta.get("category"),
        "built_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "providers": [p["id"] for p in providers],
        "runs_per_model": RUNS_PER_MODEL,
        "elements": out_elements,
    }


def main():
    providers = load_providers()
    print(f"Loaded {len(providers)} providers:")
    for p in providers:
        print(f"  - {p['id']:15} model={p['model']} protocol={p['protocol']}")

    if not providers:
        print("ERROR: no providers", file=sys.stderr); sys.exit(2)

    only = sys.argv[1] if len(sys.argv) > 1 else None
    pages = sorted(d.name for d in SAMPLES_DIR.iterdir()
                   if d.is_dir() and not d.name.startswith("_"))
    if only:
        pages = [p for p in pages if p == only]

    totals = {"elements": 0, "included": 0, "excluded": 0}
    for page_id in pages:
        print(f"\n=== {page_id} ===")
        out_path = LABELS_DIR / f"{page_id}.ground_truth.json"
        if out_path.exists():
            print(f"  skip (already built): {out_path.name}")
            prev = json.loads(out_path.read_text(encoding="utf-8"))
            for el in prev.get("elements", []):
                totals["elements"] += 1
                if el["ground_truth"]["included_in_experiment"]:
                    totals["included"] += 1
                else:
                    totals["excluded"] += 1
            continue
        try:
            result = process_page(providers, page_id)
        except Exception as e:
            print(f"  ERROR: {e!r}")
            continue
        out_path.write_text(json.dumps(result, ensure_ascii=False, indent=2),
                            encoding="utf-8")
        els = result.get("elements", [])
        inc = sum(1 for e in els if e["ground_truth"]["included_in_experiment"])
        totals["elements"] += len(els)
        totals["included"] += inc
        totals["excluded"] += (len(els) - inc)
        print(f"  {len(els)} elements labeled, {inc} included, {len(els)-inc} excluded")

    print(f"\n{'='*40}")
    print(f"TOTAL: {totals['elements']} elements, "
          f"{totals['included']} included, {totals['excluded']} excluded/ambiguous")


if __name__ == "__main__":
    main()
