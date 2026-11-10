package plan

import "time"

type Task struct {
	definition TaskDefinition
	status     TaskStatus
	result     string
	err        error
	dependents []string // 依赖此任务的其他任务
	startTime  time.Time
	endTime    time.Time
}

type TaskType string

const (
	PLANNING     TaskType = "PLANNING"     // 规划任务
	FILE_READ    TaskType = "FILE_READ"    // 读取文件，获取信息
	FILE_WRITE   TaskType = "FILE_WRITE"   // 写入文件，输出结果
	COMMAND      TaskType = "COMMAND"      // 执行命令，编译运行等
	ANALYSIS     TaskType = "ANALYSIS"     // 分析结果，中间决策
	VERIFICATION TaskType = "VERIFICATION" // 验证结果，检查正确性
)

type TaskStatus string

const (
	PENDING         TaskStatus = "PENDING" // 等待执行
	AWAITING_REVIEW TaskStatus = "AWAITING_REVIEW"
	RUNNING         TaskStatus = "RUNNING"   // 正在执行
	COMPLETED       TaskStatus = "COMPLETED" // 执行完成
	FAILED          TaskStatus = "FAILED"    // 执行失败
	CANCELLED       TaskStatus = "CANCELLED" // 执行被取消
	SKIPPED         TaskStatus = "SKIPPED"   // 被跳过
	BLOCKED         TaskStatus = "BLOCKED"   // 依赖失败或缺少输入
)

func NewTask(id, name, description string, taskType TaskType, dependencies []string) *Task {
	return NewTaskFromDefinition(TaskDefinition{ID: id, Name: name, Description: description, Type: taskType, Dependencies: dependencies})
}

func NewTaskFromDefinition(def TaskDefinition) *Task {
	return &Task{
		definition: cloneTaskDefinition(def),
		status:     PENDING,
		dependents: []string{},
	}
}

// Definition returns an independent description without runtime state.
func (t *Task) Definition() TaskDefinition { return cloneTaskDefinition(t.definition) }

func (t *Task) MarkRunning() {
	t.status = RUNNING
	t.err = nil
	t.startTime = time.Now()
	t.endTime = time.Time{}
}

func (t *Task) MarkCompleted(result string) {
	t.status = COMPLETED
	t.result = result
	t.err = nil
	t.endTime = time.Now()
}

func (t *Task) MarkAwaitingReview(result string) {
	t.status, t.result, t.err = AWAITING_REVIEW, result, nil
}

func (t *Task) MarkFailed(err error) {
	t.status = FAILED
	t.err = err
	t.endTime = time.Now()
}

func (t *Task) MarkCancelled(err error) {
	t.status = CANCELLED
	t.err = err
	t.endTime = time.Now()
}

func (t *Task) MarkBlocked(err error) {
	t.status = BLOCKED
	t.err = err
	t.endTime = time.Now()
}

func (t *Task) Dependencies() []string {
	return append([]string(nil), t.definition.Dependencies...)
}

func (t *Task) Dependents() []string {
	return t.dependents
}

func (t *Task) ID() string {
	return t.definition.ID
}

func (t *Task) Name() string {
	return t.definition.Name
}

func (t *Task) Type() TaskType {
	return t.definition.Type
}

func (t *Task) Status() TaskStatus {
	return t.status
}

func (t *Task) Description() string {
	return t.definition.Description
}

func (t *Task) Error() error {
	return t.err
}

func (t *Task) Result() string {
	return t.result
}

func (t *Task) SetDependencies(dependencies []string) {
	t.definition.Dependencies = append([]string(nil), dependencies...)
}

func (t *Task) SetDependents(dependents []string) {
	t.dependents = dependents
}

func (t *Task) ReadResources() []string {
	return append([]string(nil), t.definition.ReadResources...)
}

func (t *Task) WriteResources() []string {
	return append([]string(nil), t.definition.WriteResources...)
}

func (t *Task) SetResources(readResources, writeResources []string) {
	t.definition.ReadResources = append([]string(nil), readResources...)
	t.definition.WriteResources = append([]string(nil), writeResources...)
}
