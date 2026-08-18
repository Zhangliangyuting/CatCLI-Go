package plan

import (
	"reflect"
	"strings"
	"testing"
)

func TestParsePlan(t *testing.T) {
	planJSON := `{
		"goal": "发布应用",
		"summary": "构建并验证应用",
		"tasks": [
			{
				"id": "build",
				"name": "构建",
				"description": "构建应用",
				"type": "COMMAND",
				"dependencies": []
			},
			{
				"id": "verify",
				"name": "验证",
				"description": "运行测试验证应用",
				"type": "VERIFICATION",
				"dependencies": ["build"]
			}
		]
	}`

	p, err := parsePlan(planJSON)
	if err != nil {
		t.Fatalf("parsePlan() error = %v", err)
	}

	if got, want := p.Goal(), "发布应用"; got != want {
		t.Fatalf("Goal() = %q, want %q", got, want)
	}

	if got, want := p.summary, "构建并验证应用"; got != want {
		t.Fatalf("summary = %q, want %q", got, want)
	}

	if got, want := p.ExecutionOrder(), []string{"task_1", "task_2"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("ExecutionOrder() = %v, want %v", got, want)
	}

	buildTask, ok := p.TaskByID("task_1")
	if !ok {
		t.Fatal("task_1 was not created")
	}
	if got, want := buildTask.Description(), "构建应用"; got != want {
		t.Errorf("task_1 Description() = %q, want %q", got, want)
	}

	verifyTask, ok := p.TaskByID("task_2")
	if !ok {
		t.Fatal("task_2 was not created")
	}
	if got, want := verifyTask.Dependencies(), []string{"task_1"}; !reflect.DeepEqual(got, want) {
		t.Errorf("task_2 Dependencies() = %v, want %v", got, want)
	}
	if got, want := buildTask.Dependents(), []string{"task_2"}; !reflect.DeepEqual(got, want) {
		t.Errorf("task_1 Dependents() = %v, want %v", got, want)
	}
}

func TestParsePlanMarkdownCodeBlock(t *testing.T) {
	planJSON := "```json\n" + `{
		"goal": "测试",
		"summary": "测试代码块",
		"tasks": [{
			"id": "task_1",
			"name": "分析",
			"description": "分析输入",
			"type": "ANALYSIS",
			"dependencies": []
		}]
	}` + "\n```"

	if _, err := parsePlan(planJSON); err != nil {
		t.Fatalf("parsePlan() error = %v", err)
	}
}

func TestParsePlanRejectsInvalidPlans(t *testing.T) {
	tests := []struct {
		name       string
		planJSON   string
		wantErrSub string
	}{
		{
			name:       "empty tasks",
			planJSON:   `{"goal":"测试","tasks":[]}`,
			wantErrSub: "plan has no tasks",
		},
		{
			name: "duplicate task ID",
			planJSON: `{"goal":"测试","tasks":[
				{"id":"same","description":"任务一","type":"ANALYSIS","dependencies":[]},
				{"id":"same","description":"任务二","type":"ANALYSIS","dependencies":[]}
			]}`,
			wantErrSub: "duplicate raw task id",
		},
		{
			name:       "empty description",
			planJSON:   `{"goal":"测试","tasks":[{"id":"one","description":"","type":"ANALYSIS","dependencies":[]}]}`,
			wantErrSub: "task description is empty",
		},
		{
			name:       "invalid task type",
			planJSON:   `{"goal":"测试","tasks":[{"id":"one","description":"任务","type":"UNKNOWN","dependencies":[]}]}`,
			wantErrSub: "invalid task type",
		},
		{
			name:       "unknown dependency",
			planJSON:   `{"goal":"测试","tasks":[{"id":"one","description":"任务","type":"ANALYSIS","dependencies":["missing"]}]}`,
			wantErrSub: "unknown dependency",
		},
		{
			name:       "self dependency",
			planJSON:   `{"goal":"测试","tasks":[{"id":"one","description":"任务","type":"ANALYSIS","dependencies":["one"]}]}`,
			wantErrSub: "depends on itself",
		},
		{
			name: "cyclic dependencies",
			planJSON: `{"goal":"测试","tasks":[
				{"id":"one","description":"任务一","type":"ANALYSIS","dependencies":["two"]},
				{"id":"two","description":"任务二","type":"ANALYSIS","dependencies":["one"]}
			]}`,
			wantErrSub: "cycle detected in plan",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parsePlan(tt.planJSON)
			if err == nil {
				t.Fatalf("parsePlan() error = nil, want an error containing %q", tt.wantErrSub)
			}
			if !strings.Contains(err.Error(), tt.wantErrSub) {
				t.Fatalf("parsePlan() error = %q, want it to contain %q", err, tt.wantErrSub)
			}
		})
	}
}
