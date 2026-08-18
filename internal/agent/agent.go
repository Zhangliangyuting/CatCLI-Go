package agent

type Agent interface {
	Run(userInput string) (string, error)
}
