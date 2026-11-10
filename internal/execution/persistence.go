package execution

import "context"

type EventStore interface {
	// Append assigns a conversation-wide sequence and UTC timestamp.
	// Success means the complete newline-terminated record has been synced.
	Append(context.Context, Event) (Event, error)
	Load(ctx context.Context, conversationID string, afterSequence uint64) ([]Event, error)
}
