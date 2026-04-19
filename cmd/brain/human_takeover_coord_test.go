package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/leef-l/brain/sdk/events"
	"github.com/leef-l/brain/sdk/tool"
)

type capturePublisher struct {
	ch chan events.Event
}

func newCapturePublisher(size int) *capturePublisher {
	return &capturePublisher{ch: make(chan events.Event, size)}
}

func (p *capturePublisher) Publish(_ context.Context, ev events.Event) {
	p.ch <- ev
}

func TestHumanTakeoverRequestedEnvelopePreservesLegacyFields(t *testing.T) {
	pub := newCapturePublisher(1)
	coord := &hostHumanTakeoverCoordinator{bus: pub}
	req := tool.HumanTakeoverRequest{
		RunID:      "run-123",
		BrainKind:  "browser",
		Reason:     "captcha_detected",
		Guidance:   "solve it",
		URL:        "https://example.com/login",
		TimeoutSec: 90,
	}

	coord.publishHumanEvent("task.human.requested", req, "")

	select {
	case ev := <-pub.ch:
		if ev.Type != "task.human.requested" {
			t.Fatalf("event type=%q, want task.human.requested", ev.Type)
		}
		var payload humanTakeoverEventEnvelope
		if err := json.Unmarshal(ev.Data, &payload); err != nil {
			t.Fatalf("unmarshal payload: %v", err)
		}
		if payload.Schema != humanTakeoverEnvelopeSchema {
			t.Fatalf("schema=%q, want %q", payload.Schema, humanTakeoverEnvelopeSchema)
		}
		if payload.SchemaVersion != "v1" {
			t.Fatalf("schema_version=%q, want v1", payload.SchemaVersion)
		}
		if payload.EventName != "task.human.requested" {
			t.Fatalf("event_name=%q, want task.human.requested", payload.EventName)
		}
		if payload.EscalationType != "manual_review_required" {
			t.Fatalf("escalation_type=%q, want manual_review_required", payload.EscalationType)
		}
		if payload.ExecutionID != req.RunID {
			t.Fatalf("execution_id=%q, want %q", payload.ExecutionID, req.RunID)
		}
		if payload.Kind != "human_takeover" {
			t.Fatalf("kind=%q, want human_takeover", payload.Kind)
		}
		if payload.State != "requested" {
			t.Fatalf("state=%q, want requested", payload.State)
		}
		if payload.RunID != req.RunID || payload.BrainID != req.BrainKind {
			t.Fatalf("legacy ids mismatch: %+v", payload)
		}
		if payload.BrainKind != req.BrainKind || payload.SourceBrain != req.BrainKind {
			t.Fatalf("compat brain aliases mismatch: %+v", payload)
		}
		if payload.Reason != req.Reason || payload.Guidance != req.Guidance || payload.URL != req.URL {
			t.Fatalf("legacy fields mismatch: %+v", payload)
		}
		if payload.ReasonCode != req.Reason {
			t.Fatalf("reason_code=%q, want %q", payload.ReasonCode, req.Reason)
		}
		if payload.ReasonSummary != req.Guidance {
			t.Fatalf("reason_summary=%q, want %q", payload.ReasonSummary, req.Guidance)
		}
		if payload.RecommendedAction != req.Guidance {
			t.Fatalf("recommended_action=%q, want %q", payload.RecommendedAction, req.Guidance)
		}
		if payload.TimeoutSec != req.TimeoutSec {
			t.Fatalf("timeout_sec=%d, want %d", payload.TimeoutSec, req.TimeoutSec)
		}
		if payload.Escalation.Kind != "human_takeover" {
			t.Fatalf("escalation.kind=%q, want human_takeover", payload.Escalation.Kind)
		}
		if payload.Escalation.ExecutionID != req.RunID {
			t.Fatalf("escalation.execution_id=%q, want %q", payload.Escalation.ExecutionID, req.RunID)
		}
		if payload.Escalation.State != "requested" {
			t.Fatalf("escalation.state=%q, want requested", payload.Escalation.State)
		}
		if payload.Escalation.RunID != req.RunID || payload.Escalation.BrainID != req.BrainKind {
			t.Fatalf("escalation ids mismatch: %+v", payload.Escalation)
		}
		if payload.Escalation.BrainKind != req.BrainKind || payload.Escalation.SourceBrain != req.BrainKind {
			t.Fatalf("escalation brain aliases mismatch: %+v", payload.Escalation)
		}
		if payload.Escalation.EscalationType != "manual_review_required" {
			t.Fatalf("escalation.escalation_type=%q, want manual_review_required", payload.Escalation.EscalationType)
		}
		if payload.Escalation.ReasonCode != req.Reason {
			t.Fatalf("escalation.reason_code=%q, want %q", payload.Escalation.ReasonCode, req.Reason)
		}
		if payload.Escalation.ReasonSummary != req.Guidance {
			t.Fatalf("escalation.reason_summary=%q, want %q", payload.Escalation.ReasonSummary, req.Guidance)
		}
		if payload.Escalation.RecommendedAction != req.Guidance {
			t.Fatalf("escalation.recommended_action=%q, want %q", payload.Escalation.RecommendedAction, req.Guidance)
		}
	default:
		t.Fatal("expected published event")
	}
}

