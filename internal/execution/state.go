package execution

import (
	"AgentCLI/internal/plan"
	"fmt"
)

type RunStatus string
type CallStatus string

const (
	Interrupted RunStatus  = "interrupted"
	Completed   RunStatus  = "completed"
	Failed      RunStatus  = "failed"
	Cancelled   RunStatus  = "cancelled"
	Abandoned   RunStatus  = "abandoned"
	Resumed     RunStatus  = "resumed"
	Unknown     CallStatus = "unknown"
	Returned    CallStatus = "returned"
	Reconciled  CallStatus = "reconciled"
	NotExecuted CallStatus = "not_executed"
)

type CallState struct {
	ReusedFrom                              string
	OriginRunID                             string
	Task                                    *TaskReference
	CallID, ToolCallID, ToolName, Arguments string
	Status                                  CallStatus
	Result, Error                           string
	Reconciliation                          *ToolCallReconciliation
}

type RunState struct {
	Kind                                        RunKind
	RunID, ConversationID, Input, Answer, Error string
	Status                                      RunStatus
	Calls                                       []CallState
	LastSequence                                uint64
	ResumedFrom                                 string
	Plan                                        *PlanEventData
	Tasks                                       []TaskState
}

type TaskStatus string

const (
	TaskPending        TaskStatus = "pending"
	TaskInterrupted    TaskStatus = "interrupted"
	TaskAwaitingReview TaskStatus = "awaiting_review"
	TaskDone           TaskStatus = "completed"
	TaskErrored        TaskStatus = "failed"
	TaskStopped        TaskStatus = "cancelled"
)

type TaskState struct {
	Definition        plan.TaskDefinition
	Revision, Attempt uint64
	Status            TaskStatus
	Result, Error     string
}

