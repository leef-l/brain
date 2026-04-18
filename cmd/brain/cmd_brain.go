package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runBrainManage(args []string) int {
	return cmds.RunBrainManage(args, cmds.BrainManageDeps{
		BrainsDir: cmds.BrainManageDir,
	})
}
