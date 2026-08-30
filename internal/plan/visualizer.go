package plan

import (
	"fmt"
	"strings"
)

func (p *Plan) Visualize() string {
	var output strings.Builder

	completed := 0

	for _, taskID := range p.executionOrder {
		task, exists := p.tasks[taskID]
		if !exists {
			continue
		}

		if task.Status() == COMPLETED {
			completed++
		}
	}

	total := len(p.tasks)
	progress := 0

	if total > 0 {
		progress = completed * 100 / total
	}

	fmt.Fprintf(&output, "\n计划：%s\n", p.goal)
	fmt.Fprintf(&output, "摘要：%s\n", p.summary)
	fmt.Fprintf(&output, "状态：%s\n", p.status)
	fmt.Fprintf(
		&output,
		"进度：%d/%d (%d%%)\n\n",
		completed,
		total,
		progress,
	)

	for _, taskID := range p.executionOrder {
		task, exists := p.tasks[taskID]
		if !exists {
			continue
		}

		fmt.Fprintf(
			&output,
			"%s %-10s %-20s %s",
			taskStatusIcon(task.Status()),
			task.ID(),
			task.Name(),
			task.Type(),
		)

		if len(task.Dependencies()) > 0 {
			fmt.Fprintf(
				&output,
				"  依赖: %s",
				strings.Join(task.Dependencies(), ", "),
			)
		}
		if len(task.ReadResources()) > 0 {
			fmt.Fprintf(&output, "  读取: %s", strings.Join(task.ReadResources(), ", "))
		}
		if len(task.WriteResources()) > 0 {
			fmt.Fprintf(&output, "  写入: %s", strings.Join(task.WriteResources(), ", "))
		}

		output.WriteString("\n")
	}

	return output.String()
}

func taskStatusIcon(status TaskStatus) string {
	switch status {
	case PENDING:
		return "[ ]"
	case RUNNING:
		return "[▶]"
	case COMPLETED:
		return "[✓]"
	case FAILED:
		return "[✗]"
	case CANCELLED:
		return "[×]"
	case BLOCKED:
		return "[!]"
	case SKIPPED:
		return "[-]"
	default:
		return "[?]"
	}
}
