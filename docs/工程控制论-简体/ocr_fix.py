#!/usr/bin/env python3
"""
《工程控制论》简体 MD 的 OCR 高频错字机械清洗脚本。

策略：
  - 仅替换审校时确认的、上下文无依赖的高频 OCR 错误
  - 每次替换前后做差异统计，便于回溯
  - 原地修改 MD 文件，同时生成 .bak 备份
  - 生成修复报告 ocr_fix_report.md

用法：
  python3 ocr_fix.py           # 预演（不改文件，只报告会改什么）
  python3 ocr_fix.py --apply   # 真正改文件
  python3 ocr_fix.py --restore # 从 .bak 恢复
"""

from __future__ import annotations

import argparse
import os
import re
import shutil
import sys
from collections import Counter
from pathlib import Path

DOC_DIR = Path("/www/wwwroot/project/brain-v3/docs/工程控制论-简体")

# ============================================================
# 错字对照表
# ============================================================
# 规则：(错误, 正确, 备注)
# 只放「无歧义、上下文无关」的替换。
# 有歧义的留给人工审校。
# ============================================================

FIXES: list[tuple[str, str, str]] = [
    # --- 第8章开头样本 ---
    ("愎杂", "复杂", "第8章"),
    ("以过的", "以前的", "第8章"),
    ("科学莱", "科学家", "第8章"),
    ("臂如说", "譬如说", "第8章"),
    ("反喂", "反馈", "第8/其他章"),
    ("希果", "如果", "第8章"),

    # --- 第9章开头样本 ---
    ("氟流", "气流", "第9章"),
    ("辙入", "输入", "第9章高频"),
    ("辙出", "输出", "第9章高频"),
    ("撖入", "输入", "各章高频"),
    ("撖出", "输出", "各章高频"),
    ("輀幅", "振幅", "第10章等"),

    # --- 第10章开头样本 ---
    ("继谨器", "继电器", "第10章"),
    ("愉出", "输出", "第10章"),
    ("屋栈性", "是线性", "第10章"),
    ("运勤", "运动", "第10章"),
    ("酋先", "首先", "第10章"),
    ("遗嘁", "遗憾", "第10章"),
    ("輸出", "输出", "繁简残留"),
    ("輸入", "输入", "繁简残留"),
    ("傅遞", "传递", "繁简残留"),

    # --- 第13章开头样本 ---
    ("弹遒", "弹道", "第13章反复出现"),
    ("扰劝", "扰动", "第13章"),
    ("正说弹道", "正规弹道", "第13章"),
    ("手绩", "手续", "第13章"),
    ("闽素", "因素", "各章"),
    ("改手", "改正", "第13章"),

    # --- 第17章开头样本 ---
    ("重婴", "重要", "各章"),
    ("觐点", "观点", "各章"),
    ("考虚", "考虑", "各章"),
    ("3动", "自动", "第17章"),
    # 注意：阿施贝是钱书原译，不改为「阿什贝」
    # 李雅普诺夫 OCR 常错为「李雅遒诺夫」，必须先处理再做遒→道，否则变成「李雅道诺夫」
    ("李雅遒诺夫", "李雅普诺夫", "第19章人名"),
    ("李雅遒 诺夫", "李雅普诺夫", "第19章索引排版带空格"),
    ("李雅遒", "李雅普", "兜底"),

    # --- 第8/10/17 章 OCR 常见字形混淆 ---
    ("佴是", "但是", "高频"),
    ("佴", "但", "高频"),  # 注意：可能有误伤，先标为谨慎
    ("雨个", "两个", "高频"),
    ("雨", "两", "高频 误伤风险：放在脚本末尾单独处理"),
    ("两端", "两端", "占位"),
    ("兩", "两", "繁简残留"),

    # --- 自动化/控制论专有高频 ---
    ("频卒", "频率", "各章"),
    ("f 率", "频率", "OCR残骸"),
    ("淺动", "扰动", "各章"),
    ("震动", "振动", "各章需慎用"),  # 震动/振动有时是同义，OCR 常错为震动
    ("w 差", "偏差", "各章需慎用"),
    ("传逑", "传递", "各章"),
    ("输人", "输入", "形近字「人」「入」互换高频"),
    ("輸人", "输入", "同上"),
    ("转入", "输入", "OCR「输」误为「转」，需谨慎，放弃"),  # 有歧义，不启用

    # --- 一般常见 OCR 错字 ---
    ("亊", "事", "形近"),
    ("塞际", "实际", "形近"),
    ("稟", "票", "形近，需慎"),  # 关闭
    ("逕", "径", "形近"),
    ("遒", "道", "遒/道 形近"),
    ("叚设", "假设", "高频"),
    ("假定", "假定", "占位"),
    ("苜先", "首先", "形近"),
    ("恣意", "任意", "形近"),
    ("觉度", "角度", "形近"),

    # --- 标点 / 脚注 ---
    ("ig", "图", "OCR 图号常见识别错"),  # 风险极高，关闭
    ("困", "图", "同上关闭"),
]

