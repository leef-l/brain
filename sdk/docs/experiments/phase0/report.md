# 阶段 0 实验分析报告

- Ground-truth 元素数(included): 75
- 生成时间: 1776590396.8425562

## 1. 维度 Exact Match 准确率

| combo/model | total_runs | reversibility | risk_level | flow_role | intent (Jaccard>0.3) | stability | mean_conf |
|---|---|---|---|---|---|---|---|
| A/cheap | 225 | 92.9% | 90.2% | 72.0% | 69.3% | 76.0% | 0.92 |
| A/mid | 225 | 90.7% | 91.6% | 59.6% | 74.2% | 52.0% | 0.92 |
| A/opus | 225 | 92.0% | 92.9% | 78.7% | 89.8% | 92.0% | 0.89 |
| B/mid | 177 | 92.7% | 91.0% | 56.5% | 80.2% | 61.5% | 0.91 |
| B/opus | 207 | 92.8% | 94.2% | 73.4% | 82.1% | 98.6% | 0.90 |
| C/cheap | 225 | 91.6% | 91.6% | 64.0% | 78.7% | 80.0% | 0.93 |
| C/mid | 219 | 90.9% | 90.4% | 50.7% | 76.7% | 65.2% | 0.95 |
| C/opus | 223 | 91.9% | 94.2% | 74.9% | 83.4% | 98.6% | 0.91 |

## 2. 按页面类别细分


### A/cheap

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 100.0% | 88.9% |
| admin_structured | 100.0% | 100.0% | 50.0% | 33.3% |
| ecommerce | 86.1% | 80.6% | 66.7% | 83.3% |
| form_complex | 100.0% | 100.0% | 60.0% | 40.0% |
| form_simple | 83.3% | 83.3% | 77.8% | 100.0% |
| framework_angular | 100.0% | 100.0% | 77.8% | 100.0% |
| framework_vue | 93.9% | 87.9% | 75.8% | 63.6% |
| login_rich | 66.7% | 55.6% | 77.8% | 77.8% |
| login_simple | 100.0% | 100.0% | 40.0% | 60.0% |
| search | 100.0% | 100.0% | 80.6% | 50.0% |

### A/mid

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 27.8% | 100.0% |
| admin_structured | 100.0% | 100.0% | 38.9% | 27.8% |
| ecommerce | 94.4% | 86.1% | 55.6% | 94.4% |
| form_complex | 100.0% | 100.0% | 53.3% | 33.3% |
| form_simple | 94.4% | 83.3% | 61.1% | 94.4% |
| framework_angular | 94.4% | 100.0% | 61.1% | 83.3% |
| framework_vue | 87.9% | 93.9% | 72.7% | 66.7% |
| login_rich | 55.6% | 50.0% | 66.7% | 94.4% |
| login_simple | 86.7% | 100.0% | 33.3% | 53.3% |
| search | 91.7% | 100.0% | 86.1% | 72.2% |

### A/opus

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 94.4% | 100.0% |
| admin_structured | 100.0% | 100.0% | 77.8% | 66.7% |
| ecommerce | 91.7% | 88.9% | 72.2% | 100.0% |
| form_complex | 100.0% | 100.0% | 80.0% | 100.0% |
| form_simple | 100.0% | 83.3% | 83.3% | 100.0% |
| framework_angular | 100.0% | 100.0% | 33.3% | 100.0% |
| framework_vue | 90.9% | 90.9% | 81.8% | 72.7% |
| login_rich | 50.0% | 66.7% | 100.0% | 100.0% |
| login_simple | 80.0% | 100.0% | 40.0% | 60.0% |
| search | 100.0% | 100.0% | 100.0% | 94.4% |

### B/mid

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 29.4% | 100.0% |
| admin_structured | 100.0% | 100.0% | 33.3% | 41.7% |
| ecommerce | 96.3% | 77.8% | 55.6% | 92.6% |
| form_complex | 100.0% | 100.0% | 85.7% | 50.0% |
| form_simple | 100.0% | 81.8% | 54.5% | 100.0% |
| framework_angular | 100.0% | 100.0% | 47.1% | 82.4% |
| framework_vue | 85.2% | 96.3% | 77.8% | 77.8% |
| login_rich | 46.2% | 46.2% | 61.5% | 84.6% |
| login_simple | 100.0% | 100.0% | 30.0% | 70.0% |
| search | 96.6% | 100.0% | 62.1% | 82.8% |

### B/opus

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 66.7% | 100.0% |
| admin_structured | 100.0% | 100.0% | 66.7% | 50.0% |
| ecommerce | 100.0% | 91.7% | 72.2% | 100.0% |
| form_complex | 100.0% | 100.0% | 80.0% | 60.0% |
| framework_angular | 83.3% | 100.0% | 50.0% | 100.0% |
| framework_vue | 100.0% | 100.0% | 72.7% | 81.8% |
| login_rich | 50.0% | 50.0% | 100.0% | 61.1% |
| login_simple | 80.0% | 100.0% | 40.0% | 60.0% |
| search | 100.0% | 100.0% | 91.7% | 91.7% |

