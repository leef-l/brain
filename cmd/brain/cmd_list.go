package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runList(args []string) int {
	return cmds.RunList(args, newDefaultCLIRuntime)
}
