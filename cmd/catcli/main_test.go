package main

import (
	"AgentCLI/internal/routing"
	"context"
	"testing"
)

type fixedModeRouter struct {
	decision routing.Decision
}

func (r fixedModeRouter) Route(
	context.Context,
	string,
) (routing.Decision, error) {
	return r.decision, nil
}

type recordingAgent struct {
	called bool
	input  string
}

func (a *recordingAgent) Run(
	_ context.Context,
	input string,
) (string, error) {
	a.called = true
	a.input = input
	return "完成", nil
}

func TestRunRoutedInputSelectsPlanAgent(t *testing.T) {
	reactAgent := &recordingAgent{}
	planAgent := &recordingAgent{}
	router := fixedModeRouter{decision: routing.Decision{
		Mode:   routing.ModePlan,
		Input:  "复杂任务",
		Source: routing.SourceExplicit,
	}}

	result, err := runRoutedInput(
		context.Background(),
		router,
		reactAgent,
		planAgent,
		"/plan 复杂任务",
	)
	if err != nil {
		t.Fatalf("runRoutedInput() error = %v", err)
	}
	if result != "完成" || !planAgent.called || reactAgent.called {
		t.Fatalf(
			"result/react/plan = %q/%t/%t",
			result,
			reactAgent.called,
			planAgent.called,
		)
	}
	if planAgent.input != "复杂任务" {
		t.Fatalf("plan input = %q, want 复杂任务", planAgent.input)
	}
}
