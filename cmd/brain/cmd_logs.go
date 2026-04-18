package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runLogs(args []string) int {
	return cmds.RunLogs(args, loadPersistedRun)
}
