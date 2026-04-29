// coupling_matrix.go — 多变量耦合矩阵（Coupling Matrix）
//
// 钱学森《工程控制论》主线3（多变量耦合）的落地载体。
// Brain 系统由多个 specialist brain 组成，它们之间存在隐式耦合：
//   - CentralBrain 失败会导致大量重试，推高其他 brain 的负载
//   - CodeBrain 编译请求激增会连带提高 BrowserBrain 的文档查询量
//   - QuantBrain 高负载时 DataBrain 的数据供给成为瓶颈
//
// CouplingMatrix 量化这些耦合关系，供 FeedbackController 在决策时
// 预判"控制动作对 A 的副作用是否会通过耦合链放大到 B"。
package kernel

import (
	"sync"

	"github.com/leef-l/brain/sdk/agent"
)

// ---------------------------------------------------------------------------
// CouplingMatrix — 耦合矩阵
// ---------------------------------------------------------------------------

// CouplingMatrix 维护 brain 间的耦合系数。
// coefficients[source][target] 表示 source brain 的状态变化对 target 的影响权重。
// 正值 = 同向影响（source 负载增加 → target 负载增加）
// 负值 = 反向影响（source 负载增加 → target 负载减少）
type CouplingMatrix struct {
	mu            sync.RWMutex
	coefficients  map[string]map[string]float64 // kind → kind → weight
	lastState     *SystemState                  // 上一次的系统状态，用于在线学习
}

// NewCouplingMatrix 创建空的耦合矩阵。
func NewCouplingMatrix() *CouplingMatrix {
	return &CouplingMatrix{
		coefficients: make(map[string]map[string]float64),
		lastState:    &SystemState{Brains: make(map[agent.Kind]BrainUnitState)},
	}
}

// DefaultCouplingMatrix 返回基于当前 Brain 架构先验知识的默认耦合矩阵。
func DefaultCouplingMatrix() *CouplingMatrix {
	cm := NewCouplingMatrix()
	// Central 作为协调者，其高负载会向所有 specialist 传递任务压力
	cm.Set("central", "code", 0.40)
	cm.Set("central", "browser", 0.30)
	cm.Set("central", "quant", 0.20)
	cm.Set("central", "data", 0.15)
	cm.Set("central", "verifier", 0.25)
	cm.Set("central", "fault", 0.10)

	// CodeBrain 编译失败时 Central 会重试，形成正反馈环（需特别注意）
	cm.Set("code", "central", 0.35)

	// BrowserBrain 页面操作失败时 Central 会重试
	cm.Set("browser", "central", 0.25)

	// QuantBrain 高负载时会向 DataBrain 拉取大量历史数据
	cm.Set("quant", "data", 0.50)

	// DataBrain 响应慢会导致 QuantBrain 阻塞等待
	cm.Set("data", "quant", 0.30)

	// VerifierBrain 验证失败会触发 Central 重新规划
	cm.Set("verifier", "central", 0.20)

	// FaultBrain 故障注入会临时降低所有 brain 的可用性
	cm.Set("fault", "central", -0.15)
	cm.Set("fault", "code", -0.10)
	cm.Set("fault", "browser", -0.10)
	cm.Set("fault", "quant", -0.10)
	cm.Set("fault", "data", -0.10)

	return cm
}

// Set 设置 source → target 的耦合系数。
func (cm *CouplingMatrix) Set(source, target string, weight float64) {
	cm.mu.Lock()
	defer cm.mu.Unlock()
	if cm.coefficients[source] == nil {
		cm.coefficients[source] = make(map[string]float64)
	}
	cm.coefficients[source][target] = weight
}

// Get 读取 source → target 的耦合系数。
func (cm *CouplingMatrix) Get(source, target string) float64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	if cm.coefficients[source] == nil {
		return 0
	}
	return cm.coefficients[source][target]
}

// EstimateImpact 估算 source brain 发生 delta 变化时，对所有 brain 的级联影响。
// 返回 map[kind] = 预估变化量（一阶近似，不考虑高阶反馈）。
func (cm *CouplingMatrix) EstimateImpact(source agent.Kind, delta float64) map[agent.Kind]float64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	out := make(map[agent.Kind]float64)
	src := string(source)
	for target, weight := range cm.coefficients[src] {
		out[agent.Kind(target)] = delta * weight
	}
	return out
}

// Propagate 将一组控制动作的副作用通过耦合矩阵传播，返回预估的次级影响。
// 用于 FeedbackController 在生成动作前评估"动作 A 是否会通过耦合链恶化 B"。
func (cm *CouplingMatrix) Propagate(actions []ControlAction) map[agent.Kind]float64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()

	// 简化模型：将动作强度归一化为 [-1, 1] 的 delta
	out := make(map[agent.Kind]float64)
	for _, action := range actions {
		delta := actionToDelta(action)
		if delta == 0 {
			continue
		}
		impacts := cm.EstimateImpact(agent.Kind(action.TargetKind), delta)
		for kind, impact := range impacts {
			out[kind] += impact
		}
	}
	return out
}

// actionToDelta 将控制动作映射为归一化的变化量。
func actionToDelta(a ControlAction) float64 {
	switch a.Action {
	case ActionThrottle:
		return -0.5 // 限流 → 该 brain 负载下降
	case ActionBoost:
		return +0.5 // 增压 → 该 brain 负载上升
	case ActionRestart:
		return -0.8 // 重启 → 该 brain 暂时下线
	case ActionReroute:
		return -0.3 // 重路由 → 该 brain 负载部分转移
	default:
		return 0
	}
}

// Update 根据最新 SystemState 和控制动作历史，在线估计/调整耦合系数。
// 采用简化梯度下降：比较预测影响与实际负载变化，微调耦合系数。
func (cm *CouplingMatrix) Update(state *SystemState, actions []ControlAction) {
	if state == nil {
		return
	}
	cm.mu.Lock()
	defer cm.mu.Unlock()

	if cm.lastState == nil {
		cm.lastState = state.Clone()
		return
	}

	const lr = 0.05 // 学习率

	for _, action := range actions {
		srcDelta := actionToDelta(action)
		if srcDelta == 0 {
			continue
		}
		src := action.TargetKind
		if src == "" {
			continue
		}

		// 遍历该 source 已知的所有 target
		for target, coeff := range cm.coefficients[src] {
			predicted := coeff * srcDelta

			// 用 Load 变化作为实际变化的代理
			var actual float64
			if lastBs, ok := cm.lastState.Brains[agent.Kind(target)]; ok {
				if currBs, ok2 := state.Brains[agent.Kind(target)]; ok2 {
					actual = currBs.Load - lastBs.Load
				}
			}

			err := predicted - actual
			newCoeff := coeff - lr*err*srcDelta
			if newCoeff > 1.0 {
				newCoeff = 1.0
			} else if newCoeff < -1.0 {
				newCoeff = -1.0
			}
			if cm.coefficients[src] == nil {
				cm.coefficients[src] = make(map[string]float64)
			}
			cm.coefficients[src][target] = newCoeff
		}
	}

	cm.lastState = state.Clone()
}

// Snapshot 返回耦合矩阵的防御性拷贝。
func (cm *CouplingMatrix) Snapshot() map[string]map[string]float64 {
	cm.mu.RLock()
	defer cm.mu.RUnlock()
	out := make(map[string]map[string]float64, len(cm.coefficients))
	for s, targets := range cm.coefficients {
		tCopy := make(map[string]float64, len(targets))
		for t, w := range targets {
			tCopy[t] = w
		}
		out[s] = tCopy
	}
	return out
}
