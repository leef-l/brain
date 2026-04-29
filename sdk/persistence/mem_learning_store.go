// Package persistence provides in-process storage implementations.
//
// memLearningStore is the in-memory LearningStore implementation. It holds
// L1-L3 learning data (profiles, sequences, preferences, daily summaries,
// anomaly templates, etc.) under a single RWMutex. Production deployments
// should replace it with a persistent backend.
package persistence

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// memLearningStore implements LearningStore in memory.
type memLearningStore struct {
	mu               sync.RWMutex
	profiles         map[string]*LearningProfile
	taskScores       map[string][]*LearningTaskScore
	sequences        []*LearningSequence
	interactionSeqs  map[string][]*InteractionSequence
	preferences      map[string]*LearningPreference
	dailySummaries   map[string]*DailySummary
	anomalyTemplates map[int64]*AnomalyTemplate
	siteProfiles     map[string][]*SiteAnomalyProfile
	failureSamples   map[string][]*PatternFailureSample
	demoSequences    map[int64]*HumanDemoSequence
	sitemapSnaps     map[string]*SitemapSnapshot
	nextSeqID        int64
	nextInteractionID int64
	nextAnomalyTplID int64
	nextDemoSeqID    int64
	nextSitemapSnapID int64
}

// ---------- L1 ----------

func (s *memLearningStore) SaveProfile(_ context.Context, profile *LearningProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := *profile
	stored.UpdatedAt = time.Now()
	s.profiles[profile.BrainKind] = &stored
	return nil
}

func (s *memLearningStore) SaveTaskScore(_ context.Context, score *LearningTaskScore) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := *score
	s.taskScores[score.BrainKind] = append(s.taskScores[score.BrainKind], &stored)
	return nil
}

func (s *memLearningStore) ListProfiles(_ context.Context) ([]*LearningProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*LearningProfile, 0, len(s.profiles))
	for _, p := range s.profiles {
		copied := *p
		out = append(out, &copied)
	}
	return out, nil
}

func (s *memLearningStore) ListTaskScores(_ context.Context, brainKind string) ([]*LearningTaskScore, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	scores, ok := s.taskScores[brainKind]
	if !ok {
		return nil, nil
	}
	out := make([]*LearningTaskScore, len(scores))
	for i, sc := range scores {
		copied := *sc
		out[i] = &copied
	}
	return out, nil
}

// ---------- L2 Sequences ----------

func (s *memLearningStore) SaveSequence(_ context.Context, seq *LearningSequence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq.ID == 0 {
		s.nextSeqID++
		seq.ID = s.nextSeqID
	}
	stored := *seq
	s.sequences = append(s.sequences, &stored)
	return nil
}

func (s *memLearningStore) ListSequences(_ context.Context, limit int) ([]*LearningSequence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*LearningSequence, len(s.sequences))
	for i, seq := range s.sequences {
		copied := *seq
		out[i] = &copied
	}
	// Sort by ID descending (newest first)
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- L2 Interaction Sequences ----------

func (s *memLearningStore) SaveInteractionSequence(_ context.Context, seq *InteractionSequence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq.ID == 0 {
		s.nextInteractionID++
		seq.ID = s.nextInteractionID
	}
	stored := *seq
	s.interactionSeqs[seq.BrainKind] = append(s.interactionSeqs[seq.BrainKind], &stored)
	return nil
}

func (s *memLearningStore) ListInteractionSequences(_ context.Context, brainKind string, limit int) ([]*InteractionSequence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seqs, ok := s.interactionSeqs[brainKind]
	if !ok {
		return nil, nil
	}
	out := make([]*InteractionSequence, len(seqs))
	for i, seq := range seqs {
		copied := *seq
		out[i] = &copied
	}
	// Sort by ID descending
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- L3 Preferences ----------

func (s *memLearningStore) SavePreference(_ context.Context, pref *LearningPreference) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := *pref
	stored.UpdatedAt = time.Now()
	s.preferences[pref.Category] = &stored
	return nil
}

