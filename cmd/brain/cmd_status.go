package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runStatus(args []string) int {
	return cmds.RunStatus(args, loadPersistedRun)
}
