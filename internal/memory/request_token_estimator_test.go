package memory

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"testing"
)

func TestRequestTokenEstimatorIncludesCompleteLogicalRequest(t *testing.T) {
	estimator := NewCalibratedRequestTokenEstimator(TokenCounterFunc(func(content string) int {
		return len(content)
	}))
	messages := []llm.Message{
		llm.SystemMessage("system instructions"),
		llm.UserMessage("hello"),
	}
	withoutTools, err := estimator.Estimate(messages, nil)
	if err != nil {
		t.Fatalf("Estimate(without tools) error = %v", err)
	}
	withTools, err := estimator.Estimate(messages, []tool.Definition{{
		Type: "function",
		FunctionDefinition: tool.FunctionDefinition{
			Name: "read_file", Description: "Read a UTF-8 file",
			Parameters: map[string]interface{}{"type": "object"},
		},
	}})
	if err != nil {
		t.Fatalf("Estimate(with tools) error = %v", err)
	}
	if withoutTools <= len("system instructions")+len("hello") {
		t.Fatalf("estimate = %d, want message protocol overhead included", withoutTools)
	}
	if withTools <= withoutTools {
		t.Fatalf("with tools = %d, without tools = %d; tool definition was not counted", withTools, withoutTools)
	}
}

func TestRequestTokenEstimatorCalibratesTowardActualPromptUsage(t *testing.T) {
	estimator := NewCalibratedRequestTokenEstimator(TokenCounterFunc(func(string) int { return 1000 }))
	before, err := estimator.Estimate([]llm.Message{llm.UserMessage("hello")}, nil)
	if err != nil {
		t.Fatalf("Estimate() error = %v", err)
	}
	estimator.Observe(before, before*2)
	after, err := estimator.Estimate([]llm.Message{llm.UserMessage("hello")}, nil)
	if err != nil {
		t.Fatalf("Estimate() after observation error = %v", err)
	}
	if after <= before {
		t.Fatalf("estimate after calibration = %d, want greater than %d", after, before)
	}
}