// updatePlanAndTaskState 根据计划或任务事件更新所属 Run 的状态，由 Replay 和 Recorder 共用。
// e 仅接受 PlanSaved（e.Plan），以及 TaskStarted / TaskWorkCompleted /
// TaskCompleted / TaskFailed / TaskCancelled（e.Task）；调用方须先校验事件结构。
// run 是应用该事件之前的状态；计划事件替换 Plan 和 Tasks，任务事件更新匹配的 TaskState。
// Recorder 调用时须提供独立的 Tasks 切片，避免尚未持久化的修改影响已提交状态。
func updatePlanAndTaskState(run *RunState, e Event) error {
	if e.Type == PlanSaved {
		p := *e.Plan
		expected := uint64(1)
		if run.Plan != nil {
			last := run.Plan
			if p.PlanID != last.PlanID {
				return fmt.Errorf("plan ID changed within run")
			}
			expected = last.Revision + 1
		}
		if p.Revision != expected {
			return fmt.Errorf("plan revision must be %d", expected)
		}
		for _, call := range run.Calls {
			if call.Status == Unknown {
				return fmt.Errorf("resolve tool calls before replacing plan")
			}
		}
		for _, task := range run.Tasks {
			if task.Status == TaskInterrupted || task.Status == TaskAwaitingReview {
				return fmt.Errorf("settle active tasks before replacing plan")
			}
		}
		run.Plan = &p
		run.Tasks = make([]TaskState, 0, len(p.Tasks))
		for _, def := range p.Tasks {
			run.Tasks = append(run.Tasks, TaskState{Definition: def, Revision: p.Revision, Status: TaskPending})
		}
		return nil
	}
	if run.Plan == nil || e.Task.Revision != run.Plan.Revision {
		return fmt.Errorf("task event must reference current plan revision")
	}
	var task *TaskState
	for i := range run.Tasks {
		if run.Tasks[i].Revision == e.Task.Revision && run.Tasks[i].Definition.ID == e.Task.TaskID {
			task = &run.Tasks[i]
			break
		}
	}
	if task == nil {
		return fmt.Errorf("task absent from confirmed plan: %s", e.Task.TaskID)
	}
	data := e.Task
	switch e.Type {
	case TaskStarted:
		for _, call := range run.Calls {
			if call.Status == Unknown && call.Task != nil && call.Task.Revision == data.Revision && call.Task.TaskID == data.TaskID {
				return fmt.Errorf("resolve previous attempt calls before retrying task")
			}
		}
		if (task.Status != TaskPending && task.Status != TaskErrored && task.Status != TaskStopped) || data.Attempt != task.Attempt+1 {
			return fmt.Errorf("invalid task start or attempt")
		}
		for _, dep := range task.Definition.Dependencies {
			done := false
			for _, candidate := range run.Tasks {
				if candidate.Revision == data.Revision && candidate.Definition.ID == dep && candidate.Status == TaskDone {
					done = true
					break
				}
			}
			if !done {
				return fmt.Errorf("task dependency %s is incomplete", dep)
			}
		}
		task.Status, task.Attempt, task.Result, task.Error = TaskInterrupted, data.Attempt, "", ""
	case TaskWorkCompleted:
		for _, call := range run.Calls {
			if call.Status == Unknown && call.Task != nil && call.Task.Revision == data.Revision && call.Task.TaskID == data.TaskID && call.Task.Attempt == data.Attempt {
				return fmt.Errorf("worker has unresolved tool calls")
			}
		}
		if task.Status != TaskInterrupted || data.Attempt != task.Attempt {
			return fmt.Errorf("worker result without matching task start")
		}
		task.Status, task.Result = TaskAwaitingReview, data.Result
	case TaskCompleted:
		for _, call := range run.Calls {
			if call.Status == Unknown && call.Task != nil && call.Task.Revision == data.Revision && call.Task.TaskID == data.TaskID && call.Task.Attempt == data.Attempt {
				return fmt.Errorf("review has unresolved tool calls")
			}
		}
		if task.Status != TaskAwaitingReview || data.Attempt != task.Attempt {
			return fmt.Errorf("task completion requires matching worker result")
		}
		task.Status = TaskDone
		if data.Result != "" {
			task.Result = data.Result
		}
	case TaskFailed, TaskCancelled:
		if (task.Status != TaskInterrupted && task.Status != TaskAwaitingReview) || data.Attempt != task.Attempt {
			return fmt.Errorf("task termination without matching active attempt")
		}
		task.Status, task.Error = TaskErrored, data.Error
		if e.Type == TaskCancelled {
			task.Status = TaskStopped
		}
	}
	return nil
}

// checkPlanTasksCompleted 不接收事件，只检查 run 当前计划中的任务是否全部完成。
// Recorder 在成功结束 Run 前调用；Replay 在处理 RunCompleted 事件时调用。
// 没有计划时直接通过；本函数不修改状态，也不检查工具调用是否完成。
func checkPlanTasksCompleted(run RunState) error {
	if run.Plan == nil {
		return nil
	}
	revision := run.Plan.Revision
	for _, task := range run.Tasks {
		if task.Revision == revision && task.Status != TaskDone {
			return fmt.Errorf("completed run has incomplete plan tasks")
		}
	}
	return nil
}

// sameTaskReference 不接收完整事件或状态，只比较两份任务引用，且允许两者同时为 nil。
// Replay 处理 ToolCallCompleted 时，a 来自已有 CallState.Task，b 来自 e.ToolCall.Task，
// 用于确认完成事件与开始时记录的计划版本、任务、尝试次数及阶段完全一致。
func sameTaskReference(a, b *TaskReference) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// validateToolTask 检查工具调用携带的任务引用是否匹配 run 中当前允许执行的任务阶段。
// Recorder.StartToolCall 传入当前状态和 call.Task；Replay 处理 ToolCallStarted 时
// 传入事件之前的状态和 e.ToolCall.Task。ref == nil 表示无计划任务引用，直接通过。
// Worker 要求 TaskInterrupted（已开始、尚未记录 Worker 完成），Reviewer 要求
// TaskAwaitingReview；两者还必须匹配当前计划版本、任务 ID 和尝试次数。本函数不修改状态。
func validateToolTask(run RunState, ref *TaskReference) error {
	if ref == nil {
		return nil
	} // ReAct calls have no plan task.
	if run.Plan == nil || run.Plan.Revision != ref.Revision {
		return fmt.Errorf("tool references inactive plan")
	}
	for _, task := range run.Tasks {
		active := task.Status == TaskInterrupted && ref.Phase != "reviewer" || task.Status == TaskAwaitingReview && ref.Phase == "reviewer"
		if task.Revision == ref.Revision && task.Definition.ID == ref.TaskID && task.Attempt == ref.Attempt && active {
			return nil
		}
	}
	return fmt.Errorf("tool requires an active matching task attempt")
}

