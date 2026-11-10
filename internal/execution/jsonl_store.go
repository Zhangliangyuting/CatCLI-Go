package execution

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

const maxEventBytes = 16 << 20 //16 MiB

// JSONLEventStore serializes access within one instance. Use one instance per
// log directory; concurrent CLI processes writing the same session are unsupported.
// Files are opened per operation, so there is no Close or buffered pending data.
type JSONLEventStore struct {
	mu                 sync.Mutex
	root               string
	positions          map[string]logPosition
	checkpointInterval uint64
}

type logPosition struct {
	lastSequence   uint64
	committedBytes int64
	fileInfo       os.FileInfo
}

func NewJSONLEventStore(root string) (*JSONLEventStore, error) {
	if strings.TrimSpace(root) == "" {
		return nil, fmt.Errorf("execution log root is empty")
	}
	return &JSONLEventStore{root: root, positions: make(map[string]logPosition), checkpointInterval: 50}, nil
}

func (s *JSONLEventStore) path(conversationID string) (string, error) {
	if !validID(conversationID) {
		return "", fmt.Errorf("invalid execution conversation ID")
	}
	return filepath.Join(s.root, conversationID+".jsonl"), nil
}

func (s *JSONLEventStore) Append(ctx context.Context, e Event) (Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(e.ConversationID)
	if err != nil {
		return Event{}, err
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	if err := os.MkdirAll(s.root, 0o700); err != nil {
		return Event{}, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		delete(s.positions, e.ConversationID)
		return Event{}, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		delete(s.positions, e.ConversationID)
		return Event{}, err
	}
	position, cached := s.positions[e.ConversationID]
	if !cached || info.Size() != position.committedBytes || !os.SameFile(info, position.fileInfo) {
		// Scan once after restart, or when the file no longer matches our cache.
		delete(s.positions, e.ConversationID)
		base, events, committedBytes, err := s.readLogLocked(ctx, f, e.ConversationID)
		if err != nil {
			return Event{}, err
		}
		position = logPosition{lastSequence: base.LastSequence + uint64(len(events)), committedBytes: committedBytes, fileInfo: info}
	}
	e.Sequence, e.Timestamp = position.lastSequence+1, time.Now().UTC()
	if err := validateEvent(e); err != nil {
		return Event{}, err
	}
	data, err := json.Marshal(e)
	if err != nil {
		return Event{}, err
	}
	if len(data)+1 > maxEventBytes {
		return Event{}, fmt.Errorf("execution event exceeds %d bytes", maxEventBytes)
	}
	if err := ctx.Err(); err != nil {
		return Event{}, err
	}
	// Any error after mutation begins leaves the file's state uncertain. Keep
	// the cache invalid until both Write and Sync succeed.
	delete(s.positions, e.ConversationID)
	if info.Size() != position.committedBytes {
		// Only an unterminated tail may be discarded; readEvents already
		// rejected malformed committed lines before reaching this point.
		if err := f.Truncate(position.committedBytes); err != nil {
			return Event{}, err
		}
	}
	if _, err := f.Seek(position.committedBytes, 0); err != nil {
		return Event{}, err
	}
	if _, err := f.Write(append(data, '\n')); err != nil {
		return Event{}, fmt.Errorf("append execution event: %w", err)
	}
	if err := f.Sync(); err != nil {
		return Event{}, fmt.Errorf("sync execution event: %w", err)
	}
	position.lastSequence = e.Sequence
	position.committedBytes += int64(len(data)) + 1
	s.positions[e.ConversationID] = position
	if s.checkpointInterval > 0 && e.Sequence%s.checkpointInterval == 0 {
		// The event is already durable. A failed acceleration snapshot must not
		// turn this successful append into an ambiguous execution failure.
		_ = s.saveCheckpointLocked(ctx, f, e.ConversationID)
	}
	return e, nil
}

func (s *JSONLEventStore) Load(ctx context.Context, conversationID string, afterSequence uint64) ([]Event, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(conversationID)
	if err != nil {
		return nil, err
	}
	// A failed full read must not leave a previously cached position trusted.
	delete(s.positions, conversationID)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Raw-event inspection is separate from checkpoint-based state recovery.
	events, committedBytes, err := readEvents(ctx, f, conversationID)
	if err != nil {
		return nil, err
	}
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	s.positions[conversationID] = logPosition{lastSequence: uint64(len(events)), committedBytes: committedBytes, fileInfo: info}
	var selected []Event
	for _, e := range events {
		if e.Sequence > afterSequence {
			selected = append(selected, e)
		}
	}
	return selected, nil
}

func readEvents(ctx context.Context, f *os.File, conversationID string) ([]Event, int64, error) {
	return readEventsFrom(ctx, f, conversationID, 0, 0)
}

func readEventsFrom(ctx context.Context, f *os.File, conversationID string, afterSequence uint64, offset int64) ([]Event, int64, error) {
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return nil, 0, err
	}
	info, err := f.Stat() //获取文件大小
	if err != nil {
		return nil, 0, err
	}
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 4096), maxEventBytes)
	scanner.Split(func(data []byte, atEOF bool) (int, []byte, error) {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			return i + 1, data[:i], nil
		}
		if atEOF && len(data) > 0 {
			return len(data), data, nil
		}
		return 0, nil, nil
	})
	var events []Event
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return nil, 0, err
		}
		line := scanner.Bytes()
		if offset+int64(len(line)) == info.Size() {
			break // Unterminated tail is not a committed event.
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			return nil, 0, fmt.Errorf("corrupt execution event at byte %d: %w", offset, err)
		}
		if err := validateEvent(e); err != nil {
			return nil, 0, fmt.Errorf("execution event at byte %d: %w", offset, err)
		}
		if e.ConversationID != conversationID || e.Sequence != afterSequence+uint64(len(events))+1 || e.Sequence <= afterSequence {
			return nil, 0, fmt.Errorf("execution log identity or sequence mismatch at byte %d", offset)
		}
		events = append(events, e)
		offset += int64(len(line)) + 1
	}
	if err := scanner.Err(); err != nil {
		return nil, 0, fmt.Errorf("read execution events: %w", err)
	}
	return events, offset, nil
}
