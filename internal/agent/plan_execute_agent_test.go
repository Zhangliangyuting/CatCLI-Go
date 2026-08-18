package agent

import (
	"AgentCLI/internal/plan"
	"errors"
	"strings"
	"testing"
)

type fakePlanGenerator struct {
	plan *plan.Plan
	err  error
}

func (g *fakePlanGenerator) Generate(string) (*plan.Plan, error) {
	return g.plan, g.err
}

type fakeAgent struct {
	result string
	err    error
	input  string
}

func (a *fakeAgent) Run(input string) (string, error) {
	a.input = input
	return a.result, a.err
}

func TestPlanAndExecuteAgentRun(t *testing.T) {
	p := newTestPlan(t)
	executors := []*fakeAgent{
		{result: "读取结果"},
		{result: "验证结果"},
	}
	factoryCalls := 0

	a := NewPlanAndExecuteAgent(
		&fakePlanGenerator{plan: p},
		func() Agent {
			executor := executors[factoryCalls]
			factoryCalls++
			return executor
		},
	)

	result, err := a.Run("检查项目")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}

	if factoryCalls != 2 {
		t.Fatalf("executor factory calls = %d, want 2", factoryCalls)
	}
	if !strings.Contains(result, "task_1: 读取结果") || !strings.Contains(result, "task_2: 验证结果") {
		t.Fatalf("Run() result = %q, want both task results", result)
	}

	task1, _ := p.TaskByID("task_1")
	task2, _ := p.TaskByID("task_2")
	if task1.Status() != plan.COMPLETED || task1.Result() != "读取结果" {
		t.Errorf("task_1 status/result = %s/%q, want COMPLETED/读取结果", task1.Status(), task1.Result())
	}
	if task2.Status() != plan.COMPLETED || task2.Result() != "验证结果" {
		t.Errorf("task_2 status/result = %s/%q, want COMPLETED/验证结果", task2.Status(), task2.Result())
	}

	secondPrompt := executors[1].input
	for _, want := range []string{"整体目标：\n检查项目", "当前任务：\n验证读取结果", "读取项目文件", "读取结果"} {
		if !strings.Contains(secondPrompt, want) {
			t.Errorf("second task prompt does not contain %q; prompt = %q", want, secondPrompt)
		}
	}
}

func TestPlanAndExecuteAgentStopsOnTaskFailure(t *testing.T) {
	p := newTestPlanWithThirdTask(t)
	executionErr := errors.New("executor failed")
	executors := []*fakeAgent{
		{result: "读取结果"},
		{err: executionErr},
		{result: "不应执行"},
	}
	factoryCalls := 0

	a := NewPlanAndExecuteAgent(
		&fakePlanGenerator{plan: p},
		func() Agent {
			executor := executors[factoryCalls]
			factoryCalls++
			return executor
		},
	)

	_, err := a.Run("检查项目")
	if !errors.Is(err, executionErr) {
		t.Fatalf("Run() error = %v, want wrapped executor error", err)
	}
	if factoryCalls != 2 {
		t.Fatalf("executor factory calls = %d, want execution to stop after 2", factoryCalls)
	}

	task1, _ := p.TaskByID("task_1")
	task2, _ := p.TaskByID("task_2")
	task3, _ := p.TaskByID("task_3")
	if task1.Status() != plan.COMPLETED {
		t.Errorf("task_1 status = %s, want COMPLETED", task1.Status())
	}
	if task2.Status() != plan.FAILED || !errors.Is(task2.Error(), executionErr) {
		t.Errorf("task_2 status/error = %s/%v, want FAILED/executor error", task2.Status(), task2.Error())
	}
	if task3.Status() != plan.PENDING {
		t.Errorf("task_3 status = %s, want PENDING", task3.Status())
	}
	if !p.HasFailed() {
		t.Error("Plan.HasFailed() = false, want true")
	}
}

func TestPlanAndExecuteAgentReturnsGeneratorError(t *testing.T) {
	generationErr := errors.New("generation failed")
	factoryCalled := false
	a := NewPlanAndExecuteAgent(
		&fakePlanGenerator{err: generationErr},
		func() Agent {
			factoryCalled = true
			return &fakeAgent{}
		},
	)

	_, err := a.Run("检查项目")
	if !errors.Is(err, generationErr) {
		t.Fatalf("Run() error = %v, want wrapped generator error", err)
	}
	if factoryCalled {
		t.Error("executor factory was called after plan generation failed")
	}
}

func newTestPlan(t *testing.T) *plan.Plan {
	t.Helper()

	p := plan.NewPlan("plan_1", "检查项目")
	task1 := plan.NewTask("task_1", "读取", "读取项目文件", plan.FILE_READ, nil)
	task2 := plan.NewTask("task_2", "验证", "验证读取结果", plan.VERIFICATION, []string{"task_1"})

	for _, task := range []*plan.Task{task1, task2} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask(%s) error = %v", task.ID(), err)
		}
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	return p
}

func newTestPlanWithThirdTask(t *testing.T) *plan.Plan {
	t.Helper()

	p := newTestPlan(t)
	task3 := plan.NewTask("task_3", "总结", "总结验证结果", plan.ANALYSIS, []string{"task_2"})
	if err := p.AddTask(task3); err != nil {
		t.Fatalf("AddTask(%s) error = %v", task3.ID(), err)
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	return p
}
