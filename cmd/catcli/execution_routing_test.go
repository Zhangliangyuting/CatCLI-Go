package main

import (
	"AgentCLI/internal/execution"
	"context"
	"testing"
)

func TestResumeRoutesToOriginalExecutionKind(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  execution.RunState
		want string
	}{
		{"legacy react", execution.RunState{}, "react"},
		{"react", execution.RunState{Kind: execution.ReActRun}, "react"},
		{"planning draft", execution.RunState{Kind: execution.PlanAndExecuteRun}, "plan"},
		{"confirmed legacy plan", execution.RunState{Plan: &execution.PlanEventData{}}, "plan"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			react := func(context.Context, execution.RunState) (string, error) { return "react", nil }
			plan := func(context.Context, execution.RunState) (string, error) { return "plan", nil }
			result, err := resumeExecution(t.Context(), tc.run, react, plan)
			if err != nil || result != tc.want {
				t.Fatalf("result=%s err=%v", result, err)
			}
		})
	}
}
