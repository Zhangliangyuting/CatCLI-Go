// Package execution records execution facts and reconstructs run state without
// invoking models or tools. It does not implement automatic resumption.
package execution

import (
	"AgentCLI/internal/plan"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

type EventType string

type RunKind string

const (
	ReActRun          RunKind = "react"
	PlanAndExecuteRun RunKind = "plan_and_execute"
)

const (
	RunStarted         EventType = "run_started"
	RunCompleted       EventType = "run_completed"
	RunFailed          EventType = "run_failed"
	RunCancelled       EventType = "run_cancelled"
	RunAbandoned       EventType = "run_abandoned"
	ToolCallStarted    EventType = "tool_call_started"
	ToolCallCompleted  EventType = "tool_call_completed"
	PlanSaved          EventType = "plan_saved"
	TaskStarted        EventType = "task_started"
	TaskWorkCompleted  EventType = "task_work_completed"
	TaskCompleted      EventType = "task_completed"
	TaskFailed         EventType = "task_failed"
	TaskCancelled      EventType = "task_cancelled"
	ToolCallReconciled EventType = "tool_call_reconciled"
)

// EventHeader identifies and orders an event, regardless of its payload type.
type EventHeader struct {
	Sequence       uint64    `json:"sequence"`
	ConversationID string    `json:"conversation_id"`
	RunID          string    `json:"run_id"`
	Type           EventType `json:"type"`
	Timestamp      time.Time `json:"timestamp"`
}

type RunEventData struct {
	Kind        RunKind `json:"kind,omitempty"`
	ResumedFrom string  `json:"resumed_from,omitempty"`
	Input       string  `json:"input,omitempty"`
	Answer      string  `json:"answer,omitempty"`
	Error       string  `json:"error,omitempty"`
}

// TaskReference binds a Worker invocation to a confirmed plan and attempt.
type TaskReference struct {
	Phase    string `json:"phase,omitempty"` // empty/worker or reviewer
	Revision uint64 `json:"revision"`
	TaskID   string `json:"task_id"`
	Attempt  uint64 `json:"attempt"`
}

// CallID is our execution identity; ToolCallID is the model's protocol identity.
// Arguments remains a string so malformed model arguments can also be recorded.
type ToolCallEventData struct {
	ReusedFrom string         `json:"reused_from,omitempty"`
	Task       *TaskReference `json:"task,omitempty"`
	CallID     string         `json:"call_id"`
	ToolCallID string         `json:"tool_call_id"`
	ToolName   string         `json:"tool_name"`
	Arguments  string         `json:"arguments,omitempty"`
	Result     string         `json:"result,omitempty"`
	Error      string         `json:"error,omitempty"`
}

// PlanEventData is a confirmed full plan, not a draft or a patch. Revisions
// belong to one Run and increase from 1. Persist before dispatching its tasks.
type PlanEventData struct {
	PlanID   string                `json:"plan_id"`
	Revision uint64                `json:"revision"`
	Goal     string                `json:"goal"`
	Summary  string                `json:"summary,omitempty"`
	Tasks    []plan.TaskDefinition `json:"tasks"`
}

type TaskEventData struct {
	Revision uint64 `json:"revision"`
	TaskID   string `json:"task_id"`
	Attempt  uint64 `json:"attempt"`
	Result   string `json:"result,omitempty"`
	Error    string `json:"error,omitempty"`
}

type ReconciliationOutcome string

const (
	ConfirmedCompleted   ReconciliationOutcome = "completed"
	ConfirmedNotExecuted ReconciliationOutcome = "not_executed"
	StillUnknown         ReconciliationOutcome = "unknown"
)

// Evidence is supplied by a caller that has checked the external state. This
// event records a conclusion; it does not itself verify or re-execute anything.
// SourceRunID permits a new recovery Run to reconcile an ancestor's call.
type ToolCallReconciliation struct {
	SourceRunID string                `json:"source_run_id"`
	CallID      string                `json:"call_id"`
	Outcome     ReconciliationOutcome `json:"outcome"`
	Evidence    string                `json:"evidence"`
	Result      string                `json:"result,omitempty"`
}

// Exactly one payload must be present, matching the header's Type.
type Event struct {
	EventHeader
	Run            *RunEventData           `json:"run,omitempty"`
	ToolCall       *ToolCallEventData      `json:"tool_call,omitempty"`
	Plan           *PlanEventData          `json:"plan,omitempty"`
	Task           *TaskEventData          `json:"task,omitempty"`
	Reconciliation *ToolCallReconciliation `json:"reconciliation,omitempty"`
}

func NewID() (string, error) {
	var data [16]byte
	if _, err := rand.Read(data[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(data[:]), nil
}

func validID(id string) bool {
	if id == "" || len(id) > 200 {
		return false
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return false
		}
	}
	return true
}

func validateEvent(e Event) error {
	if e.Sequence == 0 || e.Timestamp.IsZero() || !validID(e.ConversationID) || !validID(e.RunID) {
		return fmt.Errorf("invalid execution event header")
	}
	payloads := 0
	for _, present := range []bool{e.Run != nil, e.ToolCall != nil, e.Plan != nil, e.Task != nil, e.Reconciliation != nil} {
		if present {
			payloads++
		}
	}
	if payloads != 1 {
		return fmt.Errorf("execution event requires exactly one payload")
	}
	switch e.Type {
	case RunStarted, RunCompleted, RunFailed, RunCancelled, RunAbandoned:
		if e.Run == nil || e.ToolCall != nil {
			return fmt.Errorf("run event requires only a run payload")
		}
		if e.Run.Kind != "" && (e.Type != RunStarted || (e.Run.Kind != ReActRun && e.Run.Kind != PlanAndExecuteRun)) {
			return fmt.Errorf("invalid execution run kind")
		}
		if source := e.Run.ResumedFrom; source != "" && (e.Type != RunStarted || !validID(source) || source == e.RunID) {
			return fmt.Errorf("resumed_from requires a distinct source ID on run_started")
		}
	case ToolCallStarted, ToolCallCompleted:
		if e.ToolCall == nil || e.Run != nil {
			return fmt.Errorf("tool event requires only a tool_call payload")
		}
		if !validID(e.ToolCall.CallID) || e.ToolCall.ToolCallID == "" || e.ToolCall.ToolName == "" {
			return fmt.Errorf("tool event requires execution ID, protocol ID and name")
		}
		if ref := e.ToolCall.Task; ref != nil && (!validID(ref.TaskID) || ref.Revision == 0 || ref.Attempt == 0) {
			return fmt.Errorf("invalid tool task reference")
		}
		if ref := e.ToolCall.Task; ref != nil && ref.Phase != "" && ref.Phase != "worker" && ref.Phase != "reviewer" {
			return fmt.Errorf("invalid task tool phase")
		}
	case PlanSaved:
		if e.Plan == nil {
			return fmt.Errorf("plan_saved requires a plan payload")
		}
		return validatePlan(*e.Plan)
	case TaskStarted, TaskWorkCompleted, TaskCompleted, TaskFailed, TaskCancelled:
		if e.Task == nil || !validID(e.Task.TaskID) || e.Task.Revision == 0 || e.Task.Attempt == 0 {
			return fmt.Errorf("task event requires task ID, plan revision and attempt")
		}
	case ToolCallReconciled:
		if e.Reconciliation == nil {
			return fmt.Errorf("tool_call_reconciled requires reconciliation payload")
		}
		r := e.Reconciliation
		if !validID(r.SourceRunID) || !validID(r.CallID) || r.Evidence == "" {
			return fmt.Errorf("reconciliation requires source run, call ID and evidence")
		}
		switch r.Outcome {
		case ConfirmedCompleted, ConfirmedNotExecuted, StillUnknown:
		default:
			return fmt.Errorf("invalid reconciliation outcome")
		}
	default:
		return fmt.Errorf("unknown execution event type %q", e.Type)
	}
	return nil
}

func validatePlan(p PlanEventData) error {
	if !validID(p.PlanID) || p.Revision == 0 || len(p.Tasks) == 0 {
		return fmt.Errorf("plan requires ID, revision and tasks")
	}
	tasks := make(map[string]plan.TaskDefinition, len(p.Tasks))
	for _, task := range p.Tasks {
		if !validID(task.ID) {
			return fmt.Errorf("invalid plan task ID")
		}
		if _, exists := tasks[task.ID]; exists {
			return fmt.Errorf("duplicate plan task %s", task.ID)
		}
		tasks[task.ID] = task
	}
	// DFS validates missing/duplicate dependencies and cycles before a plan can
	// become a committed scheduling input.
	state := map[string]int{}
	var visit func(string) error
	visit = func(id string) error {
		if state[id] == 1 {
			return fmt.Errorf("plan dependency cycle at %s", id)
		}
		if state[id] == 2 {
			return nil
		}
		state[id] = 1
		seen := map[string]bool{}
		for _, dep := range tasks[id].Dependencies {
			if _, ok := tasks[dep]; !ok || seen[dep] {
				return fmt.Errorf("missing or duplicate dependency %s", dep)
			}
			seen[dep] = true
			if err := visit(dep); err != nil {
				return err
			}
		}
		state[id] = 2
		return nil
	}
	for id := range tasks {
		if err := visit(id); err != nil {
			return err
		}
	}
	return nil
}
