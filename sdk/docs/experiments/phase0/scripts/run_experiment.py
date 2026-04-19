#!/usr/bin/env python3
"""Phase 0 main experiment — measure LLM L6-L8 prediction accuracy.

For each element that is `included_in_experiment`:
  For each input combo in {A: snapshot, B: snapshot+screenshot, C: snapshot+html}:
    For each model in {opus, haiku_like, mid}:
      Predict 3 times → record predictions

Results go to results/{combo}_{model}_{page}_{element}.json (append-only).
analyze.py consumes these and compares against ground_truth.

Cost discipline:
- A (snapshot-only)          ~ 400 tokens
- B (snapshot + screenshot)  ~ 4000 tokens
- C (snapshot + HTML chunk)  ~ 2500 tokens

With ~54 elements × 3 combos × 3 models × 3 runs ≈ 1460 calls.
Single element per call isolates bias that batch-labeling introduces.
"""

import base64
import json
import os
import re
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SAMPLES_DIR = ROOT / "samples"
LABELS_DIR = ROOT / "labels"
RESULTS_DIR = ROOT / "results"
RESULTS_DIR.mkdir(exist_ok=True)

RUNS_PER_MODEL = 3
HTML_CHUNK_LINES = 200
MAX_SNAPSHOT_NEIGHBORS = 8
HTML_MAX_CHARS = 12_000

ENUM_VALUES = {
    "reversibility": {"reversible", "semi_reversible", "irreversible", "conditional"},
    "risk_level":    {"safe", "safe_caution", "destructive", "external_effect"},
    "flow_role":     {"primary", "secondary", "escape", "navigation",
                      "cross_page_nav", "utility"},
}

SYSTEM = """You are labeling ONE web UI element. Output JSON with EXACTLY these fields:

- action_intent   (one short sentence, <=20 words)
- reversibility   one of {reversible, semi_reversible, irreversible, conditional}
- risk_level      one of {safe, safe_caution, destructive, external_effect}
- flow_role       one of {primary, secondary, escape, navigation, cross_page_nav, utility}
- confidence      0.0 to 1.0 (honest; do not always say 0.9)

Definitions:
- reversibility: reversible=no persistent side-effect, semi_reversible=recoverable,
  irreversible=persistent/non-undoable, conditional=depends on next step
- risk_level: safe=local/none, safe_caution=small side-effect,
  destructive=delete/pay/send-unrecallable, external_effect=emails/SMS/3rd-party
- flow_role: primary=main action, secondary=alternative, escape=cancel/forgot,
  navigation=in-page, cross_page_nav=leaves page, utility=helper

Return ONLY a JSON object, no prose, no code fences.
"""

# ---- Providers ----------------------------------------------------------

def load_providers_map():
    cfg = json.loads((Path.home() / ".brain/config.json").read_text(encoding="utf-8"))
    providers = cfg.get("providers", {})
    out = {}
    # Use the same roster as ground-truth builder, but rename for experiment clarity
    mapping = {
        "opus":   "squarefaceicon",   # strong model (Opus 4.6)
        "mid":    "tencent",          # GLM-5 via Anthropic-compatible proxy
        "cheap":  "deepseek",         # deepseek-chat
    }
    for role, pid in mapping.items():
        p = providers.get(pid)
        if not p:
            continue
        out[role] = {
            "role": role,
            "id": pid,
            "api_key": p.get("api_key"),
            "base_url": p.get("base_url") or p.get("endpoint"),
            "model": p.get("model"),
            "protocol": p.get("protocol", "anthropic"),
        }
    return out


# ---- LLM calls ----------------------------------------------------------

def call_anthropic(provider, user, image_b64=None):
    import anthropic
    client = anthropic.Anthropic(api_key=provider["api_key"], base_url=provider["base_url"])
    content = []
    if image_b64:
        content.append({
            "type": "image",
            "source": {"type": "base64", "media_type": "image/png", "data": image_b64},
        })
    content.append({"type": "text", "text": user})
    resp = client.messages.create(
        model=provider["model"],
        max_tokens=1024,
        system=SYSTEM,
        messages=[{"role": "user", "content": content}],
    )
    return resp.content[0].text


