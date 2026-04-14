package reviewrun

import (
	"context"
	"time"

	"github.com/leef-l/brain/internal/central/control"
	"github.com/leef-l/brain/internal/central/review"
)

type Request struct {
	Trade review.Request `json:"trade"`
}

type Trace struct {
	StartedAtMS  int64  `json:"started_at_ms"`
	FinishedAtMS int64  `json:"finished_at_ms"`
	Outcome      string `json:"outcome"`
}

type Result struct {
	Trade    review.Request  `json:"trade"`
	Decision review.Decision `json:"decision"`
	Trace    Trace           `json:"trace"`
	Control  control.Result  `json:"control"`
}

type Runner struct {
	reviewer   Reviewer
	controller Controller
	now        func() time.Time
}

type Reviewer interface {
	Evaluate(ctx context.Context, req review.Request) (review.Decision, error)
}

type Controller interface {
	RecordReviewOutcome(approved bool, reason string, sizeFactor float64) control.Result
}

func New(reviewer Reviewer, controller Controller) *Runner {
	return &Runner{
		reviewer:   reviewer,
		controller: controller,
		now:        time.Now,
	}
}

func (r *Runner) Run(ctx context.Context, req Request) (Result, error) {
	started := r.now().UTC().UnixMilli()
	decision, err := r.reviewer.Evaluate(ctx, req.Trade)
	if err != nil {
		return Result{}, err
	}
	controlResult := r.controller.RecordReviewOutcome(decision.Approved, decision.Reason, decision.SizeFactor)
	outcome := "rejected"
	if decision.Approved {
		outcome = "approved"
	}
	return Result{
		Trade:    req.Trade,
		Decision: decision,
		Trace: Trace{
			StartedAtMS:  started,
			FinishedAtMS: r.now().UTC().UnixMilli(),
			Outcome:      outcome,
		},
		Control: controlResult,
	}, nil
}
