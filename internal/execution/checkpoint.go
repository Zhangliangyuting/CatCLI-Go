package execution

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Checkpoint is an acceleration snapshot of a committed, append-only log prefix.
// All Runs are retained so ancestry, inherited calls and reconciliation still work.
type Checkpoint struct {
	ConversationID string     `json:"conversation_id"`
	LastSequence   uint64     `json:"last_sequence"`
	ByteOffset     int64      `json:"byte_offset"`
	Runs           []RunState `json:"runs"`
	LogID          string     `json:"log_id"`
	BoundaryHash   string     `json:"boundary_hash"`
	Checksum       string     `json:"checksum"`
}

func (c Checkpoint) validateState() error {
	if !validID(c.ConversationID) || c.LastSequence == 0 || len(c.Runs) == 0 {
		return fmt.Errorf("invalid checkpoint identity or sequence")
	}
	seen := map[string]bool{}
	var latest uint64
	for _, run := range c.Runs {
		if !validID(run.RunID) || seen[run.RunID] || run.ConversationID != c.ConversationID || run.LastSequence == 0 || run.LastSequence > c.LastSequence {
			return fmt.Errorf("invalid checkpoint run")
		}
		if run.ResumedFrom != "" && !seen[run.ResumedFrom] {
			return fmt.Errorf("checkpoint missing recovery ancestor")
		}
		switch run.Status {
		case Interrupted, Completed, Failed, Cancelled, Abandoned, Resumed:
		default:
			return fmt.Errorf("invalid checkpoint run status")
		}
		if run.LastSequence > latest {
			latest = run.LastSequence
		}
		seen[run.RunID] = true
	}
	if latest != c.LastSequence {
		return fmt.Errorf("checkpoint state does not reach its sequence")
	}
	return nil
}

func (c Checkpoint) digest() (string, error) {
	c.Checksum = ""
	data, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:]), nil
}

func logIdentity(info os.FileInfo) (string, error) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return "", fmt.Errorf("log file identity unavailable")
	}
	return fmt.Sprintf("%d:%d", stat.Dev, stat.Ino), nil
}

// checkpointBoundary reads only the final committed record before offset.
// ReadAt leaves the scanner's position untouched. Prefixes are immutable; this
// guards boundary offsets/replacement, not arbitrary edits to earlier records.
func checkpointBoundary(ctx context.Context, f *os.File, offset int64) (Event, string, error) {
	if offset <= 0 {
		return Event{}, "", fmt.Errorf("invalid checkpoint byte offset")
	}
	var chunks [][]byte
	total := 0
	end := offset
	for {
		if err := ctx.Err(); err != nil {
			return Event{}, "", err
		}
		start := end - 4096
		if start < 0 {
			start = 0
		}
		chunk := make([]byte, end-start)
		if _, err := f.ReadAt(chunk, start); err != nil {
			return Event{}, "", err
		}
		if len(chunks) == 0 && (len(chunk) == 0 || chunk[len(chunk)-1] != '\n') {
			return Event{}, "", fmt.Errorf("checkpoint offset is not a record boundary")
		}
		search := chunk
		if len(chunks) == 0 {
			search = chunk[:len(chunk)-1]
		}
		previous := bytes.LastIndexByte(search, '\n')
		chunk = chunk[previous+1:]
		chunks = append(chunks, chunk)
		total += len(chunk)
		if total > maxEventBytes {
			return Event{}, "", fmt.Errorf("checkpoint boundary record too large")
		}
		if previous >= 0 || start == 0 {
			data := make([]byte, 0, total)
			for i := len(chunks) - 1; i >= 0; i-- {
				data = append(data, chunks[i]...)
			}
			var e Event
			if err := json.Unmarshal(data, &e); err != nil {
				return Event{}, "", err
			}
			if err := validateEvent(e); err != nil {
				return Event{}, "", err
			}
			sum := sha256.Sum256(data)
			return e, hex.EncodeToString(sum[:]), nil
		}
		end = start
	}
}

