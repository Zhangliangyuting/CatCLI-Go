package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sync"
	"time"
)

type StdioTransport struct {
	config    ServerConfig
	stderr    io.Writer
	cmd       *exec.Cmd
	stdin     io.WriteCloser
	stdout    io.ReadCloser
	writeMu   sync.Mutex
	closeOnce sync.Once
	done      chan struct{}
}

func NewStdioTransport(config ServerConfig, stderr io.Writer) *StdioTransport {
	return &StdioTransport{config: config, stderr: stderr, done: make(chan struct{})}
}

func (t *StdioTransport) Start(receive func(Message), failed func(error)) error {
	t.cmd = exec.Command(t.config.Command, t.config.Args...)
	t.cmd.WaitDelay = time.Second
	t.cmd.Dir, t.cmd.Stderr = t.config.Directory, t.stderr
	environment := make(map[string]string)
	for _, value := range os.Environ() {
		for i := range value {
			if value[i] == '=' {
				environment[value[:i]] = value[i+1:]
				break
			}
		}
	}
	for key, value := range t.config.Env {
		environment[key] = value
	}
	for key, value := range environment {
		t.cmd.Env = append(t.cmd.Env, key+"="+value)
	}
	stdin, err := t.cmd.StdinPipe()
	if err != nil {
		return err
	}
	t.stdin = stdin
	stdout, err := t.cmd.StdoutPipe()
	if err != nil {
		_ = stdin.Close()
		return err
	}
	t.stdout = stdout
	if err := t.cmd.Start(); err != nil {
		_ = stdin.Close()
		_ = stdout.Close()
		return fmt.Errorf("start MCP subprocess: %w", err)
	}
	go func() {
		defer close(t.done)
		scanner := bufio.NewScanner(stdout)
		scanner.Buffer(make([]byte, 4096), maxMessageBytes)
		for scanner.Scan() {
			message, err := decodeMessage(scanner.Bytes())
			if err != nil {
				failed(err)
				_ = t.cmd.Process.Kill()
				break
			}
			receive(message)
		}
		if err := scanner.Err(); err != nil {
			failed(fmt.Errorf("read MCP stdout: %w", err))
		} else {
			failed(io.EOF)
		}
		_ = stdout.Close()
		_ = t.cmd.Wait()
	}()
	return nil
}

func (t *StdioTransport) Send(ctx context.Context, message Message) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := json.Marshal(message)
	if err != nil {
		return err
	}
	if len(data) > maxMessageBytes {
		return fmt.Errorf("MCP message too large")
	}
	result := make(chan error, 1)
	go func() {
		t.writeMu.Lock()
		defer t.writeMu.Unlock()
		if err := ctx.Err(); err != nil {
			result <- err
			return
		}
		_, err := t.stdin.Write(append(data, '\n'))
		result <- err
	}()
	select {
	case err := <-result:
		return err
	case <-ctx.Done():
		// Closing the pipe also unblocks a subprocess that stopped reading stdin.
		_ = t.Close()
		return ctx.Err()
	}
}

func (t *StdioTransport) Close() error {
	t.closeOnce.Do(func() {
		if t.stdin != nil {
			_ = t.stdin.Close()
		}
		if t.cmd == nil || t.cmd.Process == nil {
			return
		}
		timer := time.NewTimer(time.Second)
		defer timer.Stop()
		select {
		case <-t.done:
		case <-timer.C:
			_ = t.cmd.Process.Kill()
			_ = t.stdout.Close()
			<-t.done
		}
	})
	return nil
}
