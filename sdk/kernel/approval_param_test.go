package kernel

import (
	"context"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

func TestParamRuleDenyRmRf(t *testing.T) {
	approver := &DefaultSemanticApprover{
		ParamRules: []ParamRule{
			{
				ToolPattern: "*.execute_shell",
				ArgKey:      "command",
				ValueGlob:   "rm -rf **",
				Deny:        true,
				Reason:      "blanket rm -rf is never allowed",
			},
		},
	}
	req := ApprovalRequest{
		CallerKind: agent.Kind("central"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
		Args:       map[string]string{"command": "rm -rf /tmp/build"},
	}
	d := approver.Approve(context.Background(), req)
	if d.Granted {
		t.Fatal("rm -rf must be denied")
	}
	if d.Reason == "" {
		t.Error("expected reason")
	}
}

func TestParamRuleUpgradesShellClass(t *testing.T) {
	// 默认 shell 是 exec-capable,code brain 允许;用 param rule 升级到 control-plane,
	// 这样 code brain 就拒绝,只有 central 可以。
	approver := &DefaultSemanticApprover{
		ParamRules: []ParamRule{
			{
				ToolPattern: "*.execute_shell",
				ArgKey:      "command",
				ValueGlob:   "sudo **",
				Class:       ApprovalControlPlane,
				Reason:      "sudo 提权升级到 control-plane",
			},
		},
	}
	// code brain 调 sudo → 应被拒绝(control-plane 只允 central)
	codeReq := ApprovalRequest{
		CallerKind: agent.Kind("code"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
		Args:       map[string]string{"command": "sudo systemctl restart x"},
	}
	if d := approver.Approve(context.Background(), codeReq); d.Granted {
		t.Error("sudo from code must be rejected after upgrade")
	}
	// 非 sudo 命令保持原 exec-capable,code 可以
	codeNormal := ApprovalRequest{
		CallerKind: agent.Kind("code"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
		Args:       map[string]string{"command": "go test ./..."},
	}
	if d := approver.Approve(context.Background(), codeNormal); !d.Granted {
		t.Errorf("normal go test should pass, got %+v", d)
	}
}

func TestParamRuleAllowAsWhitelist(t *testing.T) {
	// browser.navigate 默认 external-network,只有 central + browser 能用;
	// 对白名单内网地址降到 readonly,让 data brain 也可用。
	approver := &DefaultSemanticApprover{
		ParamRules: []ParamRule{
			{
				ToolPattern: "browser.navigate",
				ArgKey:      "url",
				ValueGlob:   "http://localhost**",
				AllowAs:     ApprovalReadonly,
			},
		},
	}
	req := ApprovalRequest{
		CallerKind: agent.Kind("data"),
		TargetKind: agent.Kind("browser"),
		ToolName:   "browser.navigate",
		Args:       map[string]string{"url": "http://localhost:8080/api"},
	}
	if d := approver.Approve(context.Background(), req); !d.Granted {
		t.Errorf("localhost whitelist should allow data brain, got %+v", d)
	}
	// 非 localhost 的外网地址仍然走 external-network,data 被拒
	out := ApprovalRequest{
		CallerKind: agent.Kind("data"),
		TargetKind: agent.Kind("browser"),
		ToolName:   "browser.navigate",
		Args:       map[string]string{"url": "https://evil.com"},
	}
	if d := approver.Approve(context.Background(), out); d.Granted {
		t.Error("external URL should still require external-network")
	}
}

func TestParamRuleMissingArgsFallsThrough(t *testing.T) {
	approver := &DefaultSemanticApprover{
		ParamRules: []ParamRule{
			{ToolPattern: "*.execute_shell", ArgKey: "command", ValueGlob: "rm **", Deny: true},
		},
	}
	// Args 不存在,原逻辑必须生效(code.execute_shell 对 code 是允许的)
	req := ApprovalRequest{
		CallerKind: agent.Kind("code"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
	}
	if d := approver.Approve(context.Background(), req); !d.Granted {
		t.Errorf("no args should fall through to class-level decision, got %+v", d)
	}
}

func TestParamRuleOrderMatters(t *testing.T) {
	// 先匹配的规则先生效:放行白名单 sudo 命令列表,其他 sudo 拒绝
	approver := &DefaultSemanticApprover{
		ParamRules: []ParamRule{
			{ToolPattern: "*.execute_shell", ArgKey: "command", ValueGlob: "sudo -n systemctl **", AllowAs: ApprovalExecCapable},
			{ToolPattern: "*.execute_shell", ArgKey: "command", ValueGlob: "sudo **", Deny: true},
		},
	}
	whitelisted := ApprovalRequest{
		CallerKind: agent.Kind("code"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
		Args:       map[string]string{"command": "sudo -n systemctl status nginx"},
	}
	if d := approver.Approve(context.Background(), whitelisted); !d.Granted {
		t.Errorf("whitelisted sudo should pass, got %+v", d)
	}

	blocked := ApprovalRequest{
		CallerKind: agent.Kind("code"),
		TargetKind: agent.Kind("code"),
		ToolName:   "code.execute_shell",
		Args:       map[string]string{"command": "sudo rm /etc/passwd"},
	}
	if d := approver.Approve(context.Background(), blocked); d.Granted {
		t.Error("non-whitelisted sudo must be denied")
	}
}
