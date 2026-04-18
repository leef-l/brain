package main

import cmds "github.com/leef-l/brain/cmd/brain/command"

func runDoctor(args []string) int {
	return cmds.RunDoctor(args, cmds.DoctorDeps{
		ConfigPath:  configPath,
		LoadConfig:  loadConfig,
		NewRuntime:  newDefaultCLIRuntime,
		BinResolver: defaultBinResolver,
	})
}
