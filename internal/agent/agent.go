package agent

import "context"

type Agent interface {
	Run(ctx context.Context, userInput string) (string, error)
}