func (s *memLearningStore) GetPreference(_ context.Context, category string) (*LearningPreference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	pref, ok := s.preferences[category]
	if !ok {
		return nil, nil
	}
	copied := *pref
	return &copied, nil
}

func (s *memLearningStore) ListPreferences(_ context.Context) ([]*LearningPreference, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*LearningPreference, 0, len(s.preferences))
	for _, p := range s.preferences {
		copied := *p
		out = append(out, &copied)
	}
	return out, nil
}

// ---------- Daily Summaries ----------

func (s *memLearningStore) SaveDailySummary(_ context.Context, ds *DailySummary) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := *ds
	stored.UpdatedAt = time.Now()
	s.dailySummaries[ds.Date] = &stored
	return nil
}

func (s *memLearningStore) GetDailySummary(_ context.Context, date string) (*DailySummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	ds, ok := s.dailySummaries[date]
	if !ok {
		return nil, nil
	}
	copied := *ds
	return &copied, nil
}

func (s *memLearningStore) ListDailySummaries(_ context.Context, limit int) ([]*DailySummary, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*DailySummary, 0, len(s.dailySummaries))
	for _, ds := range s.dailySummaries {
		copied := *ds
		out = append(out, &copied)
	}
	// Sort by date descending
	sort.Slice(out, func(i, j int) bool {
		return out[i].Date > out[j].Date
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// ---------- Anomaly Templates ----------

func (s *memLearningStore) SaveAnomalyTemplate(_ context.Context, tpl *AnomalyTemplate) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if tpl.ID == 0 {
		s.nextAnomalyTplID++
		tpl.ID = s.nextAnomalyTplID
		tpl.CreatedAt = time.Now()
	}
	tpl.UpdatedAt = time.Now()
	stored := *tpl
	s.anomalyTemplates[tpl.ID] = &stored
	return nil
}

func (s *memLearningStore) GetAnomalyTemplate(_ context.Context, id int64) (*AnomalyTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	tpl, ok := s.anomalyTemplates[id]
	if !ok {
		return nil, nil
	}
	copied := *tpl
	return &copied, nil
}

func (s *memLearningStore) ListAnomalyTemplates(_ context.Context) ([]*AnomalyTemplate, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*AnomalyTemplate, 0, len(s.anomalyTemplates))
	for _, tpl := range s.anomalyTemplates {
		copied := *tpl
		out = append(out, &copied)
	}
	return out, nil
}

func (s *memLearningStore) DeleteAnomalyTemplate(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.anomalyTemplates, id)
	return nil
}

// ---------- Site Anomaly Profiles ----------

func (s *memLearningStore) UpsertSiteAnomalyProfile(_ context.Context, p *SiteAnomalyProfile) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	list := s.siteProfiles[p.SiteOrigin]
	for i, existing := range list {
		if existing.AnomalyType == p.AnomalyType && existing.AnomalySubtype == p.AnomalySubtype {
			stored := *p
			list[i] = &stored
			s.siteProfiles[p.SiteOrigin] = list
			return nil
		}
	}
	stored := *p
	s.siteProfiles[p.SiteOrigin] = append(list, &stored)
	return nil
}

func (s *memLearningStore) ListSiteAnomalyProfiles(_ context.Context, site string) ([]*SiteAnomalyProfile, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list, ok := s.siteProfiles[site]
	if !ok {
		return nil, nil
	}
	out := make([]*SiteAnomalyProfile, len(list))
	for i, p := range list {
		copied := *p
		out[i] = &copied
	}
	return out, nil
}

// ---------- Pattern Failure Samples ----------

func (s *memLearningStore) SavePatternFailureSample(_ context.Context, sample *PatternFailureSample) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	stored := *sample
	s.failureSamples[sample.PatternID] = append(s.failureSamples[sample.PatternID], &stored)
	return nil
}

