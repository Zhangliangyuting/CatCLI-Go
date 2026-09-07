package agent

import (
	"AgentCLI/internal/plan"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

type planScheduler struct {
	maxWorkers      int
	taskTimeout     time.Duration
	executorFactory TaskAgentFactory
}

// TaskAgentFactory creates an isolated executor for one plan task. Receiving
// the task ID lets callers assign separate memory and transcript namespaces.
type TaskAgentFactory func(taskID string) (Agent, error)

type taskJob struct {
	id     string
	prompt string
}

type taskOutcome struct {
	id     string
	result string
	err    error
}

func newPlanScheduler(
	maxWorkers int,
	taskTimeout time.Duration,
	executorFactory func() Agent,
) *planScheduler {
	return newPlanSchedulerWithTaskFactory(
		maxWorkers,
		taskTimeout,
		func(string) (Agent, error) {
			return executorFactory(), nil
		},
	)
}

func newPlanSchedulerWithTaskFactory(
	maxWorkers int,
	taskTimeout time.Duration,
	executorFactory TaskAgentFactory,
) *planScheduler {
	if maxWorkers < 1 {
		maxWorkers = 1
	}

	return &planScheduler{
		maxWorkers:      maxWorkers,
		taskTimeout:     taskTimeout,
		executorFactory: executorFactory,
	}
}

func (s *planScheduler) Execute(
	ctx context.Context,
	p *plan.Plan,
) (string, error) {
	return s.ExecuteWithObserver(ctx, p, nil)
}

func (s *planScheduler) ExecuteWithObserver(
	ctx context.Context,
	p *plan.Plan,
	observer Observer,
) (string, error) {
	observer = SynchronizedObserver(observer)
	executionCtx, cancelExecution := context.WithCancel(ctx)
	defer cancelExecution()
	ctxDone := executionCtx.Done()
	p.MarkRunning()

	taskIDs := p.ExecutionOrder()

	// remaining 表示任务还有多少依赖未完成。
	remaining := make(map[string]int, len(taskIDs))

	// dependency -> dependents
	dependents := make(map[string][]string, len(taskIDs))

	tasks := make(map[string]*plan.Task, len(taskIDs))

	for _, taskID := range taskIDs {
		task, exists := p.TaskByID(taskID)
		if !exists {
			p.MarkFailed()
			return "", fmt.Errorf("task not found: %s", taskID)
		}

		tasks[taskID] = task
		remaining[taskID] = len(task.Dependencies())

		for _, dependencyID := range task.Dependencies() {
			dependents[dependencyID] = append(
				dependents[dependencyID],
				taskID,
			)
		}
	}

	// 首批没有依赖的任务。
	ready := make([]string, 0, len(taskIDs))
	for _, taskID := range taskIDs {
		if remaining[taskID] == 0 {
			ready = append(ready, taskID)
		}
	}

	jobs := make(chan taskJob, s.maxWorkers)
	outcomes := make(chan taskOutcome, s.maxWorkers)

	var workers sync.WaitGroup
	for range s.maxWorkers {
		workers.Add(1)

		go func() {
			defer workers.Done()
			s.runWorker(executionCtx, jobs, outcomes, observer)
		}()
	}

	results := make(map[string]string, len(taskIDs))
	activeResources := newResourceTracker()
	running := 0
	completed := 0
	var executionErr error

	for len(ready) > 0 || running > 0 {
		// 填满空闲 worker。
		for executionErr == nil &&
			len(ready) > 0 &&
			running < s.maxWorkers { // 已派发但还没有处理结果的任务数量
			if err := executionCtx.Err(); err != nil {
				executionErr = err
				break
			}

			readyIndex := firstRunnableTask(ready, tasks, activeResources)
			if readyIndex < 0 {
				// 所有就绪任务都与运行中的任务存在资源冲突。
				// 等待一个运行中任务结束后再重新检查。
				break
			}

			taskID := ready[readyIndex]
			ready = append(ready[:readyIndex], ready[readyIndex+1:]...)

			task := tasks[taskID]
			activeResources.acquire(task)
			task.MarkRunning()

			emit(observer, Event{
				Type:    EventTaskStarted,
				TaskID:  task.ID(),
				Title:   task.Name(),
				Content: p.Visualize(),
			})

			// prompt 由调度器生成。
			// 此时所有依赖结果都已经完成。
			prompt := buildTaskPrompt(p, task)

			jobs <- taskJob{
				id:     taskID,
				prompt: prompt,
			}
			running++
		}

		if running == 0 {
			break
		}

		var outcome taskOutcome
		select {
		case outcome = <-outcomes:
		case <-ctxDone:
			if executionErr == nil {
				executionErr = executionCtx.Err()
			}
			cancelExecution()
			ctxDone = nil
			continue
		}
		running--

		task := tasks[outcome.id]
		activeResources.release(task)

		if outcome.err != nil {
			switch {
			case errors.Is(outcome.err, context.Canceled):
				task.MarkCancelled(outcome.err)
				emit(observer, Event{
					Type:    EventTaskCancelled,
					TaskID:  outcome.id,
					Title:   task.Name(),
					Content: outcome.err.Error(),
				})
			case errors.Is(outcome.err, context.DeadlineExceeded):
				task.MarkFailed(outcome.err)
				emit(observer, Event{
					Type:    EventTaskTimeout,
					TaskID:  outcome.id,
					Title:   task.Name(),
					Content: outcome.err.Error(),
				})
			default:
				task.MarkFailed(outcome.err)
				emit(observer, Event{
					Type:    EventTaskFailed,
					TaskID:  outcome.id,
					Title:   task.Name(),
					Content: outcome.err.Error(),
				})
			}

			if executionErr == nil {
				if ctx.Err() != nil {
					executionErr = ctx.Err()
				} else {
					executionErr = fmt.Errorf(
						"task %s failed: %w",
						outcome.id,
						outcome.err,
					)
				}
				cancelExecution()
			}

			// fail-fast：不再投递新任务，
			// 但等待已经投递的任务结束。
			continue
		}

		task.MarkCompleted(outcome.result)
		results[outcome.id] = outcome.result
		completed++

		emit(observer, Event{
			Type:    EventTaskCompleted,
			TaskID:  outcome.id,
			Title:   task.Name(),
			Content: p.Visualize(),
		})

		// 一个任务完成，立即释放它的后继任务。
		if executionErr == nil {
			for _, dependentID := range dependents[outcome.id] {
				remaining[dependentID]--

				if remaining[dependentID] == 0 {
					ready = append(ready, dependentID)
				}
			}
		}
	}

	close(jobs)
	workers.Wait()

	if executionErr != nil {
		switch {
		case errors.Is(executionErr, context.Canceled):
			p.MarkCancelled()
			emit(observer, Event{
				Type:    EventPlanCancelled,
				Title:   "计划执行已取消",
				Content: executionErr.Error(),
			})
		case errors.Is(executionErr, context.DeadlineExceeded):
			p.MarkFailed()
			emit(observer, Event{
				Type:    EventPlanTimeout,
				Title:   "计划执行超时",
				Content: executionErr.Error(),
			})
		default:
			p.MarkFailed()
			emit(observer, Event{
				Type:    EventPlanFailed,
				Title:   "计划执行失败",
				Content: executionErr.Error(),
			})
		}
		return formatTaskResults(taskIDs, results), executionErr
	}

	if completed != len(taskIDs) {
		p.MarkFailed()
		err := fmt.Errorf(
			"scheduler stopped: completed %d/%d tasks",
			completed,
			len(taskIDs),
		)
		emit(observer, Event{
			Type:    EventPlanFailed,
			Title:   "计划调度异常",
			Content: err.Error(),
		})
		return formatTaskResults(taskIDs, results), err
	}

	p.MarkCompleted()

	emit(observer, Event{
		Type:    EventPlanCompleted,
		Title:   "计划执行完成",
		Content: p.Visualize(),
	})

	return formatTaskResults(taskIDs, results), nil
}

func (s *planScheduler) runWorker(
	ctx context.Context,
	jobs <-chan taskJob,
	outcomes chan<- taskOutcome,
	observer Observer,
) {
	for job := range jobs {
		taskCtx := ctx
		cancel := func() {}
		if s.taskTimeout > 0 {
			taskCtx, cancel = context.WithTimeout(ctx, s.taskTimeout)
		}

		var result string
		var err error
		// 每个任务创建独立 Agent，避免共享短期上下文。
		var executor Agent
		var factoryErr error
		if s.executorFactory == nil {
			factoryErr = fmt.Errorf("task agent factory is nil")
		} else {
			executor, factoryErr = s.executorFactory(job.id)
		}
		if factoryErr != nil {
			err = fmt.Errorf("create task agent: %w", factoryErr)
		} else if executor == nil {
			err = fmt.Errorf("create task agent: factory returned nil")
		} else if observable, ok := executor.(ObservableAgent); ok {
			result, err = observable.RunWithObserver(
				taskCtx,
				job.prompt,
				func(event Event) {
					if event.TaskID == "" {
						event.TaskID = job.id
					}
					emit(observer, event)
				},
			)
		} else {
			result, err = executor.Run(taskCtx, job.prompt)
		}
		cancel()

		outcomes <- taskOutcome{
			id:     job.id,
			result: result,
			err:    err,
		}
	}
}

func formatTaskResults(
	taskIDs []string,
	results map[string]string,
) string {
	var output strings.Builder

	for _, taskID := range taskIDs {
		result, exists := results[taskID]
		if !exists {
			continue
		}

		fmt.Fprintf(&output, "%s: %s\n", taskID, result)
	}

	return output.String()
}

func buildTaskPrompt(p *plan.Plan, task *plan.Task) string {
	var context strings.Builder

	fmt.Fprintf(&context, "整体目标：\n%s\n\n", p.Goal())
	fmt.Fprintf(&context, "当前任务：\n%s\n\n", task.Description())
	if len(task.ReadResources()) > 0 {
		fmt.Fprintf(
			&context,
			"声明的只读资源：\n- %s\n\n",
			strings.Join(task.ReadResources(), "\n- "),
		)
	}
	if len(task.WriteResources()) > 0 {
		fmt.Fprintf(
			&context,
			"声明的写入资源：\n- %s\n\n",
			strings.Join(task.WriteResources(), "\n- "),
		)
	}

	if len(task.Dependencies()) > 0 {
		context.WriteString("前置任务及其执行结果：\n")

		for _, dependencyID := range task.Dependencies() {
			dependency, ok := p.TaskByID(dependencyID)
			if !ok {
				continue
			}

			fmt.Fprintf(
				&context,
				"- %s\n  结果：%s\n",
				dependency.Description(),
				dependency.Result(),
			)
		}

		context.WriteString("\n")
	}

	context.WriteString(`执行要求：
1. 只执行当前任务，不要重新规划整个任务。
2. 可以使用提供的工具完成任务。
3. 使用前置任务结果作为上下文。
4. 完成后简洁说明执行结果。
5. 如果无法完成，明确说明原因。
`)

	return context.String()
}
