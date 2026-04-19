#!/usr/bin/env python3
"""Phase 0 analyzer — consume results/*.json and labels/*.ground_truth.json,
produce accuracy tables by combo × model × dimension, and write report.md.

Metrics:
  - Exact Match Accuracy per enum dimension (reversibility / risk_level / flow_role)
  - Semantic Match Accuracy on action_intent (soft: token-overlap Jaccard > 0.3)
  - Confidence Calibration (mean predicted confidence vs. actual correctness)
  - Stability: fraction of (element, combo, model) triples where all 3 runs agree
  - Per-category accuracy (login / ecommerce / admin / search / form / framework_*)
  - Per-framework accuracy if data includes framework_vue / framework_angular samples

Outputs:
  report.md          — human-readable summary
  analyze_raw.json   — all metrics as JSON for later compare
"""

import json
import re
import statistics
from collections import defaultdict, Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parents[1]
LABELS_DIR = ROOT / "labels"
RESULTS_DIR = ROOT / "results"
SAMPLES_DIR = ROOT / "samples"

ENUM_DIMS = ("reversibility", "risk_level", "flow_role")


def load_ground_truth():
    gt = {}
    for f in LABELS_DIR.glob("*.ground_truth.json"):
        d = json.loads(f.read_text(encoding="utf-8"))
        for el in d.get("elements", []):
            if not el["ground_truth"].get("included_in_experiment"):
                continue
            gt[(d["page_id"], el["id"])] = {
                "category": d.get("category"),
                "ground_truth": el["ground_truth"],
                "element": el["element"],
            }
    return gt


def load_results():
    out = []
    for f in RESULTS_DIR.glob("*.json"):
        out.append(json.loads(f.read_text(encoding="utf-8")))
    return out


def tokenize(s):
    return set(re.findall(r"\w+", (s or "").lower()))


def jaccard(a, b):
    sa, sb = tokenize(a), tokenize(b)
    if not sa or not sb:
        return 0.0
    return len(sa & sb) / len(sa | sb)


def analyze(gt, results):
    # bucket by (combo, role)
    by_key = defaultdict(list)
    for r in results:
        for p in r.get("predictions", []):
            if p.get("error"):
                continue
            key = (r["combo"], r["role"])
            by_key[key].append({
                "page_id": r["page_id"],
                "element_id": r["element_id"],
                "pred": p,
                "model": r.get("model"),
            })

    summary = {}
    for (combo, role), samples in sorted(by_key.items()):
        # Collect per-element run lists for stability
        runs = defaultdict(list)
        for s in samples:
            runs[(s["page_id"], s["element_id"])].append(s["pred"])

        total = 0
        dim_correct = {d: 0 for d in ENUM_DIMS}
        intent_correct = 0
        confidences = []
        conf_correct = defaultdict(list)
        stability = 0
        stability_total = 0
        by_category = defaultdict(lambda: {d: [0, 0] for d in list(ENUM_DIMS) + ["intent"]})

        for (page_id, el_id), preds in runs.items():
            key = (page_id, el_id)
            if key not in gt:
                continue
            truth = gt[key]["ground_truth"]
            category = gt[key]["category"]

            # Stability: all runs agree on reversibility+risk+role
            if len(preds) >= 3:
                stability_total += 1
                same = all(
                    preds[0].get(d) == p.get(d)
                    for p in preds[1:]
                    for d in ENUM_DIMS
                )
                if same:
                    stability += 1

            # We score each run independently (Exact Match at run level)
            for p in preds:
                total += 1
                for d in ENUM_DIMS:
                    pred_v = p.get(d)
                    true_v = truth.get(d)
                    ok = (pred_v == true_v and pred_v is not None)
                    if ok:
                        dim_correct[d] += 1
                    by_category[category][d][1] += 1
                    if ok:
                        by_category[category][d][0] += 1
                # intent: Jaccard > 0.3
                ok_int = jaccard(p.get("action_intent", ""), truth.get("action_intent") or "") > 0.3
                if ok_int:
                    intent_correct += 1
                by_category[category]["intent"][1] += 1
                if ok_int:
                    by_category[category]["intent"][0] += 1
                # confidence
                c = p.get("confidence")
                if isinstance(c, (int, float)):
                    confidences.append(c)
                    # calibration: bucket
                    key_c = round(c, 1)
                    avg_dim_correct = sum(
                        1 for d in ENUM_DIMS if p.get(d) == truth.get(d)
                    ) / len(ENUM_DIMS)
                    conf_correct[key_c].append(avg_dim_correct)

        if total == 0:
            continue

        per_cat = {}
        for cat, dims in by_category.items():
            per_cat[cat] = {
                d: round(v[0] / v[1], 3) if v[1] else None
                for d, v in dims.items()
            }

        summary[f"{combo}/{role}"] = {
            "combo": combo, "role": role,
            "total_runs": total,
            "exact_accuracy": {
                d: round(dim_correct[d] / total, 3) for d in ENUM_DIMS
            },
            "intent_semantic_accuracy": round(intent_correct / total, 3),
            "mean_confidence": round(statistics.mean(confidences), 3) if confidences else None,
            "confidence_calibration": {
                str(k): round(statistics.mean(v), 3) for k, v in sorted(conf_correct.items())
            },
            "stability_fraction": round(stability / stability_total, 3) if stability_total else None,
            "by_category": per_cat,
        }

    return summary


