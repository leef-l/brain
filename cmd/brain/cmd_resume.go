package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runResume(args []string) int {
	return cmds.RunResume(args, loadPersistedRun, loadConfig)
}
