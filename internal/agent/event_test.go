package agent

import (
	"AgentCLI/internal/plan"
	"context"
	"testing"
)

type observableWorkflowExecutor struct{}

func (e *observableWorkflowExecutor) Run(
	context.Context,
	string,
) (string, error) {
	return "完成", nil
}

func (e *observableWorkflowExecutor) RunWithObserver(
	_ context.Context,
	_ string,
	observer Observer,
) (string, error) {
	emit(observer, Event{
		Type:    EventToolCall,
		Title:   "read_file",
		Content: `{"path":"README.md"}`,
	})
	return "完成", nil
}

func TestSchedulerEmitsTaskEventsAndAddsTaskID(t *testing.T) {
	p := plan.NewPlan("plan_1", "执行计划", "验证事件")
	if err := p.AddTask(plan.NewTask(
		"task_1",
		"读取文件",
		"读取 README",
		plan.ANALYSIS,
		nil,
	)); err != nil {
		t.Fatalf("AddTask() error = %v", err)
	}
	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	var events []Event
	scheduler := newPlanScheduler(1, 0, func() Agent {
		return &observableWorkflowExecutor{}
	})

	if _, err := scheduler.ExecuteWithObserver(
		context.Background(),
		p,
		func(event Event) { events = append(events, event) },
	); err != nil {
		t.Fatalf("ExecuteWithObserver() error = %v", err)
	}

	wantTypes := []EventType{
		EventTaskStarted,
		EventToolCall,
		EventTaskCompleted,
		EventPlanCompleted,
	}
	if len(events) != len(wantTypes) {
		t.Fatalf("event count = %d, want %d: %#v", len(events), len(wantTypes), events)
	}
	for i, wantType := range wantTypes {
		if events[i].Type != wantType {
			t.Errorf("events[%d].Type = %q, want %q", i, events[i].Type, wantType)
		}
	}
	if events[1].TaskID != "task_1" {
		t.Fatalf("tool event TaskID = %q, want task_1", events[1].TaskID)
	}
}
