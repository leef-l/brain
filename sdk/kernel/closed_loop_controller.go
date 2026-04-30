package kernel

import (
	"context"
	"fmt"
	"time"
)

// MACCS Wave 3 Batch 3 — 闭环控制器
// 七阶段闭环工作流核心串联器：需求→设计→审核→执行→验收→交付→复盘
// 每个阶段通过 runPhase 统一包装，提供超时、重试、取消和回调能力。
// 交付和复盘通过 slim 接口解耦，避免依赖尚未创建的类型。

// ClosedLoopDeliverer 交付生成的 slim 接口，解耦 delivery_generator。
type ClosedLoopDeliverer interface {
	GenerateDelivery(ctx context.Context, session *ProjectSession) (interface{}, error)
}

// ClosedLoopRetrospector 复盘的 slim 接口，解耦 retrospective。
type ClosedLoopRetrospector interface {
	RunRetrospective(ctx context.Context, session *ProjectSession) (interface{}, error)
}

// ClosedLoopConfig 闭环控制器配置。
type ClosedLoopConfig struct {
	MaxRetries       int           `json:"max_retries"`       // 阶段最大重试次数，默认 2
	PhaseTimeout     time.Duration `json:"phase_timeout"`     // 单阶段超时，默认 15min
	EnableReview     bool          `json:"enable_review"`     // 是否启用审核阶段
	EnableAcceptance bool          `json:"enable_acceptance"` // 是否启用验收阶段
	EnableRetrospec  bool          `json:"enable_retrospec"`  // 是否启用复盘阶段
	OnPhaseChange    func(phase string, event string) `json:"-"` // 阶段变更回调
}

// NewDefaultClosedLoopConfig 返回默认闭环控制器配置。
func NewDefaultClosedLoopConfig() ClosedLoopConfig {
	return ClosedLoopConfig{
		MaxRetries: 2, PhaseTimeout: 15 * time.Minute,
		EnableReview: true, EnableAcceptance: true, EnableRetrospec: true,
	}
}

// ClosedLoopDeps 闭环控制器的依赖注入容器。
type ClosedLoopDeps struct {
	Parser       RequirementParser
	Designer     DesignGenerator
	Reviewer     *DesignReviewLoop      // 可选，nil 则跳过审核
	Scheduler    *ExecutionScheduler
	Tester       AcceptanceTester       // 可选
	Deliverer    ClosedLoopDeliverer    // slim 接口
	Retrospect   ClosedLoopRetrospector // slim 接口
	SessionStore ProjectSessionStore
}

// NewClosedLoopDepsMinimal 创建最小依赖集（只用默认实现）。
func NewClosedLoopDepsMinimal() ClosedLoopDeps {
	return ClosedLoopDeps{
		Parser: NewDefaultRequirementParser(), Designer: NewDefaultDesignGenerator(),
		Scheduler: NewExecutionScheduler(ExecutionSchedulerConfig{}),
		Tester: NewDefaultAcceptanceTester(), SessionStore: NewMemProjectSessionStore(),
	}
}

// ClosedLoopResult 闭环控制器的执行结果。
type ClosedLoopResult struct {
	SessionID     string                 `json:"session_id"`
	ProjectID     string                 `json:"project_id"`
	Success       bool                   `json:"success"`
	FinalPhase    string                 `json:"final_phase"`
	PhaseResults  map[string]interface{} `json:"phase_results"`
	TotalDuration time.Duration          `json:"total_duration"`
	Error         string                 `json:"error,omitempty"`
}

// ClosedLoopController 七阶段闭环工作流的核心串联器。
type ClosedLoopController struct {
	config ClosedLoopConfig
	deps   ClosedLoopDeps
}

// NewClosedLoopController 创建闭环控制器。
func NewClosedLoopController(config ClosedLoopConfig, deps ClosedLoopDeps) *ClosedLoopController {
	if config.MaxRetries <= 0 {
		config.MaxRetries = 2
	}
	if config.PhaseTimeout <= 0 {
		config.PhaseTimeout = 15 * time.Minute
	}
	return &ClosedLoopController{config: config, deps: deps}
}

