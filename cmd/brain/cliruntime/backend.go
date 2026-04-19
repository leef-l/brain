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
	Stores       *persistence.ClosableStores
	ArtifactRoot string
}

type Backend interface {
	Open(brainKind string) (*Runtime, error)
}

type FileBackend struct {
	DataDir string
}

func NewDefaultRuntime(brainKind, dataDir string) (*Runtime, error) {
	return (&SQLiteBackend{DataDir: dataDir}).Open(brainKind)
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

// SQLiteBackend uses SQLite WAL as the unified persistence backend.
type SQLiteBackend struct {
	DataDir string
}

func (b *SQLiteBackend) Open(brainKind string) (*Runtime, error) {
	if b == nil {
		return nil, fmt.Errorf("runtime backend is nil")
	}

	dsn := filepath.Join(b.DataDir, "brain.db")
	stores, err := persistence.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}

	// JSON run store for backward compatibility during migration
	runStatePath := filepath.Join(b.DataDir, "runs.json")
	runStore, err := OpenStore(runStatePath)
	if err != nil {
		stores.Close()
		return nil, fmt.Errorf("open run store: %w", err)
	}

	k := kernel.NewKernel(
		kernel.WithPersistence(stores.Stores),
		kernel.WithToolRegistry(tool.NewMemRegistry()),
		kernel.WithAuditLogger(security.NewHashChainAuditLogger()),
	)

	return &Runtime{
		Kernel:       k,
		RunStore:     runStore,
		Stores:       stores,
		ArtifactRoot: filepath.Join(b.DataDir, "artifacts"),
	}, nil
}
