package tool

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFileHandlerReadsUTF8Text(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "text.txt")
	want := "你好，CatCLI\n"
	if err := os.WriteFile(filePath, []byte(want), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := ReadFileHandler(context.Background(), map[string]interface{}{
		"path": filePath,
	})
	if err != nil {
		t.Fatalf("ReadFileHandler() error = %v", err)
	}
	if got != want {
		t.Fatalf("ReadFileHandler() = %q, want %q", got, want)
	}
}

func TestReadFileHandlerRejectsBinaryFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "program")
	if err := os.WriteFile(filePath, []byte{0x7f, 'E', 'L', 'F', 0x00, 0x01}, 0755); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ReadFileHandler(context.Background(), map[string]interface{}{
		"path": filePath,
	})
	if err == nil || !strings.Contains(err.Error(), "binary file is not supported") {
		t.Fatalf("ReadFileHandler() error = %v, want binary-file error", err)
	}
}

func TestReadFileHandlerRejectsInvalidUTF8(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "invalid.txt")
	if err := os.WriteFile(filePath, []byte{0xff, 0xfe, 0xfd}, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ReadFileHandler(context.Background(), map[string]interface{}{
		"path": filePath,
	})
	if err == nil || !strings.Contains(err.Error(), "binary file is not supported") {
		t.Fatalf("ReadFileHandler() error = %v, want binary-file error", err)
	}
}

func TestReadFileHandlerRejectsOversizedFile(t *testing.T) {
	filePath := filepath.Join(t.TempDir(), "large.txt")
	data := bytes.Repeat([]byte("a"), int(maxReadFileBytes)+1)
	if err := os.WriteFile(filePath, data, 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, err := ReadFileHandler(context.Background(), map[string]interface{}{
		"path": filePath,
	})
	if err == nil || !strings.Contains(err.Error(), "file is too large") {
		t.Fatalf("ReadFileHandler() error = %v, want file-size error", err)
	}
}
