package multiagent

import "AgentCLI/internal/plan"

type PlanAction string

const (
	PlanExecute PlanAction = "EXECUTE"
	PlanRevise  PlanAction = "REVISE"
	PlanCancel  PlanAction = "CANCEL"
)

// PlanDecisionProvider asks whether a generated plan should be executed,
// revised, or cancelled. It is the approval gate before task execution.
type PlanDecisionProvider interface {
	Decide(
		p *plan.Plan,
	) (
		action PlanAction,
		feedback string,
		err error,
	)
}
