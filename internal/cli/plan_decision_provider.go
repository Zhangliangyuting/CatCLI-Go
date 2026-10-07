package cli

import (
	"AgentCLI/internal/agent/multiagent"
	"AgentCLI/internal/plan"
	"bufio"
	"fmt"
	"strings"
)

// CLIPlanDecisionProvider collects the user's execute, revise, or cancel
// decision from the command line before a generated plan starts.
type CLIPlanDecisionProvider struct {
	reader *bufio.Reader
}

var _ multiagent.PlanDecisionProvider = (*CLIPlanDecisionProvider)(nil)

func NewCLIPlanDecisionProvider(reader *bufio.Reader) *CLIPlanDecisionProvider {
	return &CLIPlanDecisionProvider{reader: reader}
}

func (provider *CLIPlanDecisionProvider) Decide(
	p *plan.Plan,
) (multiagent.PlanAction, string, error) {
	for {
		fmt.Println(p.Visualize())
		fmt.Print("[e] 执行  [r] 修改计划  [c] 取消: ")

		input, err := provider.reader.ReadString('\n')
		if err != nil {
			return "", "", err
		}

		switch strings.ToLower(strings.TrimSpace(input)) {
		case "e", "execute":
			return multiagent.PlanExecute, "", nil

		case "r", "revise", "replan":
			fmt.Print("请输入计划修改意见: ")

			feedback, err := provider.reader.ReadString('\n')
			if err != nil {
				return "", "", err
			}

			feedback = strings.TrimSpace(feedback)
			if feedback == "" {
				fmt.Println("修改意见不能为空")
				continue
			}

			return multiagent.PlanRevise, feedback, nil

		case "c", "cancel":
			return multiagent.PlanCancel, "", nil

		default:
			fmt.Println("无效选项，请输入 e、r 或 c")
		}
	}
}
