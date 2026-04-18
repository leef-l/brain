package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runReplay(args []string) int {
	return cmds.RunReplay(args, loadPersistedRun)
}