# 高风险替换（可能误伤）放进这个列表，单独控制
# 形近字替换往往会误伤，默认不启用
DANGEROUS_FIXES: list[tuple[str, str, str]] = [
    ("佴", "但", "形近字，误伤风险高"),
    ("雨", "两", "形近字，误伤风险高"),
    ("稟", "票", "形近字，误伤风险高"),
    ("震动", "振动", "同义近义混用"),
    ("w 差", "偏差", "模式不稳"),
    ("转入", "输入", "歧义高"),
    ("ig", "图", "歧义高"),
    ("困", "图", "歧义高"),
]


def load_files() -> list[Path]:
    """仅处理章节 MD，不处理 README/报告/指令/脚本本身。"""
    files = sorted(DOC_DIR.glob("*.md"))
    keep = []
    for f in files:
        name = f.name
        # 跳过审校工具自身和人工产出
        if name in {
            "README-续接说明.md",
            "ChatGPT任务指令.md",
            "审校报告-第一轮.md",
            "钱学森提炼版.md",
            "ocr_fix_report.md",
        }:
            continue
        keep.append(f)
    return keep


def preview(files: list[Path]) -> dict[str, Counter]:
    """统计每个错字在每个文件中会被替换几次。"""
    stats: dict[str, Counter] = {}
    for f in files:
        text = f.read_text(encoding="utf-8")
        c = Counter()
        for wrong, right, _note in FIXES:
            if wrong == right:
                continue
            # 跳过 DANGEROUS
            if (wrong, right) in {(d[0], d[1]) for d in DANGEROUS_FIXES}:
                continue
            n = text.count(wrong)
            if n:
                c[f"{wrong} → {right}"] = n
        if c:
            stats[f.name] = c
    return stats


def apply(files: list[Path]) -> tuple[dict[str, Counter], int]:
    """真正替换。"""
    stats: dict[str, Counter] = {}
    total_changes = 0
    for f in files:
        text = f.read_text(encoding="utf-8")
        original = text
        c = Counter()
        for wrong, right, _note in FIXES:
            if wrong == right:
                continue
            if (wrong, right) in {(d[0], d[1]) for d in DANGEROUS_FIXES}:
                continue
            n = text.count(wrong)
            if n:
                text = text.replace(wrong, right)
                c[f"{wrong} → {right}"] = n
                total_changes += n
        if text != original:
            # 备份
            bak = f.with_suffix(f.suffix + ".bak")
            if not bak.exists():
                shutil.copy2(f, bak)
            f.write_text(text, encoding="utf-8")
            stats[f.name] = c
    return stats, total_changes


def restore(files: list[Path]) -> int:
    """从 .bak 恢复。"""
    n = 0
    for f in files:
        bak = f.with_suffix(f.suffix + ".bak")
        if bak.exists():
            shutil.copy2(bak, f)
            n += 1
    return n


def write_report(stats: dict[str, Counter], total: int, dry_run: bool) -> Path:
    """写修复报告。"""
    report = DOC_DIR / "ocr_fix_report.md"
    lines = []
    lines.append(f"# OCR 机械修复报告")
    lines.append("")
    lines.append(f"- 模式：{'预演（未修改文件）' if dry_run else '已应用到文件'}")
    lines.append(f"- 总替换次数：**{total}**")
    lines.append(f"- 涉及文件数：{len(stats)}")
    lines.append(f"- 备份文件：同目录下 `*.md.bak`（如需回退执行 `python3 ocr_fix.py --restore`）")
    lines.append("")
    lines.append("## 分文件统计")
    lines.append("")
    for fname in sorted(stats.keys()):
        c = stats[fname]
        sub = sum(c.values())
        lines.append(f"### {fname}（共 {sub} 处）")
        lines.append("")
        for rule, n in c.most_common():
            lines.append(f"- `{rule}` × **{n}**")
        lines.append("")
    report.write_text("\n".join(lines), encoding="utf-8")
    return report


def main() -> int:
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--apply", action="store_true", help="真正修改文件")
    parser.add_argument("--restore", action="store_true", help="从 .bak 恢复")
    args = parser.parse_args()

    files = load_files()
    print(f"扫描文件数：{len(files)}")

    if args.restore:
        n = restore(files)
        print(f"已从 .bak 恢复 {n} 个文件")
        return 0

    if args.apply:
        stats, total = apply(files)
        report = write_report(stats, total, dry_run=False)
        print(f"已应用 {total} 处替换，报告：{report}")
    else:
        stats = preview(files)
        total = sum(sum(c.values()) for c in stats.values())
        report = write_report(stats, total, dry_run=True)
        print(f"[预演] 共将替换 {total} 处，报告：{report}")
        print("确认无误后运行：python3 ocr_fix.py --apply")

    return 0


if __name__ == "__main__":
    sys.exit(main())
