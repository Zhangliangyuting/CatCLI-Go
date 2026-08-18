package plan

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Plan struct {
	id             string
	goal           string
	tasks          map[string]*Task
	executionOrder []string // 任务执行顺序
	status         PlanStatus
	summary        string
	startTime      time.Time
	endTime        time.Time
}

func NewPlan(id string, goal string) *Plan {
	return &Plan{
		id:             id,
		goal:           goal,
		tasks:          make(map[string]*Task),
		executionOrder: make([]string, 0),
		status:         PLAN_CREATED,
	}
}

type rawPlan struct {
	Goal    string    `json:"goal"`
	Summary string    `json:"summary"`
	Tasks   []rawTask `json:"tasks"`
}

type rawTask struct {
	ID           string   `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description"`
	Type         TaskType `json:"type"`
	Dependencies []string `json:"dependencies"`
}

type PlanStatus string

const (
	PLAN_CREATED   PlanStatus = "CREATED"   // 已创建，未开始执行
	PLAN_RUNNING   PlanStatus = "RUNNING"   // 正在执行
	PLAN_CANCELLED PlanStatus = "CANCELLED" // 已取消
	PLAN_COMPLETED PlanStatus = "COMPLETED" // 执行完成
	PLAN_FAILED    PlanStatus = "FAILED"    // 执行失败
)

func (p *Plan) MarkRunning() {
	p.status = PLAN_RUNNING
	p.startTime = time.Now()
}

func (p *Plan) MarkCompleted() {
	p.status = PLAN_COMPLETED
	p.endTime = time.Now()
}

func (p *Plan) MarkFailed() {
	p.status = PLAN_FAILED
	p.endTime = time.Now()
}

func (p *Plan) HasFailed() bool {
	for _, task := range p.tasks {
		if task.Status() == FAILED {
			return true
		}
	}
	return false
}

func (p *Plan) AddTask(task *Task) error {
	if _, exists := p.tasks[task.ID()]; exists {
		return fmt.Errorf("task with ID %s already exists in the plan", task.ID())
	}

	p.tasks[task.ID()] = task

	for _, depID := range task.Dependencies() {
		if depTask, exists := p.tasks[depID]; exists {
			depTask.SetDependents(append(depTask.Dependents(), task.ID()))
		} else {
			return fmt.Errorf("dependency task with ID %s does not exist", depID)
		}
	}

	return nil
}

func (p *Plan) computeExecutionOrder() error {
	p.executionOrder = p.executionOrder[:0]

	err := p.TopologicalSort()
	if err != nil {
		return fmt.Errorf("compute execution order: %w", err)
	}
	return nil
}

/** 1. DFS拓扑排序算法
func (p *Plan) topologicalSort(
	taskID string,
	visited map[string]bool,
	visiting map[string]bool,
) error {
	if visiting[taskID] {
		return fmt.Errorf("cycle detected at task: %s", taskID)
	}

	if visited[taskID] {
		return nil
	}

	task, exists := p.Tasks[taskID]
	if !exists {
		return fmt.Errorf("task not found: %s", taskID)
	}

	visiting[taskID] = true

	for _, depID := range task.Dependencies {
		if _, exists := p.Tasks[depID]; !exists {
			return fmt.Errorf("dependency not found: task %s depends on %s", taskID, depID)
		}

		if err := p.topologicalSort(depID, visited, visiting); err != nil {
			return err
		}
	}

	delete(visiting, taskID)
	visited[taskID] = true
	p.ExecutionOrder = append(p.ExecutionOrder, taskID)

	return nil
}
**/

// 拓扑排序算法:kahn算法，queue内部天然并行
func (p *Plan) TopologicalSort() error {
	indegree := make(map[string]int, len(p.tasks))
	dependents := make(map[string][]string, len(p.tasks))

	// 1. 计算每个任务的入度，并构建反向依赖关系。
	for taskID, task := range p.tasks {
		indegree[taskID] = len(task.Dependencies())

		for _, dependencyID := range task.Dependencies() {
			if _, exists := p.tasks[dependencyID]; !exists {
				return fmt.Errorf(
					"dependency not found: task %s depends on %s",
					taskID,
					dependencyID,
				)
			}

			// dependencyID 完成后，可以减少 taskID 的入度。
			dependents[dependencyID] = append(
				dependents[dependencyID],
				taskID,
			)
		}
	}

	// 2. 找到所有入度为 0 的任务。
	queue := make([]string, 0)

	for _, taskID := range orderedTaskIDs(p.tasks) {
		if indegree[taskID] == 0 {
			queue = append(queue, taskID)
		}
	}

	p.executionOrder = make([]string, 0, len(p.tasks))

	// 3. 不断执行没有依赖的任务。
	for len(queue) > 0 {
		taskID := queue[0]
		queue = queue[1:]

		p.executionOrder = append(p.executionOrder, taskID)

		// 4. 当前任务完成后，它的后续任务少了一个依赖。
		for _, dependentID := range dependents[taskID] {
			indegree[dependentID]--

			if indegree[dependentID] == 0 {
				queue = append(queue, dependentID)
			}
		}
	}

	// 5. 没有处理全部任务，说明存在循环依赖。
	if len(p.executionOrder) != len(p.tasks) {
		return fmt.Errorf("cycle detected in plan")
	}

	return nil
}

func orderedTaskIDs(tasks map[string]*Task) []string {
	ids := make([]string, 0, len(tasks))
	for taskID := range tasks {
		ids = append(ids, taskID)
	}

	sort.Slice(ids, func(i, j int) bool {
		left, leftOK := taskNumber(ids[i])
		right, rightOK := taskNumber(ids[j])
		if leftOK && rightOK {
			return left < right
		}
		if leftOK != rightOK {
			return leftOK
		}
		return ids[i] < ids[j]
	})

	return ids
}

func taskNumber(id string) (int, bool) {
	const prefix = "task_"
	if !strings.HasPrefix(id, prefix) {
		return 0, false
	}

	number, err := strconv.Atoi(strings.TrimPrefix(id, prefix))
	return number, err == nil
}

func (p *Plan) WriteSummary(summary string) (*Task, bool) {
	if p.summary == "" {
		p.summary = summary
		return nil, false
	}
	return nil, false
}

func (p *Plan) TaskByID(taskID string) (*Task, bool) {
	task, exists := p.tasks[taskID]
	return task, exists
}

func (p *Plan) Goal() string {
	return p.goal
}

func (p *Plan) ExecutionOrder() []string {
	return p.executionOrder
}
