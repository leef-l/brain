package main

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/leef-l/brain/sdk/agent"
	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/protocol"
	"github.com/leef-l/brain/sdk/tool"
)

// bridgeTool is a tool.Tool that lives in the main process (chat/run) and
// forwards execution to a specialist sidecar via orchestrator.CallTool.
// This allows the LLM to directly invoke specialist tools (e.g.
// quant.global_portfolio) without going through central.delegate.
type bridgeTool struct {
	schema tool.Schema
	risk   tool.Risk
	kind   agent.Kind
	orch   *kernel.Orchestrator
}

func (t *bridgeTool) Name() string        { return t.schema.Name }
func (t *bridgeTool) Schema() tool.Schema { return t.schema }
func (t *bridgeTool) Risk() tool.Risk     { return t.risk }

func (t *bridgeTool) Execute(ctx context.Context, args json.RawMessage) (*tool.Result, error) {
	if t.orch == nil || !t.orch.CanDelegate(t.kind) {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`{"error":"%s sidecar not available"}`, t.kind)),
			IsError: true,
		}, nil
	}

	result, err := t.orch.CallTool(ctx, &protocol.SpecialistToolCallRequest{
		TargetKind: t.kind,
		ToolName:   t.schema.Name,
		Arguments:  args,
	})
	if err != nil {
		return &tool.Result{
			Output:  json.RawMessage(fmt.Sprintf(`{"error":%q}`, err.Error())),
			IsError: true,
		}, nil
	}

	return &tool.Result{
		Output:  result.Output,
		IsError: result.IsError,
	}, nil
}

// specialistToolDef describes a tool to be bridged from a specialist sidecar.
type specialistToolDef struct {
	Name        string
	Description string
	Brain       string
	Kind        agent.Kind
	Risk        tool.Risk
	InputSchema json.RawMessage
}

// quantToolDefs defines all quant sidecar tools exposed to chat/run.
var quantToolDefs = []specialistToolDef{
	{
		Name:        "quant.global_portfolio",
		Description: "查询跨账户全局投资组合：总权益、各账户余额、持仓数、健康状态。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.global_risk_status",
		Description: "查询全局风控状态：风控阈值、当前敞口、限额使用比例。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.strategy_weights",
		Description: "查询各交易单元的策略列表和权重配置。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.daily_pnl",
		Description: "查询今日各交易单元的盈亏、交易数、胜率。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.account_status",
		Description: "查询指定账户的余额、保证金、持仓详情。account_id 为空时返回所有账户。支持实盘和模拟盘。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"account_id":{"type":"string","description":"账户 ID（如 paper-test, okx-main），为空返回所有"}
			}
		}`),
	},
	{
		Name:        "quant.trade_history",
		Description: "查询历史交易记录，可按单元、品种、方向过滤。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"unit_id":{"type":"string","description":"交易单元 ID，为空返回全部"},
				"symbol":{"type":"string","description":"品种过滤"},
				"direction":{"type":"string","description":"方向过滤: long/short"},
				"since":{"type":"string","description":"起始时间 (RFC3339)"},
				"limit":{"type":"integer","description":"最大返回条数，默认 100"}
			}
		}`),
	},
	{
		Name:        "quant.trace_query",
		Description: "查询信号审计追踪记录（策略信号→风控决策→执行结果完整链路）。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"symbol":{"type":"string","description":"品种过滤，为空返回全部"},
				"outcome":{"type":"string","description":"结果过滤: executed/rejected_risk/rejected_global/needs_review"},
				"since":{"type":"string","description":"起始时间 (RFC3339)，为空返回全部"},
				"limit":{"type":"integer","description":"最大返回条数，默认 50"}
			}
		}`),
	},
	{
		Name:        "quant.pause_trading",
		Description: "暂停所有交易单元（紧急止损时使用）。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.resume_trading",
		Description: "恢复所有交易单元。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "quant.account_pause",
		Description: "暂停指定账户的所有交易单元。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"account_id":{"type":"string","description":"账户 ID"}
			},
			"required":["account_id"]
		}`),
	},
	{
		Name:        "quant.account_resume",
		Description: "恢复指定账户的所有交易单元。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"account_id":{"type":"string","description":"账户 ID"}
			},
			"required":["account_id"]
		}`),
	},
	{
		Name:        "quant.account_close_all",
		Description: "市价平仓指定账户的所有持仓（危险操作）。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskCritical,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"account_id":{"type":"string","description":"账户 ID"}
			},
			"required":["account_id"]
		}`),
	},
	{
		Name:        "quant.force_close",
		Description: "强制平仓指定账户的指定品种持仓（危险操作）。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskCritical,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"account_id":{"type":"string","description":"账户 ID"},
				"symbol":{"type":"string","description":"品种 ID，如 BTC-USDT-SWAP"}
			},
			"required":["account_id","symbol"]
		}`),
	},
	{
		Name:        "quant.backtest_start",
		Description: "在历史 K 线数据上运行回测，返回策略表现报告。",
		Brain:       "quant",
		Kind:        agent.KindQuant,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"symbol":{"type":"string","description":"品种 ID"},
				"candles":{"type":"array","description":"K 线数据数组","items":{"type":"object"}}
			},
			"required":["symbol","candles"]
		}`),
	},
}

