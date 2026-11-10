package execution

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

func checkpointPath(s *JSONLEventStore) string {
	return filepath.Join(s.root, "conversation.jsonl.checkpoint.json")
}

func startedRuns(t *testing.T, s *JSONLEventStore, from, through int) {
	t.Helper()
	for i := from; i <= through; i++ {
		appendEvent(t, s, Event{EventHeader: EventHeader{RunID: fmt.Sprintf("run-%d", i), Type: RunStarted}, Run: &RunEventData{Kind: ReActRun, Input: "task"}})
	}
}

func readTestCheckpoint(t *testing.T, s *JSONLEventStore) Checkpoint {
	t.Helper()
	data, err := os.ReadFile(checkpointPath(s))
	if err != nil {
		t.Fatal(err)
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		t.Fatal(err)
	}
	return checkpoint
}

func compareFullReplay(t *testing.T, s *JSONLEventStore) []RunState {
	t.Helper()
	events, err := s.Load(t.Context(), "conversation", 0)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.LoadRuns(t.Context(), "conversation")
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("checkpoint replay differs: error=%v\ngot=%+v\nwant=%+v", err, got, want)
	}
	return got
}

func TestCheckpointAutomaticIntervalAndIndexedSuffix(t *testing.T) {
	s := newTestStore(t)
	startedRuns(t, s, 1, 49)
	if _, err := os.Stat(checkpointPath(s)); !os.IsNotExist(err) {
		t.Fatalf("checkpoint before threshold: %v", err)
	}
	startedRuns(t, s, 50, 50)
	checkpoint := readTestCheckpoint(t, s)
	info, _ := os.Stat(filepath.Join(s.root, "conversation.jsonl"))
	if checkpoint.LastSequence != 50 || checkpoint.ByteOffset != info.Size() || len(checkpoint.Runs) != 50 {
		t.Fatalf("wrong committed prefix: %+v", checkpoint)
	}
	startedRuns(t, s, 51, 55)
	reopened, _ := NewJSONLEventStore(s.root)
	f, err := os.Open(filepath.Join(s.root, "conversation.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	base, suffix, _, err := reopened.readLogLocked(t.Context(), f, "conversation")
	if err != nil || base.LastSequence != 50 || len(suffix) != 5 || suffix[0].Sequence != 51 {
		t.Fatalf("did not use byte-indexed suffix: base=%d suffix=%v error=%v", base.LastSequence, suffix, err)
	}
	for _, after := range []uint64{0, 49, 50, 52, 55, 100} {
		events, err := reopened.Load(t.Context(), "conversation", after)
		want := 0
		if after < 55 {
			want = 55 - int(after)
		}
		if err != nil || len(events) != want || (len(events) > 0 && events[0].Sequence != after+1) {
			t.Fatalf("cursor %d: events=%d error=%v", after, len(events), err)
		}
	}
	compareFullReplay(t, reopened)
	startedRuns(t, reopened, 56, 100)
	if c := readTestCheckpoint(t, reopened); c.LastSequence != 100 {
		t.Fatalf("second interval did not advance: %d", c.LastSequence)
	}
	compareFullReplay(t, reopened)
}

func TestReadEventsFromSeeksPastPrefix(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "suffix")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	prefix := strings.Repeat("not JSON; reader must seek past this prefix\n", 1000)
	e := workflowLog(runStart())[0]
	e.Sequence = 51
	encoded, _ := json.Marshal(e)
	if _, err := f.Write(append([]byte(prefix), append(encoded, '\n')...)); err != nil {
		t.Fatal(err)
	}
	events, offset, err := readEventsFrom(t.Context(), f, "conversation", 50, int64(len(prefix)))
	if err != nil || len(events) != 1 || events[0].Sequence != 51 || offset != int64(len(prefix)+len(encoded)+1) {
		t.Fatalf("positioned read: events=%v offset=%d error=%v", events, offset, err)
	}
}

func TestCheckpointRejectsInvalidSnapshotsAndFallsBack(t *testing.T) {
	for _, mode := range []string{"missing", "partial", "checksum", "offset", "boundary", "conversation", "sequence", "ancestry"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			startedRuns(t, s, 1, 51)
			checkpoint := readTestCheckpoint(t, s)
			path := checkpointPath(s)
			switch mode {
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
			case "partial":
				if err := os.WriteFile(path, []byte(`{"last_sequence":`), 0600); err != nil {
					t.Fatal(err)
				}
			default:
				switch mode {
				case "checksum":
					checkpoint.Runs[0].Input = "damaged"
				case "offset":
					checkpoint.ByteOffset--
				case "boundary":
					checkpoint.BoundaryHash = "wrong"
				case "conversation":
					checkpoint.ConversationID = "other"
				case "sequence":
					checkpoint.LastSequence++
				case "ancestry":
					checkpoint.Runs[0].ResumedFrom = "missing"
				}
				if mode != "checksum" {
					checkpoint.Checksum, _ = checkpoint.digest()
				}
				data, _ := json.Marshal(checkpoint)
				if err := os.WriteFile(path, data, 0600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, _ := NewJSONLEventStore(s.root)
			compareFullReplay(t, reopened)
			startedRuns(t, reopened, 52, 52)
			if reopened.positions["conversation"].lastSequence != 52 {
				t.Fatal("fallback lost append sequence")
			}
		})
	}
}

func TestCheckpointRejectsReplacedOrTruncatedLog(t *testing.T) {
	for _, mode := range []string{"replace", "truncate", "boundary edit"} {
		t.Run(mode, func(t *testing.T) {
			s := newTestStore(t)
			startedRuns(t, s, 1, 51)
			path := filepath.Join(s.root, "conversation.jsonl")
			data, _ := os.ReadFile(path)
			lines := bytes.SplitAfter(data, []byte{'\n'})
			switch mode {
			case "replace":
				data = bytes.ReplaceAll(data, []byte(`"input":"task"`), []byte(`"input":"new!"`))
				replacement := path + ".replacement"
				if err := os.WriteFile(replacement, data, 0600); err != nil {
					t.Fatal(err)
				}
				if err := os.Rename(replacement, path); err != nil {
					t.Fatal(err)
				}
			case "truncate":
				if err := os.Truncate(path, int64(len(lines[0]))); err != nil {
					t.Fatal(err)
				}
			case "boundary edit":
				lines[49] = bytes.ReplaceAll(lines[49], []byte(`"input":"task"`), []byte(`"input":"new!"`))
				if err := os.WriteFile(path, bytes.Join(lines, nil), 0600); err != nil {
					t.Fatal(err)
				}
			}
			reopened, _ := NewJSONLEventStore(s.root)
			compareFullReplay(t, reopened)
		})
	}
}

func TestCheckpointUnterminatedTailAndCommittedSuffixCorruption(t *testing.T) {
	for _, tail := range []string{`{"partial":`, "{bad}\n"} {
		t.Run(tail, func(t *testing.T) {
			s := newTestStore(t)
			startedRuns(t, s, 1, 51)
			path := filepath.Join(s.root, "conversation.jsonl")
			f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0600)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := f.WriteString(tail); err != nil {
				t.Fatal(err)
			}
			f.Close()
			reopened, _ := NewJSONLEventStore(s.root)
			if strings.HasSuffix(tail, "\n") {
				if _, err := reopened.LoadRuns(t.Context(), "conversation"); err == nil {
					t.Fatal("checkpoint concealed committed suffix corruption")
				}
				if _, err := reopened.Append(t.Context(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: "extra", Type: RunStarted}, Run: &RunEventData{}}); err == nil {
					t.Fatal("append concealed committed corruption")
				}
				return
			}
			compareFullReplay(t, reopened)
			startedRuns(t, reopened, 52, 52)
			compareFullReplay(t, reopened)
		})
	}
}