def render_markdown(summary, gt):
    lines = []
    lines.append("# 阶段 0 实验分析报告\n")
    lines.append(f"- Ground-truth 元素数(included): {len(gt)}")
    lines.append(f"- 生成时间: {Path(__file__).stat().st_mtime}\n")

    if not summary:
        lines.append("**没有可分析的结果**。先跑 `scripts/run_experiment.py`。")
        return "\n".join(lines)

    # === 主表:Exact Match by combo × role × dim ===
    lines.append("## 1. 维度 Exact Match 准确率\n")
    lines.append("| combo/model | total_runs | reversibility | risk_level | flow_role | intent (Jaccard>0.3) | stability | mean_conf |")
    lines.append("|---|---|---|---|---|---|---|---|")
    for key, m in sorted(summary.items()):
        lines.append(
            f"| {key} | {m['total_runs']} | "
            f"{m['exact_accuracy']['reversibility']:.1%} | "
            f"{m['exact_accuracy']['risk_level']:.1%} | "
            f"{m['exact_accuracy']['flow_role']:.1%} | "
            f"{m['intent_semantic_accuracy']:.1%} | "
            f"{m['stability_fraction']:.1%} | "
            f"{m['mean_confidence']:.2f} |"
        )

    # === 按类别 ===
    lines.append("\n## 2. 按页面类别细分\n")
    categories = sorted({c for m in summary.values() for c in m["by_category"]})
    for key, m in sorted(summary.items()):
        lines.append(f"\n### {key}\n")
        lines.append("| category | reversibility | risk_level | flow_role | intent |")
        lines.append("|---|---|---|---|---|")
        for cat in categories:
            row = m["by_category"].get(cat, {})
            if not row:
                continue
            def fmt(x): return "—" if x is None else f"{x:.1%}"
            lines.append(
                f"| {cat} | {fmt(row.get('reversibility'))} | {fmt(row.get('risk_level'))} | "
                f"{fmt(row.get('flow_role'))} | {fmt(row.get('intent'))} |"
            )

    # === 置信度校准 ===
    lines.append("\n## 3. Confidence Calibration\n")
    lines.append("每个桶显示 (模型说 X 置信度时,实际平均维度正确率)。健康模型应大致对角。\n")
    for key, m in sorted(summary.items()):
        lines.append(f"\n### {key}\n")
        lines.append("| confidence bucket | avg dim-correct |")
        lines.append("|---|---|")
        for k, v in sorted(m["confidence_calibration"].items(), key=lambda x: float(x[0])):
            lines.append(f"| {k} | {v:.1%} |")

    # === Go/No-Go 决策 ===
    lines.append("\n## 4. Go/No-Go 初步判断\n")
    go_conditions = []
    no_go_conditions = []
    for key, m in sorted(summary.items()):
        acc = m["exact_accuracy"]
        avg_acc = sum(acc.values()) / len(acc)
        if avg_acc >= 0.70:
            go_conditions.append(f"- **{key}**: 平均 Exact Match {avg_acc:.1%} ≥ 70%  ✓ Go 候选")
        elif avg_acc < 0.60:
            no_go_conditions.append(f"- **{key}**: 平均 Exact Match {avg_acc:.1%} < 60%  ✗ No-Go 信号")
    if go_conditions:
        lines.append("### Go 候选\n")
        lines.extend(go_conditions)
    if no_go_conditions:
        lines.append("\n### No-Go 信号\n")
        lines.extend(no_go_conditions)
    if not go_conditions and not no_go_conditions:
        lines.append("介于 60-70%,属于有条件 Go(缩小范围或调整方向)。")

    return "\n".join(lines)


def main():
    gt = load_ground_truth()
    results = load_results()
    print(f"Loaded ground truth for {len(gt)} elements")
    print(f"Loaded {len(results)} result files")

    summary = analyze(gt, results)
    (ROOT / "analyze_raw.json").write_text(
        json.dumps(summary, ensure_ascii=False, indent=2), encoding="utf-8"
    )
    md = render_markdown(summary, gt)
    (ROOT / "report.md").write_text(md, encoding="utf-8")
    print(f"\nWrote {ROOT/'report.md'}")
    print(f"Wrote {ROOT/'analyze_raw.json'}")


if __name__ == "__main__":
    main()
