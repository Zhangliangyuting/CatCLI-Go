package multiagent

import (
	executionlog "AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"context"
	"errors"
	"fmt"
)

var ErrRecoveryNeedsVerification = errors.New("interrupted tool calls require verification before plan recovery")

// Resume consumes the RunState already replayed and selected by the caller.
// The old executor must be stopped; no event-log reload or Replay happens here.
func (a *PlanAndExecuteAgent) Resume(ctx context.Context, selected executionlog.RunState) (answer string, err error) {
	if a.recorder == nil || selected.ConversationID != a.recorder.ConversationID() {
		return "", fmt.Errorf("resume requires a run from this conversation")
	}
	source := &selected
	if source.Status != executionlog.Interrupted || (source.Kind != "" && source.Kind != executionlog.PlanAndExecuteRun) || (source.Kind == "" && source.Plan == nil) {
		return "", fmt.Errorf("run is not an interrupted Plan-and-Execute run")
	}
	// Automatic verification is not implemented. Leave uncertain calls and the
	// original Run unchanged instead of treating a prompt as verified evidence.
	for _, call := range source.Calls {
		if call.Status == executionlog.Unknown {
			return "", fmt.Errorf("%w: %s", ErrRecoveryNeedsVerification, call.CallID)
		}
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	continued, err := a.freshRecoveryAgent()
	if err != nil {
		return "", err
	}
	var run *executionlog.RunRecorder
	var state executionlog.RunState
	if source.Plan == nil {
		// A crash during planning has no confirmed schedule to restore. Generate
		// a new draft and ask for confirmation through the ordinary plan gate.
		run, err = a.recorder.StartRun(ctx, source.Input, source.RunID, executionlog.PlanAndExecuteRun)
	} else {
		run, state, err = a.recorder.StartRecoveredRun(ctx, *source)
	}
	if err != nil {
		return "", err
	}
	returned := false
	defer func() {
		if returned {
			if finishErr := run.FinishRun(ctx, answer, err); finishErr != nil && !errors.Is(err, finishErr) {
				err = errors.Join(err, finishErr)
			}
		}
	}()
	if source.Plan == nil {
		ctx = context.WithValue(ctx, runRecorderKey{}, run)
		answer, err = continued.run(ctx, source.Input)
	} else {
		answer, err = continued.resumePlan(ctx, run, state)
	}
	returned = true
	a.currentPlan = continued.currentPlan
	if err != nil {
		return answer, err
	}
	if _, err := a.storePlanMessage(llm.UserMessage("继续上次中断的计划：" + source.Input)); err != nil {
		return answer, err
	}
	if _, err := a.storePlanMessage(llm.AssistantMessage(answer)); err != nil {
		return answer, err
	}
	return answer, nil
}

func (a *PlanAndExecuteAgent) freshRecoveryAgent() (*PlanAndExecuteAgent, error) {
	manager := memory.NewManager(nil)
	for _, fact := range a.rootMemory.Manager.Facts() {
		if err := manager.AddEntry(fact); err != nil {
			return nil, err
		}
	}
	continued := *a
	continued.currentPlan, continued.referenceMessages = nil, nil
	continued.rootMemory = a.rootMemory
	continued.rootMemory.Manager = manager
	continued.rootMemory.ContextBuilder = memory.NewContextBuilder(manager, a.rootMemory.ContextBuilder.Retriever, a.rootMemory.ContextBuilder.MaxRetrievedTokens)
	continued.scheduler = newTaskScheduler(a.scheduler.maxWorkers, a.scheduler.taskTimeout)
	// TaskExecutor is stateless; every job creates its own Worker memory runtime.
	continued.executor = newTaskExecutor(a.executor.client, a.executor.tools, a.memoryRuntime, a.executor.reviewer, a.ragContext)
	return &continued, nil
}

func (a *PlanAndExecuteAgent) resumePlan(ctx context.Context, run *executionlog.RunRecorder, state executionlog.RunState) (string, error) {
	latest := state.Plan
	for _, task := range state.Tasks {
		if task.Revision != latest.Revision {
			continue
		}
		if task.Status == executionlog.TaskInterrupted {
			// The old Worker is gone. Close its attempt explicitly before opening
			// another one; its tool facts remain available in the recovery context.
			ref := executionlog.TaskReference{Revision: task.Revision, TaskID: task.Definition.ID, Attempt: task.Attempt}
			if err := recordTask(ctx, run, executionlog.TaskCancelled, ref, task.Result, fmt.Errorf("previous worker interrupted; continue in a fresh attempt")); err != nil {
				return "", err
			}
		}
	}
	p, err := restorePlan(run)
	if err != nil {
		return "", err
	}
	a.currentQuery = state.Input
	a.referenceMessages, err = buildReferenceContext(ctx, a.ragContext, state.Input)
	if err != nil {
		return "", err
	}
	if _, err := a.storePlanMessage(llm.UserMessage("恢复确认计划：" + state.Input)); err != nil {
		return "", err
	}
	return a.executeWithReplan(context.WithValue(ctx, runRecorderKey{}, run), p)
}