// dataToolDefs defines all data sidecar tools exposed to chat/run.
var dataToolDefs = []specialistToolDef{
	{
		Name:        "data.get_snapshot",
		Description: "查询指定品种的实时市场快照：价格、买卖盘、资金费率、微观结构指标。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instrument_id":{"type":"string","description":"品种 ID，如 BTC-USDT-SWAP"}
			},
			"required":["instrument_id"]
		}`),
	},
	{
		Name:        "data.get_candles",
		Description: "查询指定品种的历史 K 线数据（最近 500 根，返回最新 100 根）。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instrument_id":{"type":"string","description":"品种 ID"},
				"timeframe":{"type":"string","description":"时间框架：1m, 5m, 15m, 1H, 4H"}
			},
			"required":["instrument_id","timeframe"]
		}`),
	},
	{
		Name:        "data.get_feature_vector",
		Description: "获取指定品种的 192 维特征向量，含市场状态判别、异常检测、波动率分位。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instrument_id":{"type":"string","description":"品种 ID"}
			},
			"required":["instrument_id"]
		}`),
	},
	{
		Name:        "data.provider_health",
		Description: "查询数据源健康状态：WebSocket 连接、延迟、错误计数、数据质量指标。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "data.validation_stats",
		Description: "查询数据质量验证统计：拒绝数、写入数、错误数、特征计算耗时。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "data.backfill_status",
		Description: "查询历史数据回填进度：每个品种+时间框架的进度。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instrument_id":{"type":"string","description":"可选，指定品种"},
				"timeframe":{"type":"string","description":"可选，指定时间框架"}
			}
		}`),
	},
	{
		Name:        "data.active_instruments",
		Description: "查询当前活跃交易品种列表和 Ring Buffer 中的品种数量。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskSafe,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
	{
		Name:        "data.replay_start",
		Description: "启动历史数据回放（回测模式），从 PG 读取历史 K 线并以事件流方式重放。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{
			"type":"object",
			"properties":{
				"instrument_ids":{"type":"array","items":{"type":"string"},"description":"回放品种列表"},
				"timeframes":{"type":"array","items":{"type":"string"},"description":"回放时间框架列表"},
				"from_ts":{"type":"number","description":"起始时间戳（毫秒）"},
				"to_ts":{"type":"number","description":"结束时间戳（毫秒），0 = 到现在"},
				"speed":{"type":"number","description":"回放速度：0=最快, 1.0=实时, 10.0=10倍速"}
			},
			"required":["instrument_ids","from_ts"]
		}`),
	},
	{
		Name:        "data.replay_stop",
		Description: "停止当前活跃的历史回放。",
		Brain:       "data",
		Kind:        agent.KindData,
		Risk:        tool.RiskMedium,
		InputSchema: json.RawMessage(`{"type":"object","properties":{}}`),
	},
}

// registerSpecialistBridgeTools registers bridge tools for all available
// specialist brains into the tool registry. This makes specialist tools
// directly callable by the LLM in chat/run without going through
// central.delegate.
func registerSpecialistBridgeTools(reg tool.Registry, orch *kernel.Orchestrator) {
	if reg == nil || orch == nil {
		return
	}

	var defs []specialistToolDef
	if orch.CanDelegate(agent.KindQuant) {
		defs = append(defs, quantToolDefs...)
	}
	if orch.CanDelegate(agent.KindData) {
		defs = append(defs, dataToolDefs...)
	}

	for _, d := range defs {
		_ = reg.Register(&bridgeTool{
			schema: tool.Schema{
				Name:        d.Name,
				Description: d.Description,
				Brain:       d.Brain,
				InputSchema: d.InputSchema,
			},
			risk: d.Risk,
			kind: d.Kind,
			orch: orch,
		})
	}
}