func TestCheckpointFailureDoesNotFailCommittedAppend(t *testing.T) {
	s := newTestStore(t)
	s.checkpointInterval = 2
	startedRuns(t, s, 1, 2)
	previous, _ := os.ReadFile(checkpointPath(s))
	if err := os.Remove(checkpointPath(s)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(checkpointPath(s), 0700); err != nil {
		t.Fatal(err)
	}
	startedRuns(t, s, 3, 4)
	if err := s.SaveCheckpoint(t.Context(), "conversation"); err == nil {
		t.Fatal("expected snapshot publication failure")
	}
	entries, _ := filepath.Glob(filepath.Join(s.root, ".execution-checkpoint-*"))
	if len(entries) != 0 {
		t.Fatalf("temporary snapshots leaked: %v", entries)
	}
	compareFullReplay(t, s)
	if err := os.Remove(checkpointPath(s)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(checkpointPath(s), previous, 0600); err != nil {
		t.Fatal(err)
	}
	compareFullReplay(t, s)
	if err := s.SaveCheckpoint(t.Context(), "conversation"); err != nil {
		t.Fatalf("checkpoint did not recover: %v", err)
	}
}

func TestCheckpointConfigCancellationAndPermissions(t *testing.T) {
	s := newTestStore(t)
	s.checkpointInterval = 0
	startedRuns(t, s, 1, 51)
	if _, err := os.Stat(checkpointPath(s)); !os.IsNotExist(err) {
		t.Fatal("disabled automatic snapshots were written")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.SaveCheckpoint(ctx, "conversation"); err == nil {
		t.Fatal("saved with cancelled context")
	}
	if _, err := s.LoadRuns(ctx, "conversation"); err == nil {
		t.Fatal("loaded with cancelled context")
	}
	if _, err := s.LoadRuns(t.Context(), "../escape"); err == nil {
		t.Fatal("accepted invalid ID")
	}
	if err := s.SaveCheckpoint(t.Context(), "../escape"); err == nil {
		t.Fatal("saved invalid ID")
	}
	if err := s.SaveCheckpoint(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	info, _ := os.Stat(checkpointPath(s))
	if info.Mode().Perm() != 0600 {
		t.Fatal("checkpoint permissions are not private")
	}
	compareFullReplay(t, s)
}

func TestCheckpointConcurrentAppendsAndSnapshots(t *testing.T) {
	s := newTestStore(t)
	s.checkpointInterval = 7
	var wg sync.WaitGroup
	for i := range 60 {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := s.Append(t.Context(), Event{EventHeader: EventHeader{ConversationID: "conversation", RunID: fmt.Sprintf("run-%d", i), Type: RunStarted}, Run: &RunEventData{}})
			if err != nil {
				t.Error(err)
			}
			if i%10 == 0 {
				if err := s.SaveCheckpoint(t.Context(), "conversation"); err != nil {
					t.Error(err)
				}
			}
		}(i)
	}
	wg.Wait()
	if err := readTestCheckpoint(t, s).validateState(); err != nil {
		t.Fatal(err)
	}
	compareFullReplay(t, s)
}

func TestCheckpointMidCallAndLargeBoundary(t *testing.T) {
	s := newTestStore(t)
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunStarted}, Run: &RunEventData{Kind: ReActRun}})
	for i := range 24 {
		call := ToolCallEventData{CallID: fmt.Sprintf("call-%d", i), ToolCallID: fmt.Sprintf("model-%d", i), ToolName: "read", Arguments: `{}`}
		appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &call})
		call.Result = "evidence"
		appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallCompleted}, ToolCall: &call})
	}
	call := ToolCallEventData{CallID: "last", ToolCallID: "model-last", ToolName: "write", Arguments: `{"content":"` + strings.Repeat("x", 20000) + `"}`}
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallStarted}, ToolCall: &call})
	checkpoint := readTestCheckpoint(t, s)
	if checkpoint.LastSequence != 50 || checkpoint.Runs[0].Calls[24].Status != Unknown {
		t.Fatal("snapshot omitted an in-flight call")
	}
	call.Result = "written"
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: ToolCallCompleted}, ToolCall: &call})
	appendEvent(t, s, Event{EventHeader: EventHeader{Type: RunCompleted}, Run: &RunEventData{Answer: "done"}})
	reopened, _ := NewJSONLEventStore(s.root)
	compareFullReplay(t, reopened)
}