func TestHumanTakeoverResumePublishesStableEnvelope(t *testing.T) {
	pub := newCapturePublisher(2)
	runID := "run-resume"
	mgr := &runManager{}
	mgr.runs.Store(runID, &runEntry{ID: runID, Status: "running"})
	coord := newHostHumanTakeoverCoordinator(mgr, pub)

	req := tool.HumanTakeoverRequest{
		RunID:      runID,
		BrainKind:  "browser",
		Reason:     "session_expired",
		Guidance:   "login again",
		URL:        "https://example.com/profile",
		TimeoutSec: 30,
	}

	done := make(chan tool.HumanTakeoverResponse, 1)
	go func() {
		done <- coord.RequestTakeover(context.Background(), req)
	}()

	requested := waitEvent(t, pub.ch)
	if requested.Type != "task.human.requested" {
		t.Fatalf("requested type=%q, want task.human.requested", requested.Type)
	}

	if ok := coord.Resume(runID, "human completed"); !ok {
		t.Fatal("resume returned false, want true")
	}

	resp := waitResponse(t, done)
	if resp.Outcome != tool.HumanOutcomeResumed {
		t.Fatalf("outcome=%q, want resumed", resp.Outcome)
	}

	resumed := waitEvent(t, pub.ch)
	if resumed.Type != "task.human.resumed" {
		t.Fatalf("resumed type=%q, want task.human.resumed", resumed.Type)
	}

	var payload humanTakeoverEventEnvelope
	if err := json.Unmarshal(resumed.Data, &payload); err != nil {
		t.Fatalf("unmarshal resumed payload: %v", err)
	}
	if payload.EventName != "task.human.resumed" {
		t.Fatalf("event_name=%q, want task.human.resumed", payload.EventName)
	}
	if payload.EscalationType != "manual_review_required" {
		t.Fatalf("escalation_type=%q, want manual_review_required", payload.EscalationType)
	}
	if payload.ExecutionID != runID {
		t.Fatalf("execution_id=%q, want %q", payload.ExecutionID, runID)
	}
	if payload.State != string(tool.HumanOutcomeResumed) {
		t.Fatalf("state=%q, want resumed", payload.State)
	}
	if payload.BrainKind != req.BrainKind || payload.SourceBrain != req.BrainKind {
		t.Fatalf("compat brain aliases mismatch: %+v", payload)
	}
	if payload.ReasonCode != req.Reason {
		t.Fatalf("reason_code=%q, want %q", payload.ReasonCode, req.Reason)
	}
	if payload.ReasonSummary != req.Guidance {
		t.Fatalf("reason_summary=%q, want %q", payload.ReasonSummary, req.Guidance)
	}
	if payload.RecommendedAction != req.Guidance {
		t.Fatalf("recommended_action=%q, want %q", payload.RecommendedAction, req.Guidance)
	}
	if payload.Note != "human completed" {
		t.Fatalf("legacy note=%q, want human completed", payload.Note)
	}
	if payload.Escalation.ExecutionID != runID {
		t.Fatalf("escalation.execution_id=%q, want %q", payload.Escalation.ExecutionID, runID)
	}
	if payload.Escalation.State != string(tool.HumanOutcomeResumed) {
		t.Fatalf("escalation.state=%q, want resumed", payload.Escalation.State)
	}
	if payload.Escalation.BrainKind != req.BrainKind || payload.Escalation.SourceBrain != req.BrainKind {
		t.Fatalf("escalation brain aliases mismatch: %+v", payload.Escalation)
	}
	if payload.Escalation.EscalationType != "manual_review_required" {
		t.Fatalf("escalation.escalation_type=%q, want manual_review_required", payload.Escalation.EscalationType)
	}
	if payload.Escalation.ReasonCode != req.Reason {
		t.Fatalf("escalation.reason_code=%q, want %q", payload.Escalation.ReasonCode, req.Reason)
	}
	if payload.Escalation.ReasonSummary != req.Guidance {
		t.Fatalf("escalation.reason_summary=%q, want %q", payload.Escalation.ReasonSummary, req.Guidance)
	}
	if payload.Escalation.RecommendedAction != req.Guidance {
		t.Fatalf("escalation.recommended_action=%q, want %q", payload.Escalation.RecommendedAction, req.Guidance)
	}
	if payload.Escalation.Note != "human completed" {
		t.Fatalf("escalation.note=%q, want human completed", payload.Escalation.Note)
	}
}

func waitEvent(t *testing.T, ch <-chan events.Event) events.Event {
	t.Helper()
	select {
	case ev := <-ch:
		return ev
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for event")
		return events.Event{}
	}
}

func waitResponse(t *testing.T, ch <-chan tool.HumanTakeoverResponse) tool.HumanTakeoverResponse {
	t.Helper()
	select {
	case resp := <-ch:
		return resp
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for response")
		return tool.HumanTakeoverResponse{}
	}
}
