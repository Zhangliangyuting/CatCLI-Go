package routing

import (
	"AgentCLI/internal/llm"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestHybridModeRouterUsesExplicitMode(t *testing.T) {
	router := NewHybridModeRouter(nil)

	decision, err := router.Route(
		context.Background(),
		"/plan 创建一个 Go Web 项目",
	)
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if decision.Mode != ModePlan || decision.Source != SourceExplicit {
		t.Fatalf("decision = %#v, want explicit PLAN", decision)
	}
	if decision.Input != "创建一个 Go Web 项目" {
		t.Fatalf("decision.Input = %q", decision.Input)
	}
}

func TestHybridModeRouterUsesStrongRules(t *testing.T) {
	router := NewHybridModeRouter(nil)

	tests := []struct {
		name  string
		input string
		mode  ExecutionMode
	}{
		{
			name:  "complex refactor",
			input: "重构整个项目并补充测试",
			mode:  ModePlan,
		},
		{
			name:  "simple explanation",
			input: "这个 channel 是什么意思",
			mode:  ModeReact,
		},
		{
			name:  "numbered steps",
			input: "1. 修改配置\n2. 运行测试",
			mode:  ModePlan,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			decision, err := router.Route(context.Background(), tt.input)
			if err != nil {
				t.Fatalf("Route() error = %v", err)
			}
			if decision.Mode != tt.mode || decision.Source != SourceRule {
				t.Fatalf("decision = %#v, want rule %s", decision, tt.mode)
			}
		})
	}
}

func TestHybridModeRouterFallsBackToLLM(t *testing.T) {
	client, err := llm.NewOpenAICompatibleClient(
		"test-api-key",
		"http://example.test",
		"test-model",
	)
	if err != nil {
		t.Fatalf("NewOpenAICompatibleClient() error = %v", err)
	}
	client.HTTPClient = &http.Client{Transport: roundTripFunc(
		func(_ *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"choices":[{"message":{"role":"assistant","content":"PLAN"}}]}`,
				)),
				Header: make(http.Header),
			}, nil
		},
	)}

	decision, err := NewHybridModeRouter(client).Route(
		context.Background(),
		"帮我完善这个功能",
	)
	if err != nil {
		t.Fatalf("Route() error = %v", err)
	}
	if decision.Mode != ModePlan || decision.Source != SourceLLM {
		t.Fatalf("decision = %#v, want LLM PLAN", decision)
	}
}

func TestParseModeFallsBackForUnexpectedResponse(t *testing.T) {
	if _, ok := parseMode("我认为应该规划一下"); ok {
		t.Fatal("parseMode() accepted an unexpected response")
	}
}
