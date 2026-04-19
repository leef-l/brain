#!/usr/bin/env python3
"""Phase 0 sample capture.

For each sample in samples.json:
  - Open with Playwright (headless Chromium)
  - Wait for the key selector
  - Save:
      samples/<id>/url.txt
      samples/<id>/page.html           (rendered outerHTML)
      samples/<id>/screenshot.png      (full page)
      samples/<id>/snapshot.json       (result of build_snapshot.js)
      samples/<id>/_meta.json          (category, description, capture timestamp)

Robust to network failures — logs and continues.
"""

import json
import sys
import time
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
SCRIPTS = ROOT / "scripts"
SAMPLES_DIR = ROOT / "samples"
SAMPLES_JSON = SCRIPTS / "samples.json"
BUILD_SNAPSHOT_JS = (SCRIPTS / "build_snapshot.js").read_text(encoding="utf-8")

VIEWPORT = {"width": 1440, "height": 900}
NAV_TIMEOUT_MS = 30_000
WAIT_SELECTOR_TIMEOUT_MS = 15_000

try:
    from playwright.sync_api import sync_playwright, TimeoutError as PlaywrightTimeoutError
except ImportError:
    print("ERROR: playwright not installed. Run: pip3 install playwright && playwright install chromium", file=sys.stderr)
    sys.exit(2)


def capture_one(p, sample):
    sid = sample["id"]
    url = sample["url"]
    out_dir = SAMPLES_DIR / sid
    out_dir.mkdir(parents=True, exist_ok=True)

    print(f"[{sid}] → {url}")

    browser = p.chromium.launch(headless=True, args=["--no-sandbox"])
    context = browser.new_context(
        viewport=VIEWPORT,
        user_agent=(
            "Mozilla/5.0 (X11; Linux x86_64) AppleWebKit/537.36 "
            "(KHTML, like Gecko) Chrome/127.0.0.0 Safari/537.36"
        ),
        locale="en-US",
    )
    page = context.new_page()

    status = "ok"
    err = None
    snapshot = []
    try:
        page.goto(url, timeout=NAV_TIMEOUT_MS, wait_until="domcontentloaded")
        wait_for = sample.get("wait_for")
        if wait_for:
            try:
                page.wait_for_selector(wait_for, timeout=WAIT_SELECTOR_TIMEOUT_MS)
            except PlaywrightTimeoutError:
                status = "wait_selector_timeout"
        page.wait_for_timeout(1500)

        snapshot = page.evaluate(BUILD_SNAPSHOT_JS)

        html = page.content()
        (out_dir / "page.html").write_text(html, encoding="utf-8")

        try:
            page.screenshot(path=str(out_dir / "screenshot.png"), full_page=True)
        except Exception as e:
            page.screenshot(path=str(out_dir / "screenshot.png"), full_page=False)

        (out_dir / "snapshot.json").write_text(
            json.dumps(snapshot, ensure_ascii=False, indent=2), encoding="utf-8"
        )
        (out_dir / "url.txt").write_text(url + "\n", encoding="utf-8")

    except Exception as e:
        status = "error"
        err = repr(e)
        print(f"  [{sid}] ERROR: {err}", file=sys.stderr)

    meta = {
        "id": sid,
        "category": sample.get("category"),
        "description": sample.get("description"),
        "url": url,
        "captured_at": time.strftime("%Y-%m-%dT%H:%M:%SZ", time.gmtime()),
        "viewport": VIEWPORT,
        "status": status,
        "error": err,
        "element_count": len(snapshot),
    }
    (out_dir / "_meta.json").write_text(
        json.dumps(meta, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    context.close()
    browser.close()
    return meta


def already_captured(sid):
    meta = SAMPLES_DIR / sid / "_meta.json"
    if not meta.exists():
        return False
    try:
        m = json.loads(meta.read_text(encoding="utf-8"))
        return m.get("status") == "ok" and m.get("element_count", 0) > 0
    except Exception:
        return False


def main():
    samples = json.loads(SAMPLES_JSON.read_text(encoding="utf-8"))["samples"]
    SAMPLES_DIR.mkdir(exist_ok=True)
    force = "--force" in sys.argv

    results = []
    with sync_playwright() as p:
        for s in samples:
            if not force and already_captured(s["id"]):
                print(f"[{s['id']}] already captured, skip")
                results.append({"id": s["id"], "status": "skipped"})
                continue
            try:
                results.append(capture_one(p, s))
            except Exception as e:
                results.append({"id": s["id"], "status": "fatal", "error": repr(e)})

    summary = {
        "total": len(results),
        "ok": sum(1 for r in results if r.get("status") == "ok"),
        "results": results,
    }
    (ROOT / "samples" / "_summary.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8"
    )

    print()
    print(f"Captured {summary['ok']}/{summary['total']} samples")
    for r in results:
        if r.get("status") != "ok":
            print(f"  - {r['id']}: {r.get('status')} {r.get('error','')}")


if __name__ == "__main__":
    main()