func (s *memLearningStore) ListPatternFailureSamples(_ context.Context, patternID string) ([]*PatternFailureSample, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	list, ok := s.failureSamples[patternID]
	if !ok {
		return nil, nil
	}
	out := make([]*PatternFailureSample, len(list))
	for i, s := range list {
		copied := *s
		out[i] = &copied
	}
	return out, nil
}

// ---------- Human Demo Sequences ----------

func (s *memLearningStore) SaveHumanDemoSequence(_ context.Context, seq *HumanDemoSequence) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if seq.ID == 0 {
		s.nextDemoSeqID++
		seq.ID = s.nextDemoSeqID
	}
	stored := *seq
	s.demoSequences[seq.ID] = &stored
	return nil
}

func (s *memLearningStore) ListHumanDemoSequences(_ context.Context, approvedOnly bool) ([]*HumanDemoSequence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	out := make([]*HumanDemoSequence, 0, len(s.demoSequences))
	for _, seq := range s.demoSequences {
		if approvedOnly && !seq.Approved {
			continue
		}
		copied := *seq
		out = append(out, &copied)
	}
	// Sort by ID descending
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID > out[j].ID
	})
	return out, nil
}

func (s *memLearningStore) GetHumanDemoSequence(_ context.Context, id int64) (*HumanDemoSequence, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	seq, ok := s.demoSequences[id]
	if !ok {
		return nil, nil
	}
	copied := *seq
	return &copied, nil
}

func (s *memLearningStore) ApproveHumanDemoSequence(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	seq, ok := s.demoSequences[id]
	if ok {
		seq.Approved = true
	}
	return nil
}

func (s *memLearningStore) DeleteHumanDemoSequence(_ context.Context, id int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	delete(s.demoSequences, id)
	return nil
}

func (s *memLearningStore) PurgeHumanDemoSequences(_ context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int64
	for id, seq := range s.demoSequences {
		if seq.RecordedAt.Before(olderThan) {
			delete(s.demoSequences, id)
			deleted++
		}
	}
	return deleted, nil
}

// ---------- Sitemap Snapshots ----------

func snapKey(siteOrigin string, depth int) string {
	return fmt.Sprintf("%s:%d", siteOrigin, depth)
}

func (s *memLearningStore) SaveSitemapSnapshot(_ context.Context, snap *SitemapSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if snap.ID == 0 {
		s.nextSitemapSnapID++
		snap.ID = s.nextSitemapSnapID
	}
	stored := *snap
	s.sitemapSnaps[snapKey(snap.SiteOrigin, snap.Depth)] = &stored
	return nil
}

func (s *memLearningStore) GetSitemapSnapshot(_ context.Context, siteOrigin string, depth int) (*SitemapSnapshot, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	snap, ok := s.sitemapSnaps[snapKey(siteOrigin, depth)]
	if !ok {
		return nil, nil
	}
	copied := *snap
	return &copied, nil
}

func (s *memLearningStore) PurgeSitemapSnapshots(_ context.Context, olderThan time.Time) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var deleted int64
	for k, snap := range s.sitemapSnaps {
		if snap.CollectedAt.Before(olderThan) {
			delete(s.sitemapSnaps, k)
			deleted++
		}
	}
	return deleted, nil
}

// NewMemLearningStore returns an in-memory LearningStore.
func NewMemLearningStore() LearningStore {
	return &memLearningStore{
		profiles:          make(map[string]*LearningProfile),
		taskScores:        make(map[string][]*LearningTaskScore),
		sequences:         make([]*LearningSequence, 0),
		interactionSeqs:   make(map[string][]*InteractionSequence),
		preferences:       make(map[string]*LearningPreference),
		dailySummaries:    make(map[string]*DailySummary),
		anomalyTemplates:  make(map[int64]*AnomalyTemplate),
		siteProfiles:      make(map[string][]*SiteAnomalyProfile),
		failureSamples:    make(map[string][]*PatternFailureSample),
		demoSequences:     make(map[int64]*HumanDemoSequence),
		sitemapSnaps:      make(map[string]*SitemapSnapshot),
	}
}
