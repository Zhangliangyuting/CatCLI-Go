package agent

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/plan"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type workflowPlanGenerator struct {
	generated        *plan.Plan
	revised          *plan.Plan
	replanned        *plan.Plan
	generateErr      error
	reviseErr        error
	replanErr        error
	reviseCalls      int
	replanCalls      int
	revisionFeedback string
}

func (g *workflowPlanGenerator) Generate(
	context.Context,
	[]llm.Message,
) (*plan.Plan, error) {
	return g.generated, g.generateErr
}

func (g *workflowPlanGenerator) Revise(
	_ context.Context,
	_ *plan.Plan,
	feedback string,
	_ []llm.Message,
) (*plan.Plan, error) {
	g.reviseCalls++
	g.revisionFeedback = feedback
	return g.revised, g.reviseErr
}

func (g *workflowPlanGenerator) Replan(
	_ context.Context,
	p *plan.Plan,
	_ string,
	_ []llm.Message,
) (*plan.Plan, error) {
	g.replanCalls++
	if g.replanned != nil {
		return g.replanned, g.replanErr
	}
	return p, g.replanErr
}

type workflowReviewer struct {
	actions   []PlanAction
	feedbacks []string
	plans     []*plan.Plan
}

func (r *workflowReviewer) Review(p *plan.Plan) (PlanAction, string, error) {
	r.plans = append(r.plans, p)
	index := len(r.plans) - 1
	feedback := ""
	if index < len(r.feedbacks) {
		feedback = r.feedbacks[index]
	}
	return r.actions[index], feedback, nil
}

type workflowExecutor struct {
	result string
	err    error
}

func (e *workflowExecutor) Run(context.Context, string) (string, error) {
	return e.result, e.err
}

type workflowExecutorFunc func(string) (string, error)

func (f workflowExecutorFunc) Run(
	_ context.Context,
	input string,
) (string, error) {
	return f(input)
}

type contextWorkflowExecutorFunc func(
	context.Context,
	string,
) (string, error)

func (f contextWorkflowExecutorFunc) Run(
	ctx context.Context,
	input string,
) (string, error) {
	return f(ctx, input)
}

func TestTaskAgentFactoryReceivesTaskID(t *testing.T) {
	p := plan.NewPlan("plan_1", "任务工厂", "传递任务 ID")
	if err := p.AddTask(plan.NewTask(
		"task_memory",
		"记忆任务",
		"验证独立任务 Agent",
		plan.ANALYSIS,
		nil,
	)); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	var receivedTaskID string
	scheduler := newPlanSchedulerWithTaskFactory(
		1,
		0,
		func(taskID string) (Agent, error) {
			receivedTaskID = taskID
			return &workflowExecutor{result: "done"}, nil
		},
	)

	if _, err := scheduler.Execute(context.Background(), p); err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if receivedTaskID != "task_memory" {
		t.Fatalf("task factory ID = %q, want task_memory", receivedTaskID)
	}
}

func TestExecutePlanDynamicallySchedulesReadyTasks(t *testing.T) {
	p := plan.NewPlan("plan_1", "动态执行", "测试动态调度")
	for _, task := range []*plan.Task{
		plan.NewTask("task_1", "慢任务", "执行慢任务", plan.ANALYSIS, nil),
		plan.NewTask("task_2", "快速任务", "执行快速任务", plan.ANALYSIS, nil),
		plan.NewTask("task_3", "后继任务", "执行后继任务", plan.ANALYSIS, []string{"task_2"}),
	} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	releaseSlowTask := make(chan struct{})
	dependentStarted := make(chan struct{})
	var signalDependent sync.Once

	scheduler := newPlanScheduler(2, 0, func() Agent {
		return workflowExecutorFunc(func(prompt string) (string, error) {
			switch {
			case strings.Contains(prompt, "执行慢任务"):
				<-releaseSlowTask
			case strings.Contains(prompt, "执行后继任务"):
				signalDependent.Do(func() { close(dependentStarted) })
			}
			return "完成", nil
		})
	})

	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Execute(context.Background(), p)
		done <- err
	}()

	select {
	case <-dependentStarted:
		// task_3 在不相关的慢任务 task_1 完成前启动，证明调度是动态的。
	case <-time.After(time.Second):
		close(releaseSlowTask)
		t.Fatal("dependent task did not start while an unrelated task was running")
	}

	close(releaseSlowTask)
	if err := <-done; err != nil {
		t.Fatalf("executePlan() error = %v", err)
	}
	if p.Status() != plan.PLAN_COMPLETED {
		t.Fatalf("plan status = %s, want COMPLETED", p.Status())
	}
}

