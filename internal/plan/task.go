package plan

import "time"

type Task struct {
	id           string
	name         string
	description  string
	taskType     TaskType
	status       TaskStatus
	result       string
	err          error
	dependencies []string // 依赖的其他任务
	dependents   []string // 依赖此任务的其他任务
	startTime    time.Time
	endTime      time.Time
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
	PENDING   TaskStatus = "PENDING"   // 等待执行
	RUNNING   TaskStatus = "RUNNING"   // 正在执行
	COMPLETED TaskStatus = "COMPLETED" // 执行完成
	FAILED    TaskStatus = "FAILED"    // 执行失败
	SKIPPED   TaskStatus = "SKIPPED"   // 被跳过
	BLOCKED   TaskStatus = "BLOCKED"   // 依赖失败或缺少输入
)

func NewTask(id, name, description string, taskType TaskType, dependencies []string) *Task {
	return &Task{
		id:           id,
		name:         name,
		description:  description,
		taskType:     taskType,
		status:       PENDING,
		dependencies: dependencies,
		dependents:   []string{},
	}
}

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

func (t *Task) MarkFailed(err error) {
	t.status = FAILED
	t.err = err
	t.endTime = time.Now()
}

func (t *Task) MarkBlocked(err error) {
	t.status = BLOCKED
	t.err = err
	t.endTime = time.Now()
}

func (t *Task) IsExecutable(allTasks map[string]*Task) bool {
	if t.status != PENDING {
		return false
	}

	for _, depID := range t.dependencies {

		dep, ok := allTasks[depID]
		if !ok || dep.status != COMPLETED {
			return false
		}
	}

	return true
}

func (t *Task) Execute() (string, error) {
	t.MarkRunning()

	// 模拟任务执行逻辑
	// 这里可以根据任务类型执行不同的操作，例如调用工具、读取文件等
	// 目前仅模拟成功执行
	result := "Task executed successfully"
	t.MarkCompleted(result)

	return result, nil
}

func (t *Task) Dependencies() []string {
	return t.dependencies
}

func (t *Task) Dependents() []string {
	return t.dependents
}

func (t *Task) ID() string {
	return t.id
}

func (t *Task) Status() TaskStatus {
	return t.status
}

func (t *Task) Description() string {
	return t.description
}

func (t *Task) Error() error {
	return t.err
}

func (t *Task) Result() string {
	return t.result
}

func (t *Task) SetDependencies(dependencies []string) {
	t.dependencies = dependencies
}

func (t *Task) SetDependents(dependents []string) {
	t.dependents = dependents
}
