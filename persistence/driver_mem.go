package persistence

import "time"

// memDriver implements Driver for the in-memory backend.
// DSN is ignored; every Open call returns a fresh, independent set of stores.
type memDriver struct{}

func (memDriver) Open(dsn string) (*Stores, error) {
	now := func() time.Time { return time.Now().UTC() }

	meta := NewMemArtifactMetaStore(now)
	artifact := NewMemArtifactStore(meta, now)
	plan := NewMemPlanStore(now)
	checkpoint := NewMemRunCheckpointStore(now)
	usage := NewMemUsageLedger(now)
	resume := NewMemResumeCoordinator(checkpoint)

	return &Stores{
		PlanStore:          plan,
		ArtifactStore:      artifact,
		ArtifactMeta:       meta,
		RunCheckpointStore: checkpoint,
		UsageLedger:        usage,
		ResumeCoordinator:  resume,
	}, nil
}

func init() {
	Register("mem", memDriver{})
}
