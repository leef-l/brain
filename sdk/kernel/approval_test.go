package kernel

import (
	"context"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

// ────────────────────────────────────────────────────
// 辅助函数
// ────────────────────────────────────────────────────

func newApprover() *DefaultSemanticApprover {
	return &DefaultSemanticApprover{}
}

func approve(t *testing.T, approver *DefaultSemanticApprover, req ApprovalRequest) ApprovalDecision {
	t.Helper()
	return approver.Approve(context.Background(), req)
}

// ────────────────────────────────────────────────────
// 1. readonly 工具任何调用者都通过
// ────────────────────────────────────────────────────

func TestReadonly_AnyCallerGranted(t *testing.T) {
	approver := newApprover()
	callers := []agent.Kind{
		agent.KindCentral, agent.KindCode, agent.KindBrowser,
		agent.KindVerifier, agent.KindData, agent.KindQuant,
	}
	for _, caller := range callers {
		req := ApprovalRequest{
			CallerKind: caller,
			TargetKind: agent.KindData,
			ToolName:   "data.get_candles",
			Class:      ApprovalReadonly,
		}
		dec := approve(t, approver, req)
		if !dec.Granted {
			t.Errorf("readonly 工具应对 %s 放行，但被拒绝: %s", caller, dec.Reason)
		}
	}
}

// ────────────────────────────────────────────────────
// 2. control-plane 工具非 central 调用者被拒
// ────────────────────────────────────────────────────

func TestControlPlane_OnlyCentral(t *testing.T) {
	approver := newApprover()

	// central 应通过
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindQuant,
		ToolName:   "quant.place_order",
		Class:      ApprovalControlPlane,
	})
	if !dec.Granted {
		t.Errorf("central 调用 control-plane 应通过，但被拒绝: %s", dec.Reason)
	}

	// 其他 kind 应被拒绝
	rejected := []agent.Kind{
		agent.KindCode, agent.KindBrowser, agent.KindVerifier,
		agent.KindData, agent.KindQuant,
	}
	for _, caller := range rejected {
		dec := approve(t, approver, ApprovalRequest{
			CallerKind: caller,
			TargetKind: agent.KindQuant,
			ToolName:   "quant.place_order",
			Class:      ApprovalControlPlane,
		})
		if dec.Granted {
			t.Errorf("control-plane 工具应拒绝 %s，但被放行", caller)
		}
	}
}

// ────────────────────────────────────────────────────
// 3. exec-capable 工具 central 和 code 通过，其他拒绝
// ────────────────────────────────────────────────────

func TestExecCapable_CentralAndCode(t *testing.T) {
	approver := newApprover()

	// central 通过
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindCode,
		ToolName:   "code.execute_shell",
		Class:      ApprovalExecCapable,
	})
	if !dec.Granted {
		t.Errorf("central 调用 exec-capable 应通过: %s", dec.Reason)
	}

	// code 通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCode,
		TargetKind: agent.KindCode,
		ToolName:   "code.execute_shell",
		Class:      ApprovalExecCapable,
	})
	if !dec.Granted {
		t.Errorf("code 调用 exec-capable 应通过: %s", dec.Reason)
	}

	// browser 被拒绝
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindBrowser,
		TargetKind: agent.KindCode,
		ToolName:   "code.execute_shell",
		Class:      ApprovalExecCapable,
	})
	if dec.Granted {
		t.Errorf("browser 调用 exec-capable 应被拒绝")
	}
}

// ────────────────────────────────────────────────────
// 4. external-network 工具 central 和 browser 通过
// ────────────────────────────────────────────────────

func TestExternalNetwork_CentralAndBrowser(t *testing.T) {
	approver := newApprover()

	// central 通过
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.navigate",
		Class:      ApprovalExternalNetwork,
	})
	if !dec.Granted {
		t.Errorf("central 调用 external-network 应通过: %s", dec.Reason)
	}

	// browser 通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindBrowser,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.navigate",
		Class:      ApprovalExternalNetwork,
	})
	if !dec.Granted {
		t.Errorf("browser 调用 external-network 应通过: %s", dec.Reason)
	}

	// quant 被拒绝
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindQuant,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.navigate",
		Class:      ApprovalExternalNetwork,
	})
	if dec.Granted {
		t.Errorf("quant 调用 external-network 应被拒绝")
	}
}

// ────────────────────────────────────────────────────
// 5. 前缀启发式正确分类
// ────────────────────────────────────────────────────

func TestPrefixHeuristic_InferClass(t *testing.T) {
	tests := []struct {
		toolName string
		want     ApprovalClass
	}{
		// L0: readonly
		{"data.get_candles", ApprovalReadonly},
		{"data.list_instruments", ApprovalReadonly},
		{"code.read_file", ApprovalReadonly},
		{"quant.list_orders", ApprovalReadonly},
		{"quant.get_position", ApprovalReadonly},

		// L1: workspace-write
		{"code.write_file", ApprovalWorkspaceWrite},
		{"code.patch_file", ApprovalWorkspaceWrite},
		{"code.delete_file", ApprovalWorkspaceWrite},

		// L2: exec-capable
		{"code.execute_shell", ApprovalExecCapable},
		{"code.run_tests", ApprovalExecCapable},
		{"fault.exec_diagnostic", ApprovalExecCapable},

		// L3: control-plane
		{"quant.place_order", ApprovalControlPlane},
		{"quant.cancel_order", ApprovalControlPlane},
		{"exchange.withdraw", ApprovalControlPlane},

		// L4: external-network
		{"browser.navigate", ApprovalExternalNetwork},
		{"browser.form_submit", ApprovalExternalNetwork},
		{"fetch.post_webhook", ApprovalExternalNetwork},
		{"http.get", ApprovalExternalNetwork},

		// 未知工具 → 保守默认 workspace-write
		{"unknown.tool", ApprovalWorkspaceWrite},
	}
	for _, tt := range tests {
		got := inferClassByPrefix(tt.toolName)
		if got != tt.want {
			t.Errorf("inferClassByPrefix(%q) = %q, want %q", tt.toolName, got, tt.want)
		}
	}
}

