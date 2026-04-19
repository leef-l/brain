package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runPattern(args []string) int {
	return cmds.RunPattern(args, cmds.DefaultPatternDeps())
}