def call_openai(provider, user, image_b64=None):
    from openai import OpenAI
    client = OpenAI(api_key=provider["api_key"], base_url=provider["base_url"])
    # DeepSeek's chat completions don't support images across all endpoints,
    # so if image_b64 is set for this provider, we degrade gracefully.
    messages = [{"role": "system", "content": SYSTEM},
                {"role": "user", "content": user}]
    if image_b64:
        # Try image-url format; if provider rejects, we fall back to text-only.
        try:
            messages[-1]["content"] = [
                {"type": "text", "text": user},
                {"type": "image_url",
                 "image_url": {"url": f"data:image/png;base64,{image_b64}"}},
            ]
        except Exception:
            pass
    resp = client.chat.completions.create(
        model=provider["model"],
        max_tokens=1024,
        messages=messages,
    )
    return resp.choices[0].message.content


def call(provider, user, image_b64=None):
    if provider["protocol"] == "openai":
        return call_openai(provider, user, image_b64)
    return call_anthropic(provider, user, image_b64)


# ---- Prompt construction -------------------------------------------------

def parse_json(raw):
    s = raw.strip()
    if s.startswith("```"):
        s = re.sub(r"^```\w*\n", "", s).rstrip("`").rstrip().rstrip("`")
    start = s.find("{"); end = s.rfind("}")
    if start == -1 or end == -1:
        raise ValueError(f"no JSON object: {s[:200]}")
    return json.loads(s[start:end + 1])


def neighbors(el, snapshot, k=MAX_SNAPSHOT_NEIGHBORS):
    ex, ey = el.get("x", 0), el.get("y", 0)
    cands = [(((o.get("x", 0) - ex) ** 2 + (o.get("y", 0) - ey) ** 2), o)
             for o in snapshot if o.get("id") != el.get("id")]
    cands.sort(key=lambda t: t[0])
    return cands[:k]


def build_prompt_A(meta, target, snapshot):
    near = neighbors(target, snapshot)
    near_txt = "\n".join(
        f"  [{o.get('id')}] {o.get('role')} {o.get('name','')!r}" for _, o in near
    )
    return (
        f"Page URL: {meta.get('url')}\n"
        f"Category: {meta.get('category')}\n\n"
        f"TARGET (label this one):\n"
        f"  id={target.get('id')} tag={target.get('tag')} role={target.get('role')} "
        f"type={target.get('type')} name={target.get('name')!r} href={target.get('href')}\n\n"
        f"Nearby elements for context:\n{near_txt}\n"
    )


def build_prompt_C(meta, target, snapshot, html_chunk):
    base = build_prompt_A(meta, target, snapshot)
    return base + f"\n\nSurrounding HTML fragment:\n```html\n{html_chunk}\n```\n"


def build_prompt_B(meta, target, snapshot):
    """B = A + screenshot, the prompt is the same as A; the image is attached separately."""
    return build_prompt_A(meta, target, snapshot)


