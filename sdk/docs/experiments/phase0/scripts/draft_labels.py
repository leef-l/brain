#!/usr/bin/env python3
"""Phase 0 draft labeler.

For each captured sample, pick ~6 diverse interactive elements and ask Opus
to produce a draft ground-truth label along four dimensions:
  action_intent / reversibility / risk_level / flow_role

Output: labels/<page_id>.json. These drafts MUST be reviewed by a human
before feeding into run_experiment.py (that's the whole point of the
Go/No-Go: we measure LLM against human truth, not against its own draft).

Element selection strategy (greedy diversity):
  - Always include: primary submit-like buttons, password fields
  - Prefer spread across roles: button / input / link / select / textarea
  - Cap at 6 elements per page
"""

import json
import os
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SAMPLES_DIR = ROOT / "samples"
LABELS_DIR = ROOT / "labels"
LABELS_DIR.mkdir(exist_ok=True)

try:
    import anthropic
except ImportError:
    print("ERROR: anthropic SDK not installed. pip3 install anthropic", file=sys.stderr)
    sys.exit(2)

MODEL = os.environ.get("DRAFT_MODEL", "claude-opus-4-5")
BASE_URL = os.environ.get("ANTHROPIC_BASE_URL")


def load_brain_provider(preferred=None):
    """Load credentials from ~/.brain/config.json.

    If preferred is given, use that provider id; otherwise use active_provider.
    Returns (api_key, base_url, model).
    """
    try:
        cfg = json.loads(Path.home().joinpath(".brain/config.json").read_text(encoding="utf-8"))
    except Exception:
        return None, None, None
    providers = cfg.get("providers", {})
    pid = preferred or cfg.get("active_provider")
    p = providers.get(pid) if isinstance(providers, dict) else None
    if not p:
        return None, None, None
    return p.get("api_key"), p.get("base_url") or p.get("endpoint"), p.get("model")
MAX_PER_PAGE = 6

SYSTEM = """You are a web UI analyst producing a DRAFT ground truth for a
research experiment. For each interactive element, output JSON with these
fields:

- action_intent: one sentence, what the user wants by interacting
- reversibility: one of {reversible, semi_reversible, irreversible, conditional}
- risk_level: one of {safe, safe_caution, destructive, external_effect}
- flow_role: one of {primary, secondary, escape, navigation, cross_page_nav, utility}
- notes: freeform, any caveat a human reviewer should double-check
- reviewer_flag: true if this case is ambiguous enough that a human MUST
  revisit it (e.g. label could legitimately be either of two values)

Be honest. Don't claim confidence you don't have. Flag ambiguity.
Output ONLY a JSON array, one object per element, same order as input."""


def pick_elements(snapshot, url):
    """Pick up to MAX_PER_PAGE diverse interactive elements."""
    if not snapshot:
        return []

    def key(el):
        role = (el.get("role") or el.get("tag") or "").lower()
        t = (el.get("type") or "").lower()
        name = (el.get("name") or "").lower()
        score = 0
        if t == "password" or "password" in name:
            score += 100
        if t == "submit" or role in ("button",) and any(k in name for k in ("submit", "sign in", "log in", "login", "register", "buy", "add to cart", "search", "continue")):
            score += 90
        if role == "button":
            score += 50
        if el.get("tag") == "input" and t in ("text", "email", "search", "number", "tel"):
            score += 40
        if el.get("tag") == "a" and el.get("href"):
            score += 30
        if role in ("tab", "menuitem"):
            score += 20
        if not el.get("inViewport"):
            score -= 15
        return score

    sorted_els = sorted(snapshot, key=key, reverse=True)

    picked = []
    seen_roles = {}
    for el in sorted_els:
        role = el.get("role") or el.get("tag") or ""
        cnt = seen_roles.get(role, 0)
        if cnt >= 3:
            continue
        seen_roles[role] = cnt + 1
        picked.append(el)
        if len(picked) >= MAX_PER_PAGE:
            break
    return picked


def render_element(el, all_snapshot):
    """Describe one element + brief context for the LLM."""
    near = []
    ex, ey = el.get("x", 0), el.get("y", 0)
    for other in all_snapshot:
        if other.get("id") == el.get("id"):
            continue
        dx = (other.get("x", 0) - ex) ** 2 + (other.get("y", 0) - ey) ** 2
        near.append((dx, other))
    near.sort(key=lambda t: t[0])
    near_lines = []
    for _, o in near[:6]:
        near_lines.append(
            f"  [{o.get('id')}] {o.get('role')} \"{o.get('name','')}\""
        )
    target = (
        f"id={el.get('id')} tag={el.get('tag')} role={el.get('role')} "
        f"type={el.get('type')} name={el.get('name')!r} "
        f"x={el.get('x')} y={el.get('y')} href={el.get('href')}"
    )
    return f"TARGET: {target}\nNEARBY:\n" + "\n".join(near_lines)


