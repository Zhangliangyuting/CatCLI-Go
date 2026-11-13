package cli

import (
	"bufio"
	"context"
	"errors"
	"os"
	"sync"

	"golang.org/x/sys/unix"
)

// TerminalReader has a single synchronous owner of stdin. Polling lets Ctrl-C
// cancel an approval without leaving a background reader stealing future input.
// CatCLI's file identity and crash recovery already target Unix systems.
type TerminalReader struct {
	mu     sync.Mutex
	reader *bufio.Reader
	source *terminalSource
}
type terminalSource struct {
	file *os.File
	ctx  context.Context
}

func NewTerminalReader(file *os.File) *TerminalReader {
	source := &terminalSource{file: file, ctx: context.Background()}
	return &TerminalReader{reader: bufio.NewReader(source), source: source}
}
func (r *TerminalReader) ReadString(delimiter byte) (string, error) {
	return r.ReadStringContext(context.Background(), delimiter)
}
func (r *TerminalReader) ReadStringContext(ctx context.Context, delimiter byte) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	r.source.ctx = ctx
	defer func() { r.source.ctx = context.Background() }()
	return r.reader.ReadString(delimiter)
}
func (s *terminalSource) Read(data []byte) (int, error) {
	for {
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		descriptors := []unix.PollFd{{Fd: int32(s.file.Fd()), Events: unix.POLLIN}}
		n, err := unix.Poll(descriptors, 100)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return 0, err
		}
		if n == 0 {
			continue
		}
		if err := s.ctx.Err(); err != nil {
			return 0, err
		}
		return s.file.Read(data)
	}
}
