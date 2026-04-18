package main

import "github.com/leef-l/brain/cmd/brain/config"

type serveWorkdirPolicy = config.ServeWorkdirPolicy

const (
	serveWorkdirPolicyConfined = config.ServeWorkdirPolicyConfined
	serveWorkdirPolicyOpen     = config.ServeWorkdirPolicyOpen
)

var (
	parseServeWorkdirPolicy   = config.ParseServeWorkdirPolicy
	resolveServeWorkdirPolicy = config.ResolveServeWorkdirPolicy
	resolveServeRunWorkdir    = config.ResolveServeRunWorkdir
	canonicalDir              = config.CanonicalDir
)
