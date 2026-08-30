package agent

import "AgentCLI/internal/plan"

type PlanAction string

const (
	PlanExecute PlanAction = "EXECUTE"
	PlanRevise  PlanAction = "REVISE"
	PlanCancel  PlanAction = "CANCEL"
)

type PlanReviewer interface {
	Review(
		p *plan.Plan,
	) (
		action PlanAction,
		feedback string,
		err error,
	)
}
