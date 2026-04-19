package main

import (
	"strings"

	"github.com/leef-l/brain/sdk/tool"
)

const humanTakeoverEnvelopeSchema = "brain.human_takeover.escalation"

type humanTakeoverEventEnvelope struct {
	Schema            string                  `json:"schema"`
	SchemaVersion     string                  `json:"schema_version"`
	EventName         string                  `json:"event_name"`
	Escalation        humanTakeoverEscalation `json:"escalation"`
	EscalationType    string                  `json:"escalation_type,omitempty"`
	ExecutionID       string                  `json:"execution_id"`
	Kind              string                  `json:"kind"`
	State             string                  `json:"state"`
	RunID             string                  `json:"run_id"`
	BrainID           string                  `json:"brain_id,omitempty"`
	BrainKind         string                  `json:"brain_kind,omitempty"`
	SourceBrain       string                  `json:"source_brain,omitempty"`
	Reason            string                  `json:"reason"`
	ReasonCode        string                  `json:"reason_code,omitempty"`
	ReasonSummary     string                  `json:"reason_summary,omitempty"`
	Guidance          string                  `json:"guidance,omitempty"`
	RecommendedAction string                  `json:"recommended_action,omitempty"`
	URL               string                  `json:"url,omitempty"`
	TimeoutSec        int                     `json:"timeout_sec,omitempty"`
	Note              string                  `json:"note,omitempty"`
}

type humanTakeoverEscalation struct {
	ExecutionID       string `json:"execution_id"`
	Kind              string `json:"kind"`
	State             string `json:"state"`
	RunID             string `json:"run_id"`
	BrainID           string `json:"brain_id,omitempty"`
	BrainKind         string `json:"brain_kind,omitempty"`
	SourceBrain       string `json:"source_brain,omitempty"`
	EscalationType    string `json:"escalation_type,omitempty"`
	Reason            string `json:"reason"`
	ReasonCode        string `json:"reason_code,omitempty"`
	ReasonSummary     string `json:"reason_summary,omitempty"`
	Guidance          string `json:"guidance,omitempty"`
	RecommendedAction string `json:"recommended_action,omitempty"`
	URL               string `json:"url,omitempty"`
	TimeoutSec        int    `json:"timeout_sec,omitempty"`
	Note              string `json:"note,omitempty"`
}

func newHumanTakeoverEventEnvelope(eventType string, req tool.HumanTakeoverRequest, note string) humanTakeoverEventEnvelope {
	var (
		state             = humanTakeoverStateFromEventType(eventType)
		escalationType    = "manual_review_required"
		reasonSummary     = humanTakeoverReasonSummary(req)
		recommendedAction = strings.TrimSpace(req.Guidance)
	)

	return humanTakeoverEventEnvelope{
		Schema:         humanTakeoverEnvelopeSchema,
		SchemaVersion:  "v1",
		EventName:      eventType,
		EscalationType: escalationType,
		ExecutionID:    req.RunID,
		Kind:           "human_takeover",
		State:          state,
		Escalation: humanTakeoverEscalation{
			ExecutionID:       req.RunID,
			Kind:              "human_takeover",
			State:             state,
			RunID:             req.RunID,
			BrainID:           req.BrainKind,
			BrainKind:         req.BrainKind,
			SourceBrain:       req.BrainKind,
			EscalationType:    escalationType,
			Reason:            req.Reason,
			ReasonCode:        req.Reason,
			ReasonSummary:     reasonSummary,
			Guidance:          req.Guidance,
			RecommendedAction: recommendedAction,
			URL:               req.URL,
			TimeoutSec:        req.TimeoutSec,
			Note:              note,
		},
		RunID:             req.RunID,
		BrainID:           req.BrainKind,
		BrainKind:         req.BrainKind,
		SourceBrain:       req.BrainKind,
		Reason:            req.Reason,
		ReasonCode:        req.Reason,
		ReasonSummary:     reasonSummary,
		Guidance:          req.Guidance,
		RecommendedAction: recommendedAction,
		URL:               req.URL,
		TimeoutSec:        req.TimeoutSec,
		Note:              note,
	}
}

func humanTakeoverReasonSummary(req tool.HumanTakeoverRequest) string {
	if guidance := strings.TrimSpace(req.Guidance); guidance != "" {
		return guidance
	}
	return strings.TrimSpace(req.Reason)
}

func humanTakeoverStateFromEventType(eventType string) string {
	switch eventType {
	case "task.human.resumed":
		return string(tool.HumanOutcomeResumed)
	case "task.human.aborted":
		return string(tool.HumanOutcomeAborted)
	default:
		return "requested"
	}
}
