package agent

import (
	"AgentCLI/internal/plan"
	"testing"
)

func TestResourceTrackerAllowsSharedReads(t *testing.T) {
	tracker := newResourceTracker()
	readerOne := resourceTask([]string{"shared.go"}, nil)
	readerTwo := resourceTask([]string{"./shared.go"}, nil)

	tracker.acquire(readerOne)
	defer tracker.release(readerOne)

	if tracker.conflicts(readerTwo) {
		t.Fatal("two readers of the same resource should not conflict")
	}
}

func TestResourceTrackerRejectsReadWriteConflict(t *testing.T) {
	tracker := newResourceTracker()
	reader := resourceTask([]string{"shared.go"}, nil)
	writer := resourceTask(nil, []string{"./shared.go"})

	tracker.acquire(reader)
	defer tracker.release(reader)

	if !tracker.conflicts(writer) {
		t.Fatal("writer should conflict with an active reader")
	}
}

func resourceTask(reads, writes []string) *plan.Task {
	task := plan.NewTask("task", "资源任务", "测试资源冲突", plan.ANALYSIS, nil)
	task.SetResources(reads, writes)
	return task
}
