package main

import (
	"context"
	"path/filepath"

	"github.com/leef-l/brain/cmd/brain/cliruntime"
	"github.com/leef-l/brain/sdk/loop"
	"github.com/leef-l/brain/sdk/persistence"
)

type persistedRunEvent = cliruntime.RunEvent
type persistedRunRecord = cliruntime.RunRecord
type runtimeStore = cliruntime.Store
type cliRuntime = cliruntime.Runtime
type cliRuntimeBackend = cliruntime.Backend

var openRuntimeStore = cliruntime.OpenStore

func newDefaultCLIRuntime(brainKind string) (*cliRuntime, error) {
	return cliruntime.NewDefaultRuntime(brainKind, filepath.Dir(configPath()))
}

func defaultRuntimeBackend() cliRuntimeBackend {
	return &cliruntime.FileBackend{DataDir: filepath.Dir(configPath())}
}

func loadPersistedRun(id string) (*cliRuntime, *persistedRunRecord, *persistence.Checkpoint, error) {
	return cliruntime.LoadPersistedRun(id, filepath.Dir(configPath()))
}

func saveRunCheckpoint(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, state string, turnIndex int, turnUUID string) error {
	return cliruntime.SaveRunCheckpoint(ctx, k, rec, state, turnIndex, turnUUID)
}

func saveRunUsage(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, provider, model string, result *loop.RunResult) error {
	return cliruntime.SaveRunUsage(ctx, k, rec, provider, model, result)
}

func saveRunPlan(ctx context.Context, k *cliRuntime, rec *persistedRunRecord, payload map[string]interface{}) (int64, error) {
	return cliruntime.SaveRunPlan(ctx, k, rec, payload)
}
