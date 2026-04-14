package main

import (
	"fmt"
	"path/filepath"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/security"
	"github.com/leef-l/brain/sdk/tool"
)

type cliRuntime struct {
	Kernel       *kernel.Kernel
	FileStore    *persistence.FileStore
	RunStore     *runtimeStore
	ArtifactRoot string
}

type cliRuntimeBackend interface {
	Open(brainKind string) (*cliRuntime, error)
}

type fileCLIRuntimeBackend struct {
	dataDir string
}

func newDefaultCLIRuntime(brainKind string) (*cliRuntime, error) {
	return defaultRuntimeBackend().Open(brainKind)
}

func defaultRuntimeBackend() cliRuntimeBackend {
	return &fileCLIRuntimeBackend{dataDir: filepath.Dir(configPath())}
}

func (b *fileCLIRuntimeBackend) Open(brainKind string) (*cliRuntime, error) {
	if b == nil {
		return nil, fmt.Errorf("runtime backend is nil")
	}
	storePath := filepath.Join(b.dataDir, "store.json")
	runStatePath := filepath.Join(b.dataDir, "runs.json")
	artifactRoot := filepath.Join(b.dataDir, "artifacts")

	fileStore, err := persistence.OpenFileStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("open file store: %w", err)
	}
	runStore, err := openRuntimeStore(runStatePath)
	if err != nil {
		return nil, fmt.Errorf("open run store: %w", err)
	}

	metaStore := fileStore.MetaStore()
	k := kernel.NewKernel(
		kernel.WithPlanStore(fileStore.PlanStore()),
		kernel.WithArtifactMetaStore(metaStore),
		kernel.WithArtifactStore(persistence.NewFSArtifactStore(artifactRoot, metaStore, nil)),
		kernel.WithRunCheckpointStore(fileStore.CheckpointStore()),
		kernel.WithUsageLedger(fileStore.Ledger()),
		kernel.WithToolRegistry(tool.NewMemRegistry()),
		kernel.WithAuditLogger(security.NewHashChainAuditLogger()),
	)

	return &cliRuntime{
		Kernel:       k,
		FileStore:    fileStore,
		RunStore:     runStore,
		ArtifactRoot: artifactRoot,
	}, nil
}