// applyReconciliation 仅由 Replay 在处理 ToolCallReconciled 事件时调用，读取 e.Reconciliation。
// runs 是同一会话截至该事件已重建的全部 RunState；indices 将 RunID 映射到 runs 下标；
// current 是该事件所属的当前开放 Run 的下标。核查对象必须属于当前 Run 或其恢复祖先，
// 且调用状态为 Unknown。核查结论同步到当前恢复链中该调用的副本，并更新 LastSequence。
// 本函数只更新内存状态，不执行实际核查，也不向旧 Run 追加事件。
func applyReconciliation(runs []RunState, indices map[string]int, current int, e Event) error {
	data := e.Reconciliation
	target, exists := indices[data.SourceRunID]
	if !exists {
		return fmt.Errorf("reconciliation source run does not exist")
	}
	related := current == target
	for ancestor := runs[current].ResumedFrom; ancestor != "" && !related; {
		i, ok := indices[ancestor]
		if !ok {
			break
		}
		related = i == target
		ancestor = runs[i].ResumedFrom
	}
	if !related {
		return fmt.Errorf("cannot reconcile unrelated run")
	}
	for i := range runs[target].Calls {
		call := &runs[target].Calls[i]
		if call.CallID != data.CallID {
			continue
		}
		if call.Status != Unknown {
			return fmt.Errorf("cannot reconcile already resolved tool call")
		}
		origin := call.OriginRunID
		for j := range runs {
			if !runIsAncestor(runs, indices, j, current) {
				continue
			}
			for k := range runs[j].Calls {
				inherited := &runs[j].Calls[k]
				if inherited.CallID != data.CallID || inherited.OriginRunID != origin {
					continue
				}
				reconcileCall(inherited, *data)
				runs[j].LastSequence = e.Sequence
			}
		}
		return nil
	}
	return fmt.Errorf("reconciliation call does not exist")
}

// reconcileCall 更新单个调用副本；call 是待核查的 Unknown 调用，data 是核查结论，
// 对应 ToolCallReconciled 事件的 Reconciliation 载荷，不是完整 Event。
// Replay 和 Recorder 均会调用；调用方负责校验调用身份及原状态。
// completed -> Reconciled 并保存结果；not_executed -> NotExecuted；unknown 保持 Unknown。
// 三种结论都会保存核查证据；本函数不执行工具或检查外部系统。
func reconcileCall(call *CallState, data ToolCallReconciliation) {
	copy := data
	call.Reconciliation = &copy
	switch data.Outcome {
	case ConfirmedCompleted:
		call.Status, call.Result = Reconciled, data.Result
	case ConfirmedNotExecuted:
		call.Status = NotExecuted
	case StillUnknown: // Keep uncertainty explicit.
	}
}

// runIsAncestor 不接收事件；runs 和 indices 来自 Replay 已重建的同一会话状态。
// ancestor、current 都是 runs 下标，沿 ResumedFrom 检查前者是否为后者的恢复祖先。
// 同一个 Run 也返回 true；用于限制核查结论只同步到当前恢复链，本函数不修改状态。
func runIsAncestor(runs []RunState, indices map[string]int, ancestor, current int) bool {
	for {
		if ancestor == current {
			return true
		}
		parent, ok := indices[runs[current].ResumedFrom]
		if !ok {
			return false
		}
		current = parent
	}
}
