package main

import (
	"fmt"
	"strings"
)

// permissionMode represents the shared permission level for tool execution.
// It is used by chat/run/serve so the security model stays identical.
type permissionMode string

// chatMode is kept as an alias for backward compatibility inside the chat UI.
type chatMode = permissionMode

const (
	modePlan              permissionMode = "plan"
	modeDefault           permissionMode = "default"
	modeAcceptEdits       permissionMode = "accept-edits"
	modeAuto              permissionMode = "auto"
	modeRestricted        permissionMode = "restricted"
	modeBypassPermissions permissionMode = "bypass-permissions"
)

// allModes in cycling order.
var allModes = []permissionMode{
	modePlan,
	modeDefault,
	modeAcceptEdits,
	modeAuto,
	modeRestricted,
	modeBypassPermissions,
}

func parsePermissionMode(s string) (permissionMode, error) {
	switch strings.ToLower(s) {
	case "plan":
		return modePlan, nil
	case "default":
		return modeDefault, nil
	case "accept-edits", "acceptedits":
		return modeAcceptEdits, nil
	case "auto":
		return modeAuto, nil
	case "restricted":
		return modeRestricted, nil
	case "acceptedits+sandbox":
		return modeAcceptEdits, nil
	case "bypasspermissions+sandbox":
		return modeBypassPermissions, nil
	case "bypass-permissions", "bypasspermissions", "bypass":
		return modeBypassPermissions, nil
	default:
		return "", fmt.Errorf("unknown mode %q (use plan, default, accept-edits, auto, restricted, or bypass-permissions)", s)
	}
}

func parseChatMode(s string) (chatMode, error) {
	return parsePermissionMode(s)
}

func (m permissionMode) label() string {
	switch m {
	case modePlan:
		return "plan (read-only)"
	case modeDefault:
		return "default (always confirm)"
	case modeAcceptEdits:
		return "accept-edits (auto-approve edits)"
	case modeAuto:
		return "auto (sandboxed auto-approve)"
	case modeRestricted:
		return "restricted (file-policy enforced)"
	case modeBypassPermissions:
		return "bypass-permissions (no confirmation, sandbox still enforced)"
	}
	return string(m)
}

func (m permissionMode) styledLabel() string {
	switch m {
	case modePlan:
		return "\033[1;33m>\033[0m plan"
	case modeDefault:
		return "\033[1;36m>\033[0m default"
	case modeAcceptEdits:
		return "\033[1;32m>\033[0m accept-edits"
	case modeAuto:
		return "\033[1;35m>\033[0m auto"
	case modeRestricted:
		return "\033[1;34m>\033[0m restricted"
	case modeBypassPermissions:
		return "\033[1;31m>\033[0m bypass"
	}
	return m.label()
}

// cycleMode rotates through: plan → default → accept-edits → auto → bypass → plan.
func cycleMode(m permissionMode) permissionMode {
	for i, mode := range allModes {
		if mode == m {
			return allModes[(i+1)%len(allModes)]
		}
	}
	return modeDefault
}