// Execute 驱动完整的七阶段闭环工作流，返回执行结果。
func (c *ClosedLoopController) Execute(ctx context.Context, projectName, goal string) (*ClosedLoopResult, error) {
	start := time.Now()
	projectID := fmt.Sprintf("proj-%d", start.UnixNano())
	session := NewProjectSession(projectID, projectName, goal)
	if err := c.deps.SessionStore.SaveSession(ctx, session); err != nil {
		return nil, fmt.Errorf("closed_loop: 保存 session 失败: %w", err)
	}
	sm := NewProjectStateMachineWithConfig(session.SessionID, c.config.MaxRetries, ProjectSMHooks{})
	result := &ClosedLoopResult{
		SessionID: session.SessionID, ProjectID: projectID,
		PhaseResults: make(map[string]interface{}),
	}
	var spec *RequirementSpec
	var proposal *DesignProposal

	// Phase 1: Requirement
	err := c.runPhase(ctx, SMPhaseRequirement, func(pctx context.Context) error {
		if err := session.StartPhase(PhaseRequirement); err != nil {
			return err
		}
		c.notify(SMPhaseRequirement, "started")
		parsed, parseErr := c.deps.Parser.Parse(pctx, goal)
		if parseErr != nil {
			_ = session.FailPhase(PhaseRequirement, parseErr.Error())
			return parseErr
		}
		spec = parsed
		session.SetContext("requirement_spec", spec)
		result.PhaseResults[SMPhaseRequirement] = spec
		if err := session.CompletePhase(PhaseRequirement, []string{spec.SpecID}); err != nil {
			return err
		}
		return c.advanceSM(sm, SMPhaseRequirement)
	})
	if err != nil {
		return c.failResult(result, sm, start, err), nil
	}

	// Phase 2: Design
	err = c.runPhase(ctx, SMPhaseDesign, func(pctx context.Context) error {
		if err := session.StartPhase(PhaseDesign); err != nil {
			return err
		}
		c.notify(SMPhaseDesign, "started")
		generated, genErr := c.deps.Designer.Generate(pctx, spec)
		if genErr != nil {
			_ = session.FailPhase(PhaseDesign, genErr.Error())
			return genErr
		}
		proposal = generated
		session.SetContext("design_proposal", proposal)
		result.PhaseResults[SMPhaseDesign] = proposal
		if err := session.CompletePhase(PhaseDesign, []string{proposal.ProposalID}); err != nil {
			return err
		}
		return c.advanceSM(sm, SMPhaseDesign)
	})
	if err != nil {
		return c.failResult(result, sm, start, err), nil
	}

	// Phase 3: Review（可选）
	if c.config.EnableReview && c.deps.Reviewer != nil {
		err = c.runPhase(ctx, SMPhaseReview, func(pctx context.Context) error {
			if err := session.StartPhase(PhaseReview); err != nil {
				return err
			}
			c.notify(SMPhaseReview, "started")
			reviewResult, revErr := c.deps.Reviewer.Execute(pctx, proposal)
			if revErr != nil {
				_ = session.FailPhase(PhaseReview, revErr.Error())
				return revErr
			}
			result.PhaseResults[SMPhaseReview] = reviewResult
			if !reviewResult.Converged {
				_ = session.FailPhase(PhaseReview, "方案审核未通过")
				sm.SetData("phase_status", "")
				if sm.CanFire("rollback") {
					_ = sm.Fire("rollback")
				}
				c.notify(SMPhaseReview, "rollback")
				return fmt.Errorf("方案审核未通过，需回退至设计阶段")
			}
			if reviewResult.FinalProposal != nil {
				proposal = reviewResult.FinalProposal
				session.SetContext("design_proposal", proposal)
			}
			if err := session.CompletePhase(PhaseReview, nil); err != nil {
				return err
			}
			return c.advanceSM(sm, SMPhaseReview)
		})
		if err != nil {
			return c.failResult(result, sm, start, err), nil
		}
	} else {
		c.skipPhase(session, sm, PhaseReview, SMPhaseReview, "审核阶段已禁用或无审核器")
	}

	// Phase 4: Execution
	err = c.runPhase(ctx, SMPhaseExecution, func(pctx context.Context) error {
		if err := session.StartPhase(PhaseExecution); err != nil {
			return err
		}
		c.notify(SMPhaseExecution, "started")
		plan := c.deps.Designer.ToTaskPlan(proposal)
		if plan == nil {
			_ = session.FailPhase(PhaseExecution, "无法生成执行计划")
			return fmt.Errorf("ToTaskPlan 返回 nil")
		}
		execPlan, buildErr := c.deps.Scheduler.BuildExecutionPlan(plan)
		if buildErr != nil {
			_ = session.FailPhase(PhaseExecution, buildErr.Error())
			return buildErr
		}
		// 模拟执行：逐层标记任务完成
		for {
			batch := c.deps.Scheduler.NextBatch(execPlan)
			if len(batch) == 0 {
				if !c.deps.Scheduler.AdvanceLayer(execPlan) {
					break
				}
				continue
			}
			for _, task := range batch {
				c.deps.Scheduler.MarkRunning(task, string(task.Task.Kind))
				c.deps.Scheduler.MarkCompleted(task, "模拟执行完成", task.Task.EstimatedTurns)
			}
			if !c.deps.Scheduler.AdvanceLayer(execPlan) {
				break
			}
		}
		session.SetContext("execution_plan", execPlan)
		result.PhaseResults[SMPhaseExecution] = c.deps.Scheduler.Progress(execPlan)
		if err := session.CompletePhase(PhaseExecution, []string{execPlan.PlanID}); err != nil {
			return err
		}
		return c.advanceSM(sm, SMPhaseExecution)
	})
	if err != nil {
		return c.failResult(result, sm, start, err), nil
	}

	// Phase 5: Acceptance（可选）
	if c.config.EnableAcceptance && c.deps.Tester != nil {
		err = c.runPhase(ctx, SMPhaseAcceptance, func(pctx context.Context) error {
			if err := session.StartPhase(PhaseAcceptance); err != nil {
				return err
			}
			c.notify(SMPhaseAcceptance, "started")
			suite, genErr := c.deps.Tester.GenerateTests(pctx, spec, proposal)
			if genErr != nil {
				_ = session.FailPhase(PhaseAcceptance, genErr.Error())
				return genErr
			}
			artifacts := make(map[string]string, len(suite.Tests))
			for _, t := range suite.Tests {
				artifacts[t.TestID] = "模拟交付物"
			}
			report, runErr := c.deps.Tester.RunTests(pctx, suite, artifacts)
			if runErr != nil {
				_ = session.FailPhase(PhaseAcceptance, runErr.Error())
				return runErr
			}
			result.PhaseResults[SMPhaseAcceptance] = report
			if c.deps.Tester.Verdict(report) == "rejected" {
				_ = session.FailPhase(PhaseAcceptance, "验收不通过")
				sm.SetData("phase_status", "")
				if sm.CanFire("rollback") {
					_ = sm.Fire("rollback")
				}
				c.notify(SMPhaseAcceptance, "rollback")
				return fmt.Errorf("验收不通过: rejected")
			}
			if err := session.CompletePhase(PhaseAcceptance, []string{report.ReportID}); err != nil {
				return err
			}
			return c.advanceSM(sm, SMPhaseAcceptance)
		})
		if err != nil {
			return c.failResult(result, sm, start, err), nil
		}
	} else {
		c.skipPhase(session, sm, PhaseAcceptance, SMPhaseAcceptance, "验收阶段已禁用或无测试器")
	}

	// Phase 6: Delivery
	err = c.runPhase(ctx, SMPhaseDelivery, func(pctx context.Context) error {
		if err := session.StartPhase(PhaseDelivery); err != nil {
			return err
		}
		c.notify(SMPhaseDelivery, "started")
		if c.deps.Deliverer != nil {
			delivery, delErr := c.deps.Deliverer.GenerateDelivery(pctx, session)
			if delErr != nil {
				_ = session.FailPhase(PhaseDelivery, delErr.Error())
				return delErr
			}
			result.PhaseResults[SMPhaseDelivery] = delivery
		} else {
			result.PhaseResults[SMPhaseDelivery] = "交付生成器未配置，跳过"
		}
		if err := session.CompletePhase(PhaseDelivery, nil); err != nil {
			return err
		}
		return c.advanceSM(sm, SMPhaseDelivery)
	})
	if err != nil {
		return c.failResult(result, sm, start, err), nil
	}

	// Phase 7: Retrospective（可选）
	if c.config.EnableRetrospec && c.deps.Retrospect != nil {
		err = c.runPhase(ctx, SMPhaseRetrospect, func(pctx context.Context) error {
			if err := session.StartPhase(PhaseRetrospect); err != nil {
				return err
			}
			c.notify(SMPhaseRetrospect, "started")
			retro, retroErr := c.deps.Retrospect.RunRetrospective(pctx, session)
			if retroErr != nil {
				_ = session.FailPhase(PhaseRetrospect, retroErr.Error())
				return retroErr
			}
			result.PhaseResults[SMPhaseRetrospect] = retro
			if err := session.CompletePhase(PhaseRetrospect, nil); err != nil {
				return err
			}
			return c.advanceSM(sm, SMPhaseRetrospect)
		})
		if err != nil {
			return c.failResult(result, sm, start, err), nil
		}
	} else {
		c.skipPhase(session, sm, PhaseRetrospect, SMPhaseRetrospect, "复盘阶段已禁用或无复盘器")
	}

	// 完成
	result.Success = true
	result.FinalPhase = sm.CurrentPhase()
	result.TotalDuration = time.Since(start)
	_ = c.deps.SessionStore.SaveSession(ctx, session)
	return result, nil
}

