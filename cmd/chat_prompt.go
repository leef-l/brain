package main

import (
	"fmt"
	"strings"
	"time"

	"github.com/leef-l/brain/kernel"
	"github.com/leef-l/brain/tool"
)

// buildSystemPrompt constructs the L1 system prompt for the given mode.
func buildSystemPrompt(mode chatMode, sb *tool.Sandbox) string {
	base := "You are a coding assistant powered by BrainKernel.\n"
	base += fmt.Sprintf("Today's date: %s\n", time.Now().Format("2006-01-02"))

	if sb != nil {
		base += fmt.Sprintf("\nCurrent working directory: %s\n", sb.Primary())
		base += "All file paths are relative to this directory. " +
			"You already know the current directory — do NOT use shell_exec to run `pwd`.\n" +
			"File operations are sandboxed to this directory. " +
			"If you need files outside it, tell the user and they can authorize the directory.\n"
	}

	switch mode {
	case modePlan:
		return base +
			"\nYou are in PLAN mode (read-only). You can read files and search code " +
			"to understand the codebase, but you CANNOT modify files or execute commands. " +
			"When the user asks you to make changes, provide a detailed plan with " +
			"file paths and specific code changes, but do NOT use write_file or " +
			"shell_exec tools."
	case modeDefault:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. ALL tool operations " +
			"will require the user's explicit confirmation before proceeding. " +
			"Explain what you plan to do before using a tool."
	case modeAcceptEdits:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. File edits are " +
			"auto-approved, but shell commands will require confirmation. " +
			"Explain what you plan to do before using a tool."
	case modeAuto:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. Safe operations " +
			"are auto-approved. Use them freely. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	case modeRestricted:
		return base +
			"\nYou are in RESTRICTED mode. All file reads, searches, creates, edits, deletes, " +
			"and command-produced mutations are enforced by file policy. " +
			"Only operate within the explicitly allowed files and operations. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	case modeBypassPermissions:
		return base +
			"\nYou have access to tools for reading files, writing files, " +
			"searching code, and executing shell commands. All operations " +
			"proceed without confirmation. Use them freely. " +
			"Be concise. Briefly explain what you're doing before using a tool."
	}
	return base
}

// buildOrchestratorPrompt appends delegation instructions when specialist
// brains are available.
func buildOrchestratorPrompt(orch *kernel.Orchestrator, reg tool.Registry) string {
	if orch == nil || !registryHasTool(reg, "central.delegate") {
		return ""
	}

	kinds := orch.AvailableKinds()
	if len(kinds) == 0 {
		return ""
	}

	names := make([]string, len(kinds))
	for i, k := range kinds {
		names[i] = string(k)
	}

	prompt := "\n\n## Specialist Brain Delegation\n\n"
	prompt += "You have access to specialist brains that can handle specific tasks. "
	prompt += fmt.Sprintf("Available specialists: %s.\n\n", strings.Join(names, ", "))
	prompt += "Use the `central.delegate` tool to delegate tasks to the appropriate specialist:\n"
	prompt += "- **code**: For writing, editing, and debugging code. Delegate coding tasks to this brain.\n"
	prompt += "- **browser**: For web browsing, UI testing, and interacting with web pages. " +
		"This is a top-tier UI interaction specialist that can fully simulate human browser operations " +
		"(click, type, scroll, drag, hover, screenshot, etc.).\n"
	prompt += "- **verifier**: For running tests, verifying code changes, and checking output. " +
		"This brain is read-only and independent — it does not participate in implementation.\n"
	prompt += "- **fault**: For chaos engineering and fault injection testing.\n\n"
	prompt += "When you receive a task:\n"
	prompt += "1. Break it down into subtasks if needed\n"
	prompt += "2. Delegate each subtask to the appropriate specialist using `central.delegate`\n"
	prompt += "3. After code changes, delegate verification to the verifier brain\n"
	prompt += "4. Summarize the results to the user\n\n"
	prompt += "If a delegation is rejected (specialist unavailable), handle the task yourself.\n"

	// Add degradation notice if any specialists are missing.
	if notice := orch.DegradationNotice(); notice != "" {
		prompt += "\n" + notice + "\n"
	}

	return prompt
}

func registryHasTool(reg tool.Registry, name string) bool {
	if reg == nil {
		return false
	}
	_, ok := reg.Lookup(name)
	return ok
}