def draft_for_page(client, page_id, meta, snapshot):
    elements = pick_elements(snapshot, meta.get("url", ""))
    if not elements:
        return {
            "page_id": page_id,
            "url": meta.get("url"),
            "elements": [],
            "note": "empty snapshot",
        }

    user = (
        f"Page URL: {meta.get('url')}\n"
        f"Category: {meta.get('category')}\n\n"
        "Elements to label (preserve order):\n\n"
        + "\n---\n".join(render_element(e, snapshot) for e in elements)
    )

    resp = client.messages.create(
        model=MODEL,
        max_tokens=4096,
        system=SYSTEM,
        messages=[{"role": "user", "content": user}],
    )
    raw = resp.content[0].text.strip()
    # strip code fences if present
    if raw.startswith("```"):
        raw = raw.split("```", 2)[1]
        if raw.startswith("json"):
            raw = raw[4:]
        raw = raw.strip().rstrip("`")

    try:
        drafts = json.loads(raw)
    except json.JSONDecodeError as e:
        return {
            "page_id": page_id,
            "url": meta.get("url"),
            "elements": [],
            "error": f"JSON parse failed: {e}",
            "raw": raw[:2000],
        }

    out_elements = []
    for el, draft in zip(elements, drafts):
        out_elements.append({
            "id": el.get("id"),
            "element": {
                "tag": el.get("tag"),
                "role": el.get("role"),
                "type": el.get("type"),
                "name": el.get("name"),
                "href": el.get("href"),
                "bbox": {
                    "x": el.get("x"), "y": el.get("y"),
                    "w": el.get("w"), "h": el.get("h"),
                },
            },
            "draft": draft,
            "ground_truth": None,  # filled by human reviewer
            "review_status": "pending",
        })

    return {
        "page_id": page_id,
        "url": meta.get("url"),
        "category": meta.get("category"),
        "snapshot_file": f"samples/{page_id}/snapshot.json",
        "screenshot_file": f"samples/{page_id}/screenshot.png",
        "html_file": f"samples/{page_id}/page.html",
        "drafted_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "drafted_by_model": MODEL,
        "elements": out_elements,
    }


def main():
    global MODEL
    provider = os.environ.get("DRAFT_PROVIDER", "squarefaceicon")
    api_key = os.environ.get("ANTHROPIC_API_KEY")
    base_url = BASE_URL
    model_from_cfg = None
    if not api_key:
        api_key, base_url, model_from_cfg = load_brain_provider(provider)
    if not api_key:
        print(f"ERROR: no API key for provider={provider}", file=sys.stderr)
        sys.exit(2)

    # Model precedence: env DRAFT_MODEL → config provider.model → fallback
    if os.environ.get("DRAFT_MODEL"):
        MODEL = os.environ["DRAFT_MODEL"]
    elif model_from_cfg:
        MODEL = model_from_cfg

    kwargs = {"api_key": api_key}
    if base_url:
        kwargs["base_url"] = base_url
    print(f"Using provider={provider} model={MODEL} base_url={base_url or 'default'}")
    client = anthropic.Anthropic(**kwargs)

    pages = sorted(p for p in SAMPLES_DIR.iterdir() if p.is_dir() and not p.name.startswith("_"))
    total = 0
    for page_dir in pages:
        page_id = page_dir.name
        meta_path = page_dir / "_meta.json"
        snap_path = page_dir / "snapshot.json"
        if not meta_path.exists() or not snap_path.exists():
            print(f"  skip {page_id}: missing meta or snapshot")
            continue
        meta = json.loads(meta_path.read_text(encoding="utf-8"))
        snapshot = json.loads(snap_path.read_text(encoding="utf-8"))

        print(f"drafting {page_id} ({len(snapshot)} elements) ...")
        try:
            result = draft_for_page(client, page_id, meta, snapshot)
        except Exception as e:
            print(f"  {page_id}: ERROR {e!r}", file=sys.stderr)
            continue

        (LABELS_DIR / f"{page_id}.json").write_text(
            json.dumps(result, ensure_ascii=False, indent=2), encoding="utf-8"
        )
        total += len(result.get("elements", []))
        time.sleep(1.5)

    print(f"\nDrafted {total} element labels across {len(pages)} pages.")
    print(f"Output: {LABELS_DIR}/*.json")
    print("Next: human reviewer fills `ground_truth` and flips `review_status` to 'reviewed'.")


if __name__ == "__main__":
    main()