// runPhase 通用阶段执行包装器（超时 + 重试 + 取消检查）。
func (c *ClosedLoopController) runPhase(ctx context.Context, phase string, fn func(context.Context) error) error {
	var lastErr error
	for attempt := 0; attempt <= c.config.MaxRetries; attempt++ {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		pctx, cancel := context.WithTimeout(ctx, c.config.PhaseTimeout)
		err := fn(pctx)
		cancel()
		if err == nil {
			return nil
		}
		lastErr = err
		if attempt >= c.config.MaxRetries {
			break
		}
		c.notify(phase, fmt.Sprintf("retry_%d", attempt+1))
	}
	return fmt.Errorf("阶段 %s 在 %d 次尝试后失败: %w", phase, c.config.MaxRetries+1, lastErr)
}

// advanceSM 设置状态机 phase_status=done 并 Fire("advance")，通知完成。
func (c *ClosedLoopController) advanceSM(sm *ProjectStateMachine, phase string) error {
	sm.SetData("phase_status", "done")
	if err := sm.Fire("advance"); err != nil {
		return fmt.Errorf("状态机推进失败: %w", err)
	}
	c.notify(phase, "completed")
	return nil
}

// skipPhase 跳过指定阶段并推进状态机。
func (c *ClosedLoopController) skipPhase(session *ProjectSession, sm *ProjectStateMachine, pt ProjectPhaseType, smPhase, reason string) {
	_ = session.SkipPhase(pt, reason)
	sm.SetData("phase_status", "done")
	_ = sm.Fire("skip")
	c.notify(smPhase, "skipped")
}

// notify 调用阶段变更回调（如果已配置）。
func (c *ClosedLoopController) notify(phase, event string) {
	if c.config.OnPhaseChange != nil {
		c.config.OnPhaseChange(phase, event)
	}
}

// failResult 构造失败的 ClosedLoopResult。
func (c *ClosedLoopController) failResult(r *ClosedLoopResult, sm *ProjectStateMachine, start time.Time, err error) *ClosedLoopResult {
	r.Success = false
	r.FinalPhase = sm.CurrentPhase()
	r.TotalDuration = time.Since(start)
	r.Error = err.Error()
	return r
}