func TestExecutePlanSerializesTasksWritingSameResource(t *testing.T) {
	p := plan.NewPlan("plan_1", "避免写冲突", "相同资源串行执行")
	first := plan.NewTask("task_1", "第一次修改", "执行第一次修改", plan.FILE_WRITE, nil)
	first.SetResources(nil, []string{"shared.go"})
	second := plan.NewTask("task_2", "第二次修改", "执行第二次修改", plan.FILE_WRITE, nil)
	second.SetResources(nil, []string{"./shared.go"})
	for _, task := range []*plan.Task{first, second} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	firstStarted := make(chan struct{})
	secondStarted := make(chan struct{})
	releaseFirst := make(chan struct{})
	scheduler := newPlanScheduler(2, 0, func() Agent {
		return workflowExecutorFunc(func(prompt string) (string, error) {
			if strings.Contains(prompt, "执行第一次修改") {
				close(firstStarted)
				<-releaseFirst
			} else if strings.Contains(prompt, "执行第二次修改") {
				close(secondStarted)
			}
			return "完成", nil
		})
	})

	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Execute(context.Background(), p)
		done <- err
	}()

	select {
	case <-firstStarted:
	case <-time.After(time.Second):
		t.Fatal("first task did not start")
	}
	select {
	case <-secondStarted:
		t.Fatal("conflicting task started before the first task completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(releaseFirst)
	select {
	case <-secondStarted:
	case <-time.After(time.Second):
		t.Fatal("second task did not start after resource was released")
	}
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecutePlanRunsTasksWritingDifferentResourcesConcurrently(t *testing.T) {
	p := plan.NewPlan("plan_1", "并行写入", "不同资源并行执行")
	first := plan.NewTask("task_1", "修改一", "修改文件一", plan.FILE_WRITE, nil)
	first.SetResources(nil, []string{"first.go"})
	second := plan.NewTask("task_2", "修改二", "修改文件二", plan.FILE_WRITE, nil)
	second.SetResources(nil, []string{"second.go"})
	for _, task := range []*plan.Task{first, second} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	started := make(chan struct{}, 2)
	release := make(chan struct{})
	scheduler := newPlanScheduler(2, 0, func() Agent {
		return workflowExecutorFunc(func(string) (string, error) {
			started <- struct{}{}
			<-release
			return "完成", nil
		})
	})

	done := make(chan error, 1)
	go func() {
		_, err := scheduler.Execute(context.Background(), p)
		done <- err
	}()
	for range 2 {
		select {
		case <-started:
		case <-time.After(time.Second):
			close(release)
			t.Fatal("tasks using different resources did not run concurrently")
		}
	}
	close(release)
	if err := <-done; err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
}

func TestExecutePlanStopsSchedulingAfterFailure(t *testing.T) {
	p := plan.NewPlan("plan_1", "失败停止", "测试失败后停止调度")
	failedTask := plan.NewTask("task_1", "失败任务", "执行失败任务", plan.ANALYSIS, nil)
	blockedTask := plan.NewTask("task_2", "后继任务", "不应执行", plan.ANALYSIS, []string{"task_1"})
	for _, task := range []*plan.Task{failedTask, blockedTask} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	executorCalls := 0
	scheduler := newPlanScheduler(2, 0, func() Agent {
		executorCalls++
		return &workflowExecutor{err: errors.New("boom")}
	})

	_, err := scheduler.Execute(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("executePlan() error = %v, want boom", err)
	}
	if executorCalls != 1 {
		t.Fatalf("executor calls = %d, want 1", executorCalls)
	}
	if blockedTask.Status() != plan.PENDING {
		t.Fatalf("dependent task status = %s, want PENDING", blockedTask.Status())
	}
}

func TestExecutePlanCancelsRunningSiblingAfterFailure(t *testing.T) {
	p := plan.NewPlan("plan_1", "失败取消", "取消正在运行的同批任务")
	for _, task := range []*plan.Task{
		plan.NewTask("task_1", "失败任务", "执行失败任务", plan.ANALYSIS, nil),
		plan.NewTask("task_2", "等待任务", "等待取消信号", plan.ANALYSIS, nil),
	} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask() error = %v", err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	waitingStarted := make(chan struct{})
	waitingCancelled := make(chan struct{})
	scheduler := newPlanScheduler(2, 0, func() Agent {
		return contextWorkflowExecutorFunc(
			func(ctx context.Context, prompt string) (string, error) {
				switch {
				case strings.Contains(prompt, "等待取消信号"):
					close(waitingStarted)
					<-ctx.Done()
					close(waitingCancelled)
					return "", ctx.Err()
				case strings.Contains(prompt, "执行失败任务"):
					<-waitingStarted
					return "", errors.New("boom")
				default:
					return "完成", nil
				}
			},
		)
	})

	_, err := scheduler.Execute(context.Background(), p)
	if err == nil || !strings.Contains(err.Error(), "boom") {
		t.Fatalf("Execute() error = %v, want boom", err)
	}
	select {
	case <-waitingCancelled:
	default:
		t.Fatal("running sibling did not receive cancellation")
	}
	waitingTask, _ := p.TaskByID("task_2")
	if waitingTask.Status() != plan.CANCELLED {
		t.Fatalf("waiting task status = %s, want CANCELLED", waitingTask.Status())
	}
}

func TestExecutePlanAppliesTaskTimeout(t *testing.T) {
	p := newWorkflowPlan(t, "任务超时")
	var events []Event
	scheduler := newPlanScheduler(
		1,
		20*time.Millisecond,
		func() Agent {
			return contextWorkflowExecutorFunc(
				func(ctx context.Context, _ string) (string, error) {
					<-ctx.Done()
					return "", ctx.Err()
				},
			)
		},
	)

	_, err := scheduler.ExecuteWithObserver(
		context.Background(),
		p,
		func(event Event) { events = append(events, event) },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Execute() error = %v, want context.DeadlineExceeded", err)
	}
	task, _ := p.TaskByID("task_1")
	if task.Status() != plan.FAILED {
		t.Fatalf("task status = %s, want FAILED", task.Status())
	}
	if !containsEventType(events, EventTaskTimeout) {
		t.Fatalf("events = %#v, want EventTaskTimeout", events)
	}
}

func TestPlanAndExecuteAgentReviewsThenExecutes(t *testing.T) {
	p := newWorkflowPlan(t, "原计划")
	reviewer := &workflowReviewer{actions: []PlanAction{PlanExecute}}
	generator := &workflowPlanGenerator{generated: p}

	a := NewPlanAndExecuteAgent(
		generator,
		func() Agent { return &workflowExecutor{result: "完成"} },
		reviewer,
		0,
		2,
		0,
		0,
	)

	result, err := a.Run(context.Background(), "完成目标")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !strings.Contains(result, "task_1: 完成") {
		t.Fatalf("Run() result = %q, want task result", result)
	}
	if p.Status() != plan.PLAN_COMPLETED {
		t.Fatalf("plan status = %s, want COMPLETED", p.Status())
	}
}

func TestPlanAndExecuteAgentRevisesThenReviewsAgain(t *testing.T) {
	original := newWorkflowPlan(t, "原计划")
	revised := newWorkflowPlan(t, "修订计划")
	reviewer := &workflowReviewer{
		actions:   []PlanAction{PlanRevise, PlanExecute},
		feedbacks: []string{"增加验证步骤"},
	}
	generator := &workflowPlanGenerator{generated: original, revised: revised}

	a := NewPlanAndExecuteAgent(
		generator,
		func() Agent { return &workflowExecutor{result: "完成"} },
		reviewer,
		0,
		2,
		0,
		0,
	)

	if _, err := a.Run(context.Background(), "完成目标"); err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if generator.reviseCalls != 1 || generator.revisionFeedback != "增加验证步骤" {
		t.Fatalf("Revise() calls/feedback = %d/%q", generator.reviseCalls, generator.revisionFeedback)
	}
	if len(reviewer.plans) != 2 || reviewer.plans[1] != revised {
		t.Fatal("revised plan was not shown for review")
	}
}

func TestPlanAndExecuteAgentCancelsWithoutExecuting(t *testing.T) {
	p := newWorkflowPlan(t, "取消计划")
	reviewer := &workflowReviewer{actions: []PlanAction{PlanCancel}}
	executorCalled := false

	a := NewPlanAndExecuteAgent(
		&workflowPlanGenerator{generated: p},
		func() Agent {
			executorCalled = true
			return &workflowExecutor{}
		},
		reviewer,
		0,
		2,
		0,
		0,
	)

	result, err := a.Run(context.Background(), "取消目标")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if executorCalled {
		t.Fatal("executor was called after cancellation")
	}
	if p.Status() != plan.PLAN_CANCELLED {
		t.Fatalf("plan status = %s, want CANCELLED", p.Status())
	}
	if !strings.Contains(result, "已取消") {
		t.Fatalf("Run() result = %q, want cancellation message", result)
	}
}

func TestPlanAndExecuteAgentReplansAfterExecutionFailure(t *testing.T) {
	original := newWorkflowPlan(t, "原计划")
	replanned := newWorkflowPlan(t, "替代计划")
	generator := &workflowPlanGenerator{
		generated: original,
		replanned: replanned,
	}
	reviewer := &workflowReviewer{actions: []PlanAction{PlanExecute}}
	executors := []Agent{
		&workflowExecutor{err: errors.New("执行失败")},
		&workflowExecutor{result: "恢复完成"},
	}
	executorIndex := 0

	a := NewPlanAndExecuteAgent(
		generator,
		func() Agent {
			executor := executors[executorIndex]
			executorIndex++
			return executor
		},
		reviewer,
		1,
		2,
		0,
		0,
	)

	result, err := a.Run(context.Background(), "完成目标")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if generator.replanCalls != 1 {
		t.Fatalf("Replan() calls = %d, want 1", generator.replanCalls)
	}
	if !strings.Contains(result, "恢复完成") {
		t.Fatalf("Run() result = %q, want replacement plan result", result)
	}
	if a.CurrentPlan() != replanned || replanned.Status() != plan.PLAN_COMPLETED {
		t.Fatal("replacement plan was not executed to completion")
	}
}

func TestPlanAndExecuteAgentDoesNotReplanAfterPlanTimeout(t *testing.T) {
	p := newWorkflowPlan(t, "计划超时")
	generator := &workflowPlanGenerator{generated: p}
	a := NewPlanAndExecuteAgent(
		generator,
		func() Agent {
			return contextWorkflowExecutorFunc(
				func(ctx context.Context, _ string) (string, error) {
					<-ctx.Done()
					return "", ctx.Err()
				},
			)
		},
		&workflowReviewer{actions: []PlanAction{PlanExecute}},
		3,
		1,
		0,
		20*time.Millisecond,
	)

	_, err := a.Run(context.Background(), "执行超时计划")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want context.DeadlineExceeded", err)
	}
	if generator.replanCalls != 0 {
		t.Fatalf("Replan() calls = %d, want 0", generator.replanCalls)
	}
}

func TestPlanAndExecuteAgentReturnsReviewError(t *testing.T) {
	reviewErr := errors.New("review failed")
	reviewer := PlanReviewerFunc(func(*plan.Plan) (PlanAction, string, error) {
		return "", "", reviewErr
	})

	a := NewPlanAndExecuteAgent(
		&workflowPlanGenerator{generated: newWorkflowPlan(t, "计划")},
		func() Agent { return &workflowExecutor{} },
		reviewer,
		0,
		2,
		0,
		0,
	)

	_, err := a.Run(context.Background(), "目标")
	if !errors.Is(err, reviewErr) {
		t.Fatalf("Run() error = %v, want review error", err)
	}
}

type PlanReviewerFunc func(*plan.Plan) (PlanAction, string, error)

func (f PlanReviewerFunc) Review(p *plan.Plan) (PlanAction, string, error) {
	return f(p)
}

func newWorkflowPlan(t *testing.T, goal string) *plan.Plan {
	t.Helper()
	p := plan.NewPlan("plan_1", goal, "测试工作流程")
	if err := p.AddTask(plan.NewTask("task_1", "执行", "执行任务", plan.ANALYSIS, nil)); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}
	return p
}

func containsEventType(events []Event, want EventType) bool {
	for _, event := range events {
		if event.Type == want {
			return true
		}
	}
	return false
}
