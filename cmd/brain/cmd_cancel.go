package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runCancel(args []string) int {
	return cmds.RunCancel(args, loadPersistedRun)
}