func (s *JSONLEventStore) readCheckpointLocked(ctx context.Context, f *os.File, id string) (Checkpoint, error) {
	path, err := s.path(id)
	if err != nil {
		return Checkpoint{}, err
	}
	data, err := os.ReadFile(path + ".checkpoint.json")
	if err != nil {
		return Checkpoint{}, err
	}
	var checkpoint Checkpoint
	if err := json.Unmarshal(data, &checkpoint); err != nil {
		return Checkpoint{}, err
	}
	if err := checkpoint.validateState(); err != nil {
		return Checkpoint{}, err
	}
	digest, err := checkpoint.digest()
	if err != nil || digest != checkpoint.Checksum || checkpoint.ConversationID != id {
		return Checkpoint{}, fmt.Errorf("checkpoint checksum or conversation mismatch")
	}
	info, err := f.Stat()
	if err != nil {
		return Checkpoint{}, err
	}
	identity, err := logIdentity(info)
	if err != nil || identity != checkpoint.LogID || checkpoint.ByteOffset > info.Size() {
		return Checkpoint{}, fmt.Errorf("checkpoint log replaced or truncated")
	}
	e, hash, err := checkpointBoundary(ctx, f, checkpoint.ByteOffset)
	if err != nil || hash != checkpoint.BoundaryHash || e.Sequence != checkpoint.LastSequence || e.ConversationID != id {
		return Checkpoint{}, fmt.Errorf("checkpoint log boundary mismatch")
	}
	return checkpoint, nil
}

// Recovery always uses a valid checkpoint. Only a missing or invalid snapshot
// falls back to the full log; corrupt suffix events are reported as errors.
func (s *JSONLEventStore) readLogLocked(ctx context.Context, f *os.File, id string) (Checkpoint, []Event, int64, error) {
	if checkpoint, err := s.readCheckpointLocked(ctx, f, id); err == nil {
		events, offset, err := readEventsFrom(ctx, f, id, checkpoint.LastSequence, checkpoint.ByteOffset)
		return checkpoint, events, offset, err
	}
	events, offset, err := readEvents(ctx, f, id)
	return Checkpoint{}, events, offset, err
}

func (s *JSONLEventStore) loadRunsLocked(ctx context.Context, f *os.File, id string) (Checkpoint, error) {
	base, events, offset, err := s.readLogLocked(ctx, f, id)
	if err != nil {
		return Checkpoint{}, err
	}
	var runs []RunState
	if base.LastSequence > 0 {
		runs, err = ReplayFrom(base, events)
		if err != nil {
			// A snapshot is disposable, including one with invalid state. The
			// complete event log remains authoritative.
			base = Checkpoint{}
			events, offset, err = readEvents(ctx, f, id)
			if err != nil {
				return Checkpoint{}, err
			}
			runs, err = Replay(events)
		}
	} else {
		runs, err = Replay(events)
	}
	if err != nil {
		return Checkpoint{}, err
	}
	info, err := f.Stat()
	if err != nil {
		return Checkpoint{}, err
	}
	checkpoint := Checkpoint{ConversationID: id, LastSequence: base.LastSequence + uint64(len(events)), ByteOffset: offset, Runs: runs}
	s.positions[id] = logPosition{lastSequence: checkpoint.LastSequence, committedBytes: offset, fileInfo: info}
	return checkpoint, nil
}

// LoadRuns seeks directly to a validated snapshot's byte boundary and replays
// only the suffix. Missing/invalid snapshots fall back to full replay.
func (s *JSONLEventStore) LoadRuns(ctx context.Context, id string) ([]RunState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	delete(s.positions, id)
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
	checkpoint, err := s.loadRunsLocked(ctx, f, id)
	return checkpoint.Runs, err
}

// SaveCheckpoint is also suitable for a best-effort normal-exit snapshot.
func (s *JSONLEventStore) SaveCheckpoint(ctx context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.path(id)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	defer f.Close()
	return s.saveCheckpointLocked(ctx, f, id)
}

func (s *JSONLEventStore) saveCheckpointLocked(ctx context.Context, f *os.File, id string) error {
	checkpoint, err := s.loadRunsLocked(ctx, f, id)
	if err != nil || checkpoint.LastSequence == 0 {
		return err
	}
	info, err := f.Stat()
	if err != nil {
		return err
	}
	checkpoint.LogID, err = logIdentity(info)
	if err != nil {
		return err
	}
	boundary, hash, err := checkpointBoundary(ctx, f, checkpoint.ByteOffset)
	if err != nil {
		return err
	}
	if boundary.Sequence != checkpoint.LastSequence || boundary.ConversationID != id {
		return fmt.Errorf("checkpoint boundary is not the replayed prefix")
	}
	checkpoint.BoundaryHash = hash
	checkpoint.Checksum, err = checkpoint.digest()
	if err != nil {
		return err
	}
	data, err := json.Marshal(checkpoint)
	if err != nil {
		return err
	}
	path, err := s.path(id)
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(s.root, ".execution-checkpoint-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	defer tmp.Close()
	if err := tmp.Chmod(0o600); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if n, err := tmp.Write(data); err != nil {
		return err
	} else if n != len(data) {
		return io.ErrShortWrite
	}
	if err := tmp.Sync(); err != nil {
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path+".checkpoint.json"); err != nil {
		return err
	}
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}
