package main

import (
	"AgentCLI/internal/agent/multiagent"
	"AgentCLI/internal/execution"
	"AgentCLI/internal/plan"
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"syscall"
	"testing"
	"time"
)

const crashConversation = "crash-conversation"

// This test is a subprocess entry point, not an external model/MCP integration.
// The parent kills the process at an acknowledged boundary; no defer runs.
func TestExecutionCrashChild(t *testing.T) {
	if os.Getenv("CATCLI_CRASH_CHILD") != "1" {
		return
	}
	root, stage := os.Getenv("CATCLI_CRASH_ROOT"), os.Getenv("CATCLI_CRASH_STAGE")
	store, err := execution.NewJSONLEventStore(filepath.Join(root, "execution"))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// Establish an actual automatic checkpoint at sequence 50. All later
	// operations must be recovered from its byte-indexed suffix after SIGKILL.
	for i := range 50 {
		_, err := store.Append(ctx, execution.Event{
			EventHeader: execution.EventHeader{ConversationID: crashConversation, RunID: fmt.Sprintf("seed-%d", i), Type: execution.RunStarted},
			Run:         &execution.RunEventData{Kind: execution.ReActRun, Input: "checkpoint seed"},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	recorder := execution.NewRecorder(store, crashConversation)
	run, err := recorder.StartRun(ctx, "create effect.txt", "", execution.PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.SavePlan(ctx, execution.PlanEventData{
		PlanID: "crash-plan", Revision: 1, Goal: "create effect.txt",
		Tasks: []plan.TaskDefinition{{ID: "write", Type: plan.FILE_WRITE}},
	}); err != nil {
		t.Fatal(err)
	}
	task := execution.TaskEventData{Revision: 1, TaskID: "write", Attempt: 1}
	if err := run.RecordTask(ctx, execution.TaskStarted, task); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "before_tool_start")
	args, _ := json.Marshal(map[string]string{"path": filepath.Join(root, "effect.txt"), "content": "one external effect\n"})
	call, err := run.StartToolCall(ctx, execution.ToolCallEventData{
		ToolCallID: "model-write", ToolName: "write_file", Arguments: string(args),
		Task: &execution.TaskReference{Revision: 1, TaskID: "write", Attempt: 1, Phase: "worker"},
	})
	if err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_tool_start")
	// A real filesystem side effect, distinct from the execution log. Make it
	// durable before pausing in the result-not-yet-recorded window.
	effect, err := os.OpenFile(filepath.Join(root, "effect.txt"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := effect.WriteString("one external effect\n"); err != nil {
		t.Fatal(err)
	}
	if err := effect.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := effect.Close(); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_side_effect")
	if err := run.CompleteToolCall(ctx, call.CallID, "written", nil); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_tool_result")
	task.Result = "worker result"
	if err := run.RecordTask(ctx, execution.TaskWorkCompleted, task); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_worker_result")
	if err := run.RecordTask(ctx, execution.TaskCompleted, task); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_task_completed")
	if err := run.FinishRun(ctx, "done", nil); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "after_run_completed")
	if stage == "checkpoint_temp_created" || stage == "checkpoint_temp_synced" {
		// Observe existing Context checks in the real save path. No production
		// fault-injection hook, alternate writer or copied save algorithm is used.
		ctx = checkpointCrashContext{Context: ctx, root: filepath.Join(root, "execution"), stage: stage}
	}
	if err := store.SaveCheckpoint(ctx, crashConversation); err != nil {
		t.Fatal(err)
	}
	crashAt(stage, "checkpoint_published")
	t.Fatalf("did not reach requested crash boundary %q", stage)
}

func crashAt(requested, reached string) {
	if requested != reached {
		return
	}
	fmt.Fprintln(os.Stdout, "CRASH_READY")
	var release [1]byte
	// Parent keeps stdin open and sends SIGKILL instead of releasing us.
	if _, err := io.ReadFull(os.Stdin, release[:]); err != nil {
		os.Exit(3)
	}
}

type checkpointCrashContext struct {
	context.Context
	root, stage string
}

func (c checkpointCrashContext) Err() error {
	if err := c.Context.Err(); err != nil {
		return err
	}
	files, _ := filepath.Glob(filepath.Join(c.root, ".execution-checkpoint-*"))
	for _, name := range files {
		info, err := os.Stat(name)
		if err != nil {
			continue
		}
		if c.stage == "checkpoint_temp_created" && info.Size() == 0 {
			crashAt(c.stage, "checkpoint_temp_created")
		}
		// In the production save function the next Context check after Write
		// occurs only after the temporary file has been Sync'ed and closed.
		if c.stage == "checkpoint_temp_synced" && info.Size() > 0 {
			crashAt(c.stage, "checkpoint_temp_synced")
		}
	}
	return nil
}

func killExecutionChild(t *testing.T, root, stage string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestExecutionCrashChild$", "-test.count=1")
	cmd.Env = append(os.Environ(), "CATCLI_CRASH_CHILD=1", "CATCLI_CRASH_ROOT="+root, "CATCLI_CRASH_STAGE="+stage)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan error, 1)
	go func() {
		scanner := bufio.NewScanner(stdout)
		for scanner.Scan() {
			if scanner.Text() == "CRASH_READY" {
				ready <- nil
				return
			}
		}
		if err := scanner.Err(); err != nil {
			ready <- err
		} else {
			ready <- errors.New("child exited without acknowledging crash boundary")
		}
	}()
	select {
	case err := <-ready:
		if err != nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
			t.Fatalf("stage %s: %v; stderr: %s", stage, err, stderr.String())
		}
	case <-ctx.Done():
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		t.Fatalf("stage %s timed out; stderr: %s", stage, stderr.String())
	}
	if err := cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	if err := cmd.Wait(); err == nil {
		t.Fatal("child exited normally instead of being killed")
	}
	status, ok := cmd.ProcessState.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGKILL {
		t.Fatalf("child was not killed by SIGKILL: %v; stderr: %s", cmd.ProcessState, stderr.String())
	}
}

func TestExecutionRecoveryAfterSIGKILL(t *testing.T) {
	cases := []struct {
		stage     string
		sequence  uint64
		run       execution.RunStatus
		task      execution.TaskStatus
		call      execution.CallStatus
		hasEffect bool
	}{
		{"before_tool_start", 53, execution.Interrupted, execution.TaskInterrupted, "", false},
		{"after_tool_start", 54, execution.Interrupted, execution.TaskInterrupted, execution.Unknown, false},
		{"after_side_effect", 54, execution.Interrupted, execution.TaskInterrupted, execution.Unknown, true},
		{"after_tool_result", 55, execution.Interrupted, execution.TaskInterrupted, execution.Returned, true},
		{"after_worker_result", 56, execution.Interrupted, execution.TaskAwaitingReview, execution.Returned, true},
		{"after_task_completed", 57, execution.Interrupted, execution.TaskDone, execution.Returned, true},
		{"after_run_completed", 58, execution.Completed, execution.TaskDone, execution.Returned, true},
	}
	for _, tc := range cases {
		t.Run(tc.stage, func(t *testing.T) {
			root := t.TempDir()
			killExecutionChild(t, root, tc.stage)
			store, runs, err := loadExecutionLog(root, crashConversation)
			if err != nil || len(runs) != 51 {
				t.Fatalf("startup recovery: runs=%d error=%v", len(runs), err)
			}
			latest := runs[len(runs)-1]
			if latest.LastSequence != tc.sequence || latest.Status != tc.run || len(latest.Tasks) != 1 || latest.Tasks[0].Status != tc.task || latest.Tasks[0].Attempt != 1 {
				t.Fatalf("wrong recovered state: %+v", latest)
			}
			if tc.call == "" {
				if len(latest.Calls) != 0 {
					t.Fatal("invented a tool start")
				}
			} else if len(latest.Calls) != 1 || latest.Calls[0].Status != tc.call {
				t.Fatalf("wrong recovered calls: %+v", latest.Calls)
			}
			if tc.call == execution.Returned && latest.Calls[0].Result != "written" {
				t.Fatal("lost durable tool result")
			}
			if tc.task == execution.TaskAwaitingReview && latest.Tasks[0].Result != "worker result" {
				t.Fatal("lost saved Worker output")
			}
			assertCrashEffect(t, root, tc.hasEffect)
			assertFullCrashReplay(t, store, runs)
			if tc.call == execution.Unknown {
				before, _ := store.Load(t.Context(), crashConversation, 0)
				agent := &multiagent.PlanAndExecuteAgent{}
				multiagent.WithRecorder(execution.NewRecorder(store, crashConversation))(agent)
				if _, err := agent.Resume(t.Context(), latest); !errors.Is(err, multiagent.ErrRecoveryNeedsVerification) {
					t.Fatalf("ambiguous side effect was not gated: %v", err)
				}
				after, _ := store.Load(t.Context(), crashConversation, 0)
				if !reflect.DeepEqual(before, after) {
					t.Fatal("blocked recovery consumed or rewrote the original Run")
				}
				assertCrashEffect(t, root, tc.hasEffect)
			}
			// A fresh process/store can append immediately after recovery; sequence
			// and committed byte position must survive the killed writer.
			if _, err := store.Append(t.Context(), execution.Event{
				EventHeader: execution.EventHeader{ConversationID: crashConversation, RunID: "next", Type: execution.RunStarted},
				Run:         &execution.RunEventData{Kind: execution.ReActRun},
			}); err != nil {
				t.Fatal(err)
			}
			_, again, err := loadExecutionLog(root, crashConversation)
			if err != nil || again[len(again)-1].LastSequence != tc.sequence+1 {
				t.Fatalf("append after crash: %+v %v", again, err)
			}
		})
	}
}

func TestCheckpointRecoveryAfterSIGKILL(t *testing.T) {
	for _, stage := range []string{"checkpoint_temp_created", "checkpoint_temp_synced", "checkpoint_published"} {
		t.Run(stage, func(t *testing.T) {
			root := t.TempDir()
			killExecutionChild(t, root, stage)
			path := filepath.Join(root, "execution", crashConversation+".jsonl.checkpoint.json")
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			var checkpoint execution.Checkpoint
			if err := json.Unmarshal(data, &checkpoint); err != nil {
				t.Fatalf("published checkpoint is partial: %v", err)
			}
			want := uint64(50)
			if stage == "checkpoint_published" {
				want = 58
			}
			if checkpoint.LastSequence != want {
				t.Fatalf("published checkpoint sequence=%d, want %d", checkpoint.LastSequence, want)
			}
			temps, err := filepath.Glob(filepath.Join(root, "execution", ".execution-checkpoint-*"))
			if err != nil {
				t.Fatal(err)
			}
			if stage == "checkpoint_published" {
				if len(temps) != 0 {
					t.Fatal("successful publication left a temporary file")
				}
			} else {
				if len(temps) != 1 {
					t.Fatalf("expected abandoned temporary snapshot, got %v", temps)
				}
				info, _ := os.Stat(temps[0])
				if stage == "checkpoint_temp_created" && info.Size() != 0 {
					t.Fatal("did not stop before temporary snapshot write")
				}
				if stage == "checkpoint_temp_synced" {
					data, err := os.ReadFile(temps[0])
					var temporary execution.Checkpoint
					if err != nil || json.Unmarshal(data, &temporary) != nil || temporary.LastSequence != 58 {
						t.Fatal("did not stop after temporary snapshot was synced")
					}
				}
			}
			store, runs, err := loadExecutionLog(root, crashConversation)
			if err != nil || len(runs) != 51 || runs[len(runs)-1].Status != execution.Completed {
				t.Fatalf("snapshot crash recovery: %+v %v", runs, err)
			}
			assertFullCrashReplay(t, store, runs)
			assertCrashEffect(t, root, true)
			// Re-saving after a crash must tolerate an orphaned temporary file.
			if err := store.SaveCheckpoint(t.Context(), crashConversation); err != nil {
				t.Fatal(err)
			}
			_, again, err := loadExecutionLog(root, crashConversation)
			if err != nil || !reflect.DeepEqual(runs, again) {
				t.Fatalf("recovery changed after replacing snapshot: %v", err)
			}
		})
	}
}

func assertCrashEffect(t *testing.T, root string, exists bool) {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "effect.txt"))
	if !exists {
		if !os.IsNotExist(err) {
			t.Fatalf("side effect unexpectedly exists: %q %v", data, err)
		}
		return
	}
	if err != nil || string(data) != "one external effect\n" {
		t.Fatalf("side effect missing or repeated: %q %v", data, err)
	}
}

func assertFullCrashReplay(t *testing.T, store *execution.JSONLEventStore, recovered []execution.RunState) {
	t.Helper()
	events, err := store.Load(t.Context(), crashConversation, 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := execution.Replay(events)
	if err != nil || !reflect.DeepEqual(recovered, want) {
		t.Fatalf("checkpoint recovery differs from full replay: %v", err)
	}
}
