package cliruntime

import (
	"fmt"
	"path/filepath"

	"github.com/leef-l/brain/sdk/kernel"
	"github.com/leef-l/brain/sdk/persistence"
	"github.com/leef-l/brain/sdk/security"
	"github.com/leef-l/brain/sdk/tool"
)

type Runtime struct {
	Kernel       *kernel.Kernel
	FileStore    *persistence.FileStore
	RunStore     *Store
	ArtifactRoot string
}

type Backend interface {
	Open(brainKind string) (*Runtime, error)
}

type FileBackend struct {
	DataDir string
}

func NewDefaultRuntime(brainKind, dataDir string) (*Runtime, error) {
	return (&FileBackend{DataDir: dataDir}).Open(brainKind)
}

func (b *FileBackend) Open(brainKind string) (*Runtime, error) {
	if b == nil {
		return nil, fmt.Errorf("runtime backend is nil")
	}
	storePath := filepath.Join(b.DataDir, "store.json")
	runStatePath := filepath.Join(b.DataDir, "runs.json")
	artifactRoot := filepath.Join(b.DataDir, "artifacts")

	fileStore, err := persistence.OpenFileStore(storePath)
	if err != nil {
		return nil, fmt.Errorf("open file store: %w", err)
	}
	runStore, err := OpenStore(runStatePath)
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

	return &Runtime{
		Kernel:       k,
		FileStore:    fileStore,
		RunStore:     runStore,
		ArtifactRoot: artifactRoot,
	}, nil
}
