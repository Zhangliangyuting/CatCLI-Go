package agent

import (
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"context"
	"errors"
	"strings"
	"testing"
)

func interruptedTestRun(t *testing.T, store execution.EventStore) execution.RunState {
	t.Helper()
	for _, e := range []execution.Event{
		{EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: "original", Type: execution.RunStarted}, Run: &execution.RunEventData{Input: "edit main.go"}},
		{EventHeader: execution.EventHeader{ConversationID: "conversation", RunID: "original", Type: execution.ToolCallStarted}, ToolCall: &execution.ToolCallEventData{CallID: "execution-call", ToolCallID: "old-call", ToolName: "write", Arguments: `{"path":"main.go"}`}},
	} {
		if _, err := store.Append(t.Context(), e); err != nil {
			t.Fatal(err)
		}
	}
	events, _ := store.Load(t.Context(), "conversation", 0)
	runs, err := execution.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	return runs[0]
}

func TestResumeLinksNewRunAndPreservesConversation(t *testing.T) {
	store, _ := execution.NewJSONLEventStore(t.TempDir())
	source := interruptedTestRun(t, store)
	a, requests := newLoggedTestAgent(t, store, func(context.Context, map[string]any) (string, error) {
		t.Fatal("unexpected automatic tool replay")
		return "", nil
	}, nil)
	a.memoryManager.AddMessage(llm.UserMessage("older conversation"), "")
	a.memoryManager.AddMessage(llm.Message{Role: "assistant", ToolCalls: []llm.ToolCall{testExecutionCall("old-call", `{}`)}}, "")
	answer, err := a.Resume(t.Context(), source)
	if err != nil || answer != "done" || *requests != 1 {
		t.Fatalf("resume: %q %v requests=%d", answer, err, *requests)
	}
	events, _ := store.Load(t.Context(), "conversation", 0)
	runs, err := execution.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 2 || runs[0].Status != execution.Resumed || runs[1].Status != execution.Completed || runs[1].Input != source.Input {
		t.Fatalf("unexpected states: %+v", runs)
	}
	if events[2].Run.ResumedFrom != source.RunID {
		t.Fatal("missing durable link")
	}
	messages, err := a.contextMessages()
	if err != nil {
		t.Fatal(err)
	}
	var text strings.Builder
	for _, m := range messages {
		text.WriteString(m.Content)
	}
	if !strings.Contains(text.String(), "older conversation") || !strings.Contains(text.String(), "结果不确定") || !strings.Contains(text.String(), "done") {
		t.Fatalf("lost root history or recovery outcome: %s", text.String())
	}
	if _, err := a.Resume(t.Context(), source); err == nil {
		t.Fatal("allowed duplicate resume")
	}
}

func TestResumeFailedStartLeavesOriginalInterrupted(t *testing.T) {
	store, _ := execution.NewJSONLEventStore(t.TempDir())
	source := interruptedTestRun(t, store)
	a, requests := newLoggedTestAgent(t, failingExecutionStore{store, execution.RunStarted}, func(context.Context, map[string]any) (string, error) { t.Fatal("unexpected dispatch"); return "", nil }, nil)
	if _, err := a.Resume(t.Context(), source); err == nil {
		t.Fatal("expected start persistence failure")
	}
	if *requests != 0 {
		t.Fatal("called model before recording continuation")
	}
	events, _ := store.Load(t.Context(), "conversation", 0)
	runs, err := execution.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	if len(runs) != 1 || runs[0].Status != execution.Interrupted {
		t.Fatalf("lost recoverable source: %+v", runs)
	}
}

type resumeNoLoadStore struct{ execution.EventStore }

func (s resumeNoLoadStore) Load(context.Context, string, uint64) ([]execution.Event, error) {
	return nil, errors.New("Resume must not reload execution history")
}

func TestResumeUsesSuppliedStateWithoutLoadingHistory(t *testing.T) {
	store, err := execution.NewJSONLEventStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	source := interruptedTestRun(t, store)
	a, requests := newLoggedTestAgent(t, resumeNoLoadStore{store}, nil, nil)
	answer, err := a.Resume(t.Context(), source)
	if err != nil || answer != "done" || *requests != 1 {
		t.Fatalf("answer=%q error=%v requests=%d", answer, err, *requests)
	}
	if _, err := a.Resume(t.Context(), source); err == nil || *requests != 1 {
		t.Fatalf("duplicate resume dispatched: error=%v requests=%d", err, *requests)
	}
}

func TestResumeRejectsInvalidSuppliedStateBeforeModelCall(t *testing.T) {
	for _, mode := range []string{"completed", "wrong conversation", "wrong kind"} {
		t.Run(mode, func(t *testing.T) {
			store, err := execution.NewJSONLEventStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			source := interruptedTestRun(t, store)
			switch mode {
			case "completed":
				source.Status = execution.Completed
			case "wrong conversation":
				source.ConversationID = "other"
			case "wrong kind":
				source.Kind = execution.PlanAndExecuteRun
			}
			a, requests := newLoggedTestAgent(t, resumeNoLoadStore{store}, nil, nil)
			if _, err := a.Resume(t.Context(), source); err == nil || *requests != 0 {
				t.Fatalf("invalid state dispatched: error=%v requests=%d", err, *requests)
			}
		})
	}
}

func TestRecoveryPromptExplainsUncertaintyAndTruncation(t *testing.T) {
	prompt, err := recoveryPrompt(execution.RunState{RunID: "run", Input: "fix it", Calls: []execution.CallState{{ToolName: "write", Status: execution.Unknown, Result: strings.Repeat("界", 30000)}}})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(prompt, "结果不确定") || !strings.Contains(prompt, "内容已截断") || !strings.Contains(prompt, "fix it") || len([]rune(prompt)) > 30000 {
		t.Fatal("missing safe bounded recovery context")
	}
}
