package cli

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/plan"
	"bufio"
	"fmt"
	"strings"
)

type PlanReviewer struct {
	reader *bufio.Reader
}

var _ agent.PlanReviewer = (*PlanReviewer)(nil)

func NewPlanReviewer(reader *bufio.Reader) *PlanReviewer {
	return &PlanReviewer{
		reader: reader,
	}
}

func (r *PlanReviewer) Review(
	p *plan.Plan,
) (agent.PlanAction, string, error) {
	for {
		fmt.Println(p.Visualize())
		fmt.Print("[e] 执行  [r] 修改计划  [c] 取消: ")

		input, err := r.reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}

		switch strings.ToLower(strings.TrimSpace(input)) {
		case "e", "execute":
			return agent.PlanExecute, "", nil

		case "r", "revise", "replan":
			fmt.Print("请输入计划修改意见: ")

			feedback, err := r.reader.ReadString('\n')
			if err != nil {
				return "", "", err
			}

			feedback = strings.TrimSpace(feedback)
			if feedback == "" {
				fmt.Println("修改意见不能为空")
				continue
			}

			return agent.PlanRevise, feedback, nil

		case "c", "cancel":
			return agent.PlanCancel, "", nil

		default:
			fmt.Println("无效选项，请输入 e、r 或 c")
		}
	}
}