func TestCheckpointReplayRecoveryAncestryAndIsolation(t *testing.T) {
	s := newTestStore(t)
	s.checkpointInterval = 0
	r := NewRecorder(s, "conversation")
	old, err := r.StartRun(t.Context(), "update code", "", PlanAndExecuteRun)
	if err != nil {
		t.Fatal(err)
	}
	if err := old.SavePlan(t.Context(), testPlan(1)); err != nil {
		t.Fatal(err)
	}
	if err := old.RecordTask(t.Context(), TaskStarted, TaskEventData{Revision: 1, TaskID: "read", Attempt: 1}); err != nil {
		t.Fatal(err)
	}
	call, err := old.StartToolCall(t.Context(), ToolCallEventData{ToolCallID: "model", ToolName: "read", Arguments: `{}`, Task: &TaskReference{Revision: 1, TaskID: "read", Attempt: 1, Phase: "worker"}})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SaveCheckpoint(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	checkpoint := readTestCheckpoint(t, s)
	original, _ := json.Marshal(checkpoint)
	run, _, err := r.StartRecoveredRun(t.Context(), checkpoint.Runs[0])
	if err != nil {
		t.Fatal(err)
	}
	if err := run.ReconcileToolCall(t.Context(), ToolCallReconciliation{SourceRunID: old.ID(), CallID: call.CallID, Outcome: ConfirmedCompleted, Evidence: "checked", Result: "source"}); err != nil {
		t.Fatal(err)
	}
	for _, e := range []Event{
		taskEvent(TaskCancelled, "read", 1, 1),
		taskEvent(TaskStarted, "read", 1, 2),
		taskEvent(TaskWorkCompleted, "read", 1, 2),
		taskEvent(TaskCompleted, "read", 1, 2),
		taskEvent(TaskStarted, "edit", 1, 1),
		taskEvent(TaskWorkCompleted, "edit", 1, 1),
		taskEvent(TaskCompleted, "edit", 1, 1),
	} {
		if err := run.RecordTask(t.Context(), e.Type, *e.Task); err != nil {
			t.Fatal(err)
		}
	}
	if err := run.FinishRun(t.Context(), "done", nil); err != nil {
		t.Fatal(err)
	}
	suffix, err := s.Load(t.Context(), "conversation", checkpoint.LastSequence)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := ReplayFrom(checkpoint, suffix)
	if err != nil || len(runs) != 2 || runs[0].Status != Resumed || runs[1].Status != Completed || runs[0].Calls[0].Status != Reconciled || runs[1].Calls[0].Status != Reconciled {
		t.Fatalf("lost inherited progress: %+v, %v", runs, err)
	}
	after, _ := json.Marshal(checkpoint)
	if !bytes.Equal(original, after) {
		t.Fatal("incremental replay mutated its checkpoint")
	}
	bad := append(append([]Event(nil), suffix...), suffix[len(suffix)-1])
	if _, err := ReplayFrom(checkpoint, bad); err == nil {
		t.Fatal("accepted duplicate suffix sequence")
	}
	after, _ = json.Marshal(checkpoint)
	if !bytes.Equal(original, after) {
		t.Fatal("failed incremental replay mutated its checkpoint")
	}
	if _, err := ReplayFrom(checkpoint, suffix[1:]); err == nil {
		t.Fatal("accepted skipped suffix sequence")
	}
	reopened, _ := NewJSONLEventStore(s.root)
	compareFullReplay(t, reopened)
	if err := reopened.SaveCheckpoint(t.Context(), "conversation"); err != nil {
		t.Fatal(err)
	}
	newRecorder := NewRecorder(reopened, "conversation")
	if _, err := newRecorder.StartRun(t.Context(), "next", "", ReActRun); err != nil {
		t.Fatal(err)
	}
	compareFullReplay(t, reopened)
}

func TestCheckpointSeedsAppendAfterReopenWithoutLoad(t *testing.T) {
	s := newTestStore(t)
	startedRuns(t, s, 1, 52)
	reopened, _ := NewJSONLEventStore(s.root)
	startedRuns(t, reopened, 53, 53)
	if reopened.positions["conversation"].lastSequence != 53 {
		t.Fatal("append lost sequence after indexed restart")
	}
	compareFullReplay(t, reopened)
}
