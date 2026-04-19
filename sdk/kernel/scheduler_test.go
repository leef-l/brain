package kernel

import (
	"context"
	"testing"

	"github.com/leef-l/brain/sdk/agent"
)

func TestTaskSchedulerLinearDeps(t *testing.T) {
	s := NewDefaultTaskScheduler(nil, nil)
	tasks := []SchedulableTask{
		{ID: "A", TaskType: "plan"},
		{ID: "B", TaskType: "code", DependsOn: []string{"A"}},
		{ID: "C", TaskType: "verify", DependsOn: []string{"B"}},
	}
	plan, err := s.Plan(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Batches) != 3 {
		t.Fatalf("batches = %d, want 3", len(plan.Batches))
	}
	if plan.Batches[0].Tasks[0].ID != "A" {
		t.Errorf("batch 0 = %s, want A", plan.Batches[0].Tasks[0].ID)
	}
	if plan.Batches[1].Tasks[0].ID != "B" {
		t.Errorf("batch 1 = %s, want B", plan.Batches[1].Tasks[0].ID)
	}
	if plan.Batches[2].Tasks[0].ID != "C" {
		t.Errorf("batch 2 = %s, want C", plan.Batches[2].Tasks[0].ID)
	}
}

func TestTaskSchedulerParallel(t *testing.T) {
	s := NewDefaultTaskScheduler(nil, nil)
	tasks := []SchedulableTask{
		{ID: "A", TaskType: "plan"},
		{ID: "B", TaskType: "code"},
		{ID: "C", TaskType: "data"},
		{ID: "D", TaskType: "verify", DependsOn: []string{"A", "B", "C"}},
	}
	plan, err := s.Plan(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Batches) != 2 {
		t.Fatalf("batches = %d, want 2", len(plan.Batches))
	}
	if len(plan.Batches[0].Tasks) != 3 {
		t.Errorf("batch 0 tasks = %d, want 3", len(plan.Batches[0].Tasks))
	}
	if plan.Batches[1].Tasks[0].ID != "D" {
		t.Errorf("batch 1 = %s, want D", plan.Batches[1].Tasks[0].ID)
	}
}

func TestTaskSchedulerCycleDetection(t *testing.T) {
	s := NewDefaultTaskScheduler(nil, nil)
	tasks := []SchedulableTask{
		{ID: "A", DependsOn: []string{"B"}},
		{ID: "B", DependsOn: []string{"A"}},
	}
	_, err := s.Plan(context.Background(), tasks)
	if err == nil {
		t.Fatal("expected cycle error")
	}
}

func TestTaskSchedulerSelectBrainWithLearner(t *testing.T) {
	le := NewLearningEngine()
	le.RecordDelegateResult(agent.KindCode, "code.edit", 0.9, 0.8, 0.7, 0.95)
	le.RecordDelegateResult(agent.KindCode, "code.edit", 0.85, 0.75, 0.7, 0.9)
	le.RecordDelegateResult(agent.Kind("data"), "code.edit", 0.5, 0.4, 0.3, 0.6)

	s := NewDefaultTaskScheduler(le, nil)
	brain, err := s.SelectBrain(context.Background(), "code.edit", []agent.Kind{agent.KindCode, "data"})
	if err != nil {
		t.Fatalf("SelectBrain: %v", err)
	}
	if brain != agent.KindCode {
		t.Errorf("selected %s, want code", brain)
	}
}

func TestTaskSchedulerBrainAssignment(t *testing.T) {
	available := func() []agent.Kind {
		return []agent.Kind{agent.KindCode, agent.KindData}
	}
	s := NewDefaultTaskScheduler(nil, available)
	tasks := []SchedulableTask{
		{ID: "A", TaskType: "code.edit"},
		{ID: "B", TaskType: "data.fetch", BrainKind: agent.KindData},
	}
	plan, err := s.Plan(context.Background(), tasks)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Batches) != 1 {
		t.Fatalf("batches = %d, want 1", len(plan.Batches))
	}
	if plan.Batches[0].BrainAssignment["B"] != agent.KindData {
		t.Errorf("B assignment = %s, want data", plan.Batches[0].BrainAssignment["B"])
	}
}

func TestTaskSchedulerEmpty(t *testing.T) {
	s := NewDefaultTaskScheduler(nil, nil)
	plan, err := s.Plan(context.Background(), nil)
	if err != nil {
		t.Fatalf("Plan: %v", err)
	}
	if len(plan.Batches) != 0 {
		t.Errorf("batches = %d, want 0", len(plan.Batches))
	}
}
