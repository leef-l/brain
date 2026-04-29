package persistence

import (
	"fmt"
	"os"
	"path/filepath"
)

// fileDriver implements Driver for the JSON-file backend.
// DSN is the file path (e.g. "/var/lib/brain/state.json").
// If the directory does not exist, it is created automatically.
type fileDriver struct{}

func (fileDriver) Open(dsn string) (*Stores, error) {
	if dsn == "" {
		return nil, fmt.Errorf("file driver: DSN (file path) is required")
	}

	fs, err := OpenFileStore(dsn)
	if err != nil {
		return nil, fmt.Errorf("file driver: %w", err)
	}

	// Build a filesystem-backed ArtifactStore alongside the FileStore.
	// Artifacts are stored in a sibling "artifacts" directory next to the
	// JSON file.
	artifactDir := filepath.Join(filepath.Dir(dsn), "artifacts")
	if mkErr := os.MkdirAll(artifactDir, 0700); mkErr != nil {
		return nil, fmt.Errorf("file driver: mkdir artifacts: %w", mkErr)
	}
	metaStore := fs.MetaStore()
	artifactStore := NewFSArtifactStore(artifactDir, metaStore, nil)

	checkpoint := fs.CheckpointStore()
	resume := NewMemResumeCoordinator(checkpoint)

	return &Stores{
		PlanStore:          fs.PlanStore(),
		ArtifactStore:      artifactStore,
		ArtifactMeta:       metaStore,
		RunCheckpointStore: checkpoint,
		UsageLedger:        fs.Ledger(),
		ResumeCoordinator:  resume,
		RunStore:           NewMemRunStore(),
		AuditLogger:        NewMemAuditLogger(),
		LearningStore:      NewMemLearningStore(),
		SharedMessageStore: NewMemSharedMessageStore(),
	}, nil
}

func init() {
	Register("file", fileDriver{})
}
