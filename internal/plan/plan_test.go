package plan

import (
	"reflect"
	"strings"
	"testing"
)

func TestTopologicalSortUsesDependencyOrder(t *testing.T) {
	p := NewPlan("plan_1", "执行三个任务")

	task1 := NewTask("task_1", "一", "第一个任务", ANALYSIS, nil)
	task2 := NewTask("task_2", "二", "第二个任务", ANALYSIS, []string{"task_1"})
	task3 := NewTask("task_3", "三", "第三个任务", VERIFICATION, []string{"task_2"})

	for _, task := range []*Task{task1, task2, task3} {
		if err := p.AddTask(task); err != nil {
			t.Fatalf("AddTask(%s) error = %v", task.ID(), err)
		}
	}

	if err := p.TopologicalSort(); err != nil {
		t.Fatalf("TopologicalSort() error = %v", err)
	}

	want := []string{"task_1", "task_2", "task_3"}
	if got := p.ExecutionOrder(); !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutionOrder() = %v, want %v", got, want)
	}
}

func TestTopologicalSortDetectsCycle(t *testing.T) {
	p := NewPlan("plan_1", "循环依赖")
	task1 := NewTask("task_1", "一", "第一个任务", ANALYSIS, nil)
	task2 := NewTask("task_2", "二", "第二个任务", ANALYSIS, nil)

	if err := p.AddTask(task1); err != nil {
		t.Fatal(err)
	}
	if err := p.AddTask(task2); err != nil {
		t.Fatal(err)
	}

	task1.SetDependencies([]string{"task_2"})
	task2.SetDependencies([]string{"task_1"})

	err := p.TopologicalSort()
	if err == nil {
		t.Fatal("TopologicalSort() error = nil, want cycle error")
	}
	if !strings.Contains(err.Error(), "cycle detected") {
		t.Fatalf("TopologicalSort() error = %q, want cycle detected", err)
	}
}