// ────────────────────────────────────────────────────
// 6. 前缀启发式 + 授权矩阵联合测试
// ────────────────────────────────────────────────────

func TestHeuristic_WithAuthorization(t *testing.T) {
	approver := newApprover()

	// 不带 Class 的请求 → 应走启发式推断
	// central 调用 "central.restart_sidecar"（无明确前缀规则 → 默认 workspace-write → 通过）
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindCentral,
		ToolName:   "central.restart_sidecar",
	})
	if !dec.Granted {
		t.Errorf("central 调用未知工具应按 workspace-write 通过: %s", dec.Reason)
	}

	// data brain 调用 browser.navigate → 启发式推断为 external-network → data 被拒
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindData,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.navigate",
	})
	if dec.Granted {
		t.Errorf("data 调用 browser.navigate 应被拒绝（external-network）")
	}

	// quant brain 调用 data.get_candles → 启发式推断为 readonly → 通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindQuant,
		TargetKind: agent.KindData,
		ToolName:   "data.get_candles",
	})
	if !dec.Granted {
		t.Errorf("quant 调用 data.get_candles 应按 readonly 通过: %s", dec.Reason)
	}
}

// ────────────────────────────────────────────────────
// 7. maxClass 只升不降
// ────────────────────────────────────────────────────

func TestMaxClass(t *testing.T) {
	tests := []struct {
		a, b ApprovalClass
		want ApprovalClass
	}{
		{ApprovalReadonly, ApprovalControlPlane, ApprovalControlPlane},
		{ApprovalExternalNetwork, ApprovalReadonly, ApprovalExternalNetwork},
		{ApprovalWorkspaceWrite, ApprovalWorkspaceWrite, ApprovalWorkspaceWrite},
		{ApprovalExecCapable, ApprovalControlPlane, ApprovalControlPlane},
	}
	for _, tt := range tests {
		got := maxClass(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("maxClass(%q, %q) = %q, want %q", tt.a, tt.b, got, tt.want)
		}
	}
}

// ────────────────────────────────────────────────────
// 8. ManifestMinLevel 预留接口测试
// ────────────────────────────────────────────────────

func TestManifestMinLevel_Upgrade(t *testing.T) {
	approver := &DefaultSemanticApprover{
		// 模拟 browser brain 的 manifest 声明最低 external-network
		ManifestMinLevel: func(targetKind agent.Kind) ApprovalClass {
			if targetKind == agent.KindBrowser {
				return ApprovalExternalNetwork
			}
			return ""
		},
	}

	// browser.get_title 本身会被启发式推断为 external-network（browser.* 前缀）
	// 但即使显式声明为 readonly，manifest 也应将其提升到 external-network
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindQuant,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.get_title",
		Class:      ApprovalReadonly, // 工具声明 readonly
	})
	// manifest 提升到 external-network，quant 不在允许列表中 → 拒绝
	if dec.Granted {
		t.Errorf("manifest 应将 readonly 提升为 external-network，quant 应被拒绝")
	}

	// central 调用同一工具应通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindBrowser,
		ToolName:   "browser.get_title",
		Class:      ApprovalReadonly,
	})
	if !dec.Granted {
		t.Errorf("central 调用 browser 工具应通过（即使提升到 external-network）: %s", dec.Reason)
	}
}

// ────────────────────────────────────────────────────
// 9. workspace-write 授权规则
// ────────────────────────────────────────────────────

func TestWorkspaceWrite_CentralAlwaysGranted(t *testing.T) {
	approver := newApprover()
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindCentral,
		TargetKind: agent.KindCode,
		ToolName:   "code.write_file",
		Class:      ApprovalWorkspaceWrite,
	})
	if !dec.Granted {
		t.Errorf("central 调用 workspace-write 应通过: %s", dec.Reason)
	}
}

// ────────────────────────────────────────────────────
// 10. 向后兼容：旧白名单 4 条规则的语义映射
// ────────────────────────────────────────────────────

func TestBackwardCompat_OldWhitelistRules(t *testing.T) {
	approver := newApprover()

	// 规则1: verifier → browser.* (external-network) — verifier 不在 external-network 允许列表
	// 这是预期行为：新系统中 verifier → browser 需要通过旧的 SpecialistToolCallAuthorizer 或新规则
	// 在新系统中，browser.* 被推断为 external-network，verifier 没有权限

	// 规则2: quant → data.get_* (readonly) — 任何人都通过
	dec := approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindQuant,
		TargetKind: agent.KindData,
		ToolName:   "data.get_candles",
	})
	if !dec.Granted {
		t.Errorf("quant → data.get_candles (readonly) 应通过: %s", dec.Reason)
	}

	// 规则3: quant → central.review_trade (workspace-write 默认) — 通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindQuant,
		TargetKind: agent.KindCentral,
		ToolName:   "central.review_trade",
	})
	if !dec.Granted {
		t.Errorf("quant → central.review_trade 应通过: %s", dec.Reason)
	}

	// 规则4: data → central.data_alert (workspace-write 默认) — 通过
	dec = approve(t, approver, ApprovalRequest{
		CallerKind: agent.KindData,
		TargetKind: agent.KindCentral,
		ToolName:   "central.data_alert",
	})
	if !dec.Granted {
		t.Errorf("data → central.data_alert 应通过: %s", dec.Reason)
	}
}
