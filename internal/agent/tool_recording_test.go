package agent

import (
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"context"
	"errors"
	"strings"
	"testing"
)

func TestReActDirectToolExecutionRecording(t *testing.T) {
	for _, mode := range []string{"success", "tool error", "invalid arguments", "start failure", "result failure", "panic"} {
		t.Run(mode, func(t *testing.T) {
			store, err := execution.NewJSONLEventStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			var sink execution.EventStore = store
			if mode == "start failure" {
				sink = failingExecutionStore{store, execution.ToolCallStarted}
			}
			if mode == "result failure" {
				sink = failingExecutionStore{store, execution.ToolCallCompleted}
			}
			args := `{"path":"main.go"}`
			if mode == "invalid arguments" {
				args = `{`
			}
			calls := 0
			a, requests := newLoggedTestAgent(t, sink, func(_ context.Context, args map[string]any) (string, error) {
				calls++
				if args["path"] != "main.go" {
					t.Fatalf("wrong tool arguments: %v", args)
				}
				if mode == "panic" {
					panic("tool panic")
				}
				if mode == "tool error" {
					return "", errors.New("tool unavailable")
				}
				return "written", nil
			}, []llm.ToolCall{testExecutionCall("model-call", args)})
			var answer string
			var runErr error
			var caught any
			func() {
				defer func() { caught = recover() }()
				answer, runErr = a.Run(t.Context(), "write file")
			}()
			wantCalls := 1
			if mode == "invalid arguments" || mode == "start failure" {
				wantCalls = 0
			}
			if calls != wantCalls {
				t.Fatalf("tool executions=%d, want %d", calls, wantCalls)
			}
			if mode == "panic" {
				if caught != "tool panic" {
					t.Fatalf("panic changed: %v", caught)
				}
			} else if caught != nil {
				t.Fatalf("unexpected panic: %v", caught)
			}
			events, err := store.Load(t.Context(), "conversation", 0)
			if err != nil {
				t.Fatal(err)
			}
			runs, err := execution.Replay(events)
			if err != nil || len(runs) != 1 {
				t.Fatalf("runs=%+v error=%v", runs, err)
			}
			run := runs[0]
			switch mode {
			case "start failure", "result failure", "panic":
				if run.Status != execution.Interrupted || *requests != 1 {
					t.Fatalf("ambiguous execution continued: %+v requests=%d", run, *requests)
				}
				if mode != "panic" && runErr == nil {
					t.Fatal("lost recording error")
				}
				if mode == "start failure" {
					if len(run.Calls) != 0 {
						t.Fatal("recorded a failed start")
					}
				} else if len(run.Calls) != 1 || run.Calls[0].Status != execution.Unknown {
					t.Fatalf("missing unknown call: %+v", run)
				}
			default:
				if runErr != nil || answer != "done" || run.Status != execution.Completed || *requests != 2 {
					t.Fatalf("answer=%q error=%v run=%+v requests=%d", answer, runErr, run, *requests)
				}
				if len(run.Calls) != 1 || run.Calls[0].Status != execution.Returned {
					t.Fatalf("missing returned call: %+v", run)
				}
				call := run.Calls[0]
				if mode == "success" {
					if call.Result != "written" || call.Error != "" {
						t.Fatal(call)
					}
				} else if call.Error == "" || !strings.HasPrefix(call.Result, "ERROR: ") {
					t.Fatalf("tool error not recorded as a returned result: %+v", call)
				}
			}
		})
	}
}
