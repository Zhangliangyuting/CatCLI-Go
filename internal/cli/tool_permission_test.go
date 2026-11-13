package cli

import (
	"AgentCLI/internal/tool"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"
)

type permissionTestReader struct {
	answer string
	err    error
}

func (r permissionTestReader) ReadStringContext(context.Context, byte) (string, error) {
	return r.answer, r.err
}
func TestToolPermissionAnswers(t *testing.T) {
	for _, answer := range []string{"y\n", "yes\n", "n\n", "\n", "anything\n"} {
		var output bytes.Buffer
		approved, err := ToolPermissionConfirmer(permissionTestReader{answer: answer}, &output)(t.Context(), tool.PermissionRequest{Tool: "file\x1b[2J", Operation: "execute", Arguments: map[string]any{"content": "hello"}})
		want := answer == "y\n" || answer == "yes\n"
		if err != nil || approved != want {
			t.Fatalf("answer=%q approved=%v err=%v", answer, approved, err)
		}
		if strings.Contains(output.String(), "\x1b") {
			t.Fatal("terminal escape printed")
		}
		if !strings.Contains(output.String(), "hello") {
			t.Fatal("arguments not displayed")
		}
	}
	approved, err := ToolPermissionConfirmer(permissionTestReader{answer: "yes", err: io.EOF}, io.Discard)(t.Context(), tool.PermissionRequest{})
	if approved || !errors.Is(err, io.EOF) {
		t.Fatalf("EOF accepted: %v %v", approved, err)
	}
}
func TestTerminalApprovalCancellationDoesNotStealNextLine(t *testing.T) {
	input, output, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	defer output.Close()
	reader := NewTerminalReader(input)
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	if _, err := reader.ReadStringContext(ctx, '\n'); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("input cancellation: %v", err)
	}
	if _, err := io.WriteString(output, "next command\n"); err != nil {
		t.Fatal(err)
	}
	ctx2, cancel2 := context.WithTimeout(t.Context(), time.Second)
	defer cancel2()
	line, err := reader.ReadStringContext(ctx2, '\n')
	if err != nil || line != "next command\n" {
		t.Fatalf("line=%q err=%v", line, err)
	}
}
