package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runDemo(args []string) int {
	return cmds.RunDemo(args, cmds.DefaultDemoDeps())
}
