package multiagent

import (
	"AgentCLI/internal/tool"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReviewingTaskExecutorApprovesResult(t *testing.T) {
	execution := TaskExecution{TaskID: "task_1", Prompt: "implement"}
	client, recording := newRecordedChatClient(t, `{"approved":true,"feedback":""}`)
	reviewer, err := NewReviewerSubAgent(client, tool.NewToolRegistry())
	if err != nil {
		t.Fatalf("NewReviewerSubAgent() error = %v", err)
	}
	executor := newTestWorkerExecutor(t, "implemented", reviewer)

	result, err := executor.Execute(context.Background(), execution)
	if err != nil {
		t.Fatalf("Execute() error = %v", err)
	}
	if result != "implemented" || recording.count() != 1 {
		t.Fatalf("Execute() result/review calls = %q/%d", result, recording.count())
	}
	messages := recording.lastMessages()
	input := messages[len(messages)-1].Content
	if !strings.Contains(input, execution.TaskID) || !strings.Contains(input, "implemented") {
		t.Fatalf("review input = %q", input)
	}
}

func TestReviewingTaskExecutorRejectsResult(t *testing.T) {
	client, _ := newRecordedChatClient(t, `{"approved":false,"feedback":"tests are missing"}`)
	reviewer, err := NewReviewerSubAgent(client, tool.NewToolRegistry())
	if err != nil {
		t.Fatalf("NewReviewerSubAgent() error = %v", err)
	}
	executor := newTestWorkerExecutor(t, "incomplete", reviewer)

	_, err = executor.Execute(context.Background(), TaskExecution{TaskID: "task_2"})
	var rejected *ResultRejectedError
	if !errors.As(err, &rejected) {
		t.Fatalf("Execute() error = %v, want ResultRejectedError", err)
	}
	if rejected.TaskID != "task_2" || rejected.Feedback != "tests are missing" {
		t.Fatalf("rejection = %#v", rejected)
	}
}

func TestReviewingTaskExecutorSkipsReviewAfterExecutionError(t *testing.T) {
	client, recording := newRecordedChatClient(t, `{"approved":true,"feedback":""}`)
	reviewer, err := NewReviewerSubAgent(client, tool.NewToolRegistry())
	if err != nil {
		t.Fatalf("NewReviewerSubAgent() error = %v", err)
	}
	executor := newTestWorkerExecutor(t, "unused", reviewer)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = executor.Execute(ctx, TaskExecution{TaskID: "task_3"})
	if !errors.Is(err, context.Canceled) || recording.count() != 0 {
		t.Fatalf("Execute() error/review calls = %v/%d", err, recording.count())
	}
}