### C/cheap

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 83.3% | 100.0% |
| admin_structured | 100.0% | 100.0% | 55.6% | 50.0% |
| ecommerce | 83.3% | 86.1% | 52.8% | 97.2% |
| form_complex | 100.0% | 100.0% | 60.0% | 40.0% |
| form_simple | 94.4% | 88.9% | 77.8% | 94.4% |
| framework_angular | 100.0% | 100.0% | 61.1% | 100.0% |
| framework_vue | 90.9% | 90.9% | 72.7% | 72.7% |
| login_rich | 50.0% | 50.0% | 72.2% | 72.2% |
| login_simple | 100.0% | 100.0% | 33.3% | 60.0% |
| search | 100.0% | 100.0% | 66.7% | 77.8% |

### C/mid

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 5.6% | 100.0% |
| admin_structured | 100.0% | 100.0% | 43.8% | 43.8% |
| ecommerce | 85.7% | 80.0% | 57.1% | 94.3% |
| form_complex | 100.0% | 100.0% | 73.3% | 46.7% |
| form_simple | 100.0% | 70.6% | 58.8% | 82.4% |
| framework_angular | 94.1% | 100.0% | 35.3% | 88.2% |
| framework_vue | 90.6% | 100.0% | 56.2% | 71.9% |
| login_rich | 55.6% | 50.0% | 66.7% | 66.7% |
| login_simple | 80.0% | 100.0% | 13.3% | 60.0% |
| search | 100.0% | 100.0% | 66.7% | 83.3% |

### C/opus

| category | reversibility | risk_level | flow_role | intent |
|---|---|---|---|---|
| admin_freeform | 100.0% | 100.0% | 33.3% | 100.0% |
| admin_structured | 100.0% | 100.0% | 83.3% | 66.7% |
| ecommerce | 100.0% | 100.0% | 65.7% | 100.0% |
| form_complex | 100.0% | 100.0% | 100.0% | 78.6% |
| form_simple | 100.0% | 83.3% | 100.0% | 83.3% |
| framework_angular | 100.0% | 100.0% | 33.3% | 100.0% |
| framework_vue | 90.9% | 97.0% | 66.7% | 87.9% |
| login_rich | 33.3% | 50.0% | 100.0% | 50.0% |
| login_simple | 80.0% | 100.0% | 60.0% | 60.0% |
| search | 100.0% | 100.0% | 100.0% | 83.3% |

## 3. Confidence Calibration

每个桶显示 (模型说 X 置信度时,实际平均维度正确率)。健康模型应大致对角。


### A/cheap

| confidence bucket | avg dim-correct |
|---|---|
| 0.7 | 77.8% |
| 0.8 | 75.8% |
| 0.9 | 87.5% |

### A/mid

| confidence bucket | avg dim-correct |
|---|---|
| 0.5 | 66.7% |
| 0.6 | 66.7% |
| 0.7 | 66.7% |
| 0.8 | 75.0% |
| 0.9 | 82.0% |
| 1.0 | 87.9% |

### A/opus

| confidence bucket | avg dim-correct |
|---|---|
| 0.4 | 100.0% |
| 0.5 | 81.8% |
| 0.6 | 100.0% |
| 0.7 | 100.0% |
| 0.8 | 74.6% |
| 0.9 | 87.3% |
| 1.0 | 95.0% |

### B/mid

| confidence bucket | avg dim-correct |
|---|---|
| 0.5 | 66.7% |
| 0.6 | 88.9% |
| 0.7 | 86.7% |
| 0.8 | 72.1% |
| 0.9 | 81.9% |
| 1.0 | 82.2% |

### B/opus

| confidence bucket | avg dim-correct |
|---|---|
| 0.3 | 83.3% |
| 0.4 | 66.7% |
| 0.5 | 66.7% |
| 0.6 | 100.0% |
| 0.7 | 100.0% |
| 0.8 | 83.3% |
| 0.9 | 84.6% |
| 1.0 | 94.2% |

### C/cheap

| confidence bucket | avg dim-correct |
|---|---|
| 0.8 | 80.6% |
| 0.9 | 82.6% |

### C/mid

| confidence bucket | avg dim-correct |
|---|---|
| 0.6 | 77.8% |
| 0.7 | 100.0% |
| 0.8 | 76.5% |
| 0.9 | 73.3% |
| 1.0 | 86.5% |

### C/opus

| confidence bucket | avg dim-correct |
|---|---|
| 0.4 | 100.0% |
| 0.5 | 100.0% |
| 0.6 | 100.0% |
| 0.8 | 77.8% |
| 0.9 | 82.1% |
| 1.0 | 95.7% |

## 4. Go/No-Go 初步判断

### Go 候选

- **A/cheap**: 平均 Exact Match 85.0% ≥ 70%  ✓ Go 候选
- **A/mid**: 平均 Exact Match 80.6% ≥ 70%  ✓ Go 候选
- **A/opus**: 平均 Exact Match 87.9% ≥ 70%  ✓ Go 候选
- **B/mid**: 平均 Exact Match 80.1% ≥ 70%  ✓ Go 候选
- **B/opus**: 平均 Exact Match 86.8% ≥ 70%  ✓ Go 候选
- **C/cheap**: 平均 Exact Match 82.4% ≥ 70%  ✓ Go 候选
- **C/mid**: 平均 Exact Match 77.3% ≥ 70%  ✓ Go 候选
- **C/opus**: 平均 Exact Match 87.0% ≥ 70%  ✓ Go 候选