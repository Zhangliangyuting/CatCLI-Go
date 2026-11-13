package multiagent

import (
	executionlog "AgentCLI/internal/execution"
	"AgentCLI/internal/tool"
	"strings"
	"testing"
)

func TestRecoveryPromptDistinguishesFreshReadFromUncertainMutation(t *testing.T) {
	registry := newApprovedTestRegistry()
	registry.RegisterProvider(tool.NewBuiltinProvider(nil))
	ref := &executionlog.TaskReference{Revision: 1, TaskID: "task", Attempt: 1, Phase: "worker"}
	calls := []executionlog.CallState{
		{CallID: "read", ToolName: "read_file", Task: ref, Status: executionlog.RetryAllowed},
		{CallID: "write", ToolName: "write_file", Task: ref, Status: executionlog.RetryAllowed},
	}
	prompt := taskRecoveryContext(calls, "task", 1, registry)
	if !strings.Contains(prompt, `"read" ("read_file"): 只读调用，可重新查询当前状态`) {
		t.Fatal("recovery prompt did not permit a fresh observation")
	}
	if !strings.Contains(prompt, `"write" ("write_file"): 重复操作的安全性未获证明`) {
		t.Fatal("recovery prompt treated an uncertain write as safe to repeat")
	}
	if strings.Contains(prompt, `"write" ("write_file"): 只读调用`) {
		t.Fatal("recovery prompt marked a mutation as read-only")
	}
}
