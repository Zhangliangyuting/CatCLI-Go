package agent

import "AgentCLI/internal/memory"

type recordingAgentTranscript struct {
	conversationIDs []string
	entries         []memory.Entry
}

func (store *recordingAgentTranscript) Append(conversationID string, entries ...memory.Entry) error {
	store.conversationIDs = append(store.conversationIDs, conversationID)
	store.entries = append(store.entries, entries...)
	return nil
}

func (store *recordingAgentTranscript) Load(string) ([]memory.Entry, error) {
	return append([]memory.Entry(nil), store.entries...), nil
}