def extract_html_chunk(html_text, target_name):
    """Very cheap: find first occurrence of target's visible name in raw HTML,
    return ±HTML_MAX_CHARS/2 window around it. Fallback: first HTML_MAX_CHARS chars."""
    if target_name:
        idx = html_text.find(target_name)
        if idx >= 0:
            start = max(0, idx - HTML_MAX_CHARS // 2)
            end = min(len(html_text), idx + HTML_MAX_CHARS // 2)
            return html_text[start:end]
    return html_text[:HTML_MAX_CHARS]


# ---- Main ---------------------------------------------------------------

def result_path(combo, role, page_id, el_id):
    return RESULTS_DIR / f"{combo}_{role}_{page_id}_{el_id}.json"


def already_done(path, needed_runs):
    if not path.exists():
        return False
    try:
        data = json.loads(path.read_text(encoding="utf-8"))
        return len([p for p in data.get("predictions", []) if p.get("error") is None]) >= needed_runs
    except Exception:
        return False


def run_role(combo, role, provider, page_id, meta, snapshot, target, html_chunk, screenshot_b64):
    """Process one (combo, role, element). Runs in a thread."""
    path = result_path(combo, role, page_id, target["id"])
    if already_done(path, RUNS_PER_MODEL):
        return "skip"

    # Skip image combo for non-image-capable providers rather than wasting calls.
    if combo == "B" and provider["protocol"] == "openai" and provider["id"] == "deepseek":
        Path(path).write_text(json.dumps({
            "combo": combo, "role": role, "provider": provider["id"],
            "page_id": page_id, "element_id": target["id"],
            "predictions": [], "skipped": "provider has no image support",
        }, ensure_ascii=False, indent=2), encoding="utf-8")
        return "skipped_image"

    if combo == "A":
        user = build_prompt_A(meta, target, snapshot); image_b64 = None
    elif combo == "B":
        user = build_prompt_B(meta, target, snapshot); image_b64 = screenshot_b64
    elif combo == "C":
        user = build_prompt_C(meta, target, snapshot, html_chunk); image_b64 = None
    else:
        raise ValueError(combo)

    predictions = []
    for run in range(RUNS_PER_MODEL):
        t0 = time.time()
        try:
            raw = call(provider, user, image_b64=image_b64)
            obj = parse_json(raw)
            predictions.append({"run": run, "latency_ms": int((time.time() - t0) * 1000), **obj})
        except Exception as e:
            predictions.append({"run": run, "latency_ms": int((time.time() - t0) * 1000),
                                 "error": repr(e)[:500]})
        time.sleep(0.15)

    Path(path).write_text(json.dumps({
        "combo": combo, "role": role, "provider": provider["id"],
        "model": provider["model"], "page_id": page_id, "element_id": target["id"],
        "predictions": predictions,
    }, ensure_ascii=False, indent=2), encoding="utf-8")
    return "done"


def run_one(combo, role_providers, page_id, meta, snapshot, target, html_chunk, screenshot_b64):
    """Kick off all roles for this element in parallel."""
    from concurrent.futures import ThreadPoolExecutor, as_completed
    with ThreadPoolExecutor(max_workers=len(role_providers)) as pool:
        futs = [
            pool.submit(run_role, combo, role, provider, page_id, meta, snapshot,
                        target, html_chunk, screenshot_b64)
            for role, provider in role_providers.items()
        ]
        for f in as_completed(futs):
            f.result()


def main():
    roles = load_providers_map()
    print(f"Providers loaded: {list(roles.keys())}")
    for r, p in roles.items():
        print(f"  {r:8} → {p['id']:15} model={p['model']} protocol={p['protocol']}")
    if len(roles) < 2:
        print("ERROR: need at least 2 provider roles"); sys.exit(2)

    # Find ground truth files
    gt_files = sorted(LABELS_DIR.glob("*.ground_truth.json"))
    print(f"\nGround truth files: {len(gt_files)}")

    tasks = []
    for gt_path in gt_files:
        gt = json.loads(gt_path.read_text(encoding="utf-8"))
        page_id = gt["page_id"]
        page_dir = SAMPLES_DIR / page_id
        meta = json.loads((page_dir / "_meta.json").read_text(encoding="utf-8"))
        snapshot = json.loads((page_dir / "snapshot.json").read_text(encoding="utf-8"))
        html_text = (page_dir / "page.html").read_text(encoding="utf-8", errors="replace")
        screenshot_path = page_dir / "screenshot.png"
        screenshot_b64 = None
        if screenshot_path.exists():
            screenshot_b64 = base64.b64encode(screenshot_path.read_bytes()).decode()

        for el in gt.get("elements", []):
            if not el["ground_truth"].get("included_in_experiment"):
                continue
            # Find this element's full record in snapshot (for nearby context)
            target = next((s for s in snapshot if s.get("id") == el["id"]), None)
            if target is None:
                continue
            html_chunk = extract_html_chunk(html_text, target.get("name") or "")
            tasks.append((page_id, meta, snapshot, target, html_chunk, screenshot_b64))

    print(f"Total elements to label: {len(tasks)}")
    print(f"Planned calls: {len(tasks)} × 3 combos × {len(roles)} models × {RUNS_PER_MODEL} runs "
          f"= {len(tasks) * 3 * len(roles) * RUNS_PER_MODEL}")

    only_combo = os.environ.get("ONLY_COMBO")  # A / B / C for restart/continue
    only_role = os.environ.get("ONLY_ROLE")

    combos = [c for c in ("A", "B", "C") if (not only_combo or c == only_combo)]
    role_subset = {k: v for k, v in roles.items() if (not only_role or k == only_role)}

    from concurrent.futures import ThreadPoolExecutor, as_completed
    ELEMENT_PARALLEL = int(os.environ.get("ELEMENT_PARALLEL", "4"))
    for combo in combos:
        done = 0
        total = len(tasks)
        with ThreadPoolExecutor(max_workers=ELEMENT_PARALLEL) as pool:
            futs = {
                pool.submit(run_one, combo, role_subset, *t): t for t in tasks
            }
            for f in as_completed(futs):
                try:
                    f.result()
                except Exception as e:
                    print(f"  element error: {e!r}")
                done += 1
                if done % 5 == 0 or done == total:
                    print(f"  ...combo={combo} {done}/{total}")
        print(f"=== combo {combo} done ===")

    print(f"\nAll results in {RESULTS_DIR}")


if __name__ == "__main__":
    main()
