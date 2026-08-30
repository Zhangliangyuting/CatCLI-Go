package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestEditFileHandlerSerializesReadModifyWriteByPath(t *testing.T) {
	const edits = 32

	filePath := filepath.Join(t.TempDir(), "shared.txt")
	parts := make([]string, edits)
	for i := range edits {
		parts[i] = fmt.Sprintf("value_%02d", i)
	}
	if err := os.WriteFile(filePath, []byte(strings.Join(parts, "\n")), 0644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	start := make(chan struct{})
	var workers sync.WaitGroup
	errs := make(chan error, edits)
	for i := range edits {
		workers.Add(1)
		go func(index int) {
			defer workers.Done()
			<-start
			oldValue := fmt.Sprintf("value_%02d", index)
			_, err := EditFileHandler(context.Background(), map[string]interface{}{
				"file_path":  filePath,
				"old_string": oldValue,
				"new_string": "updated_" + oldValue,
			})
			errs <- err
		}(i)
	}

	close(start)
	workers.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("EditFileHandler() error = %v", err)
		}
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	for _, oldValue := range parts {
		if !strings.Contains(string(content), "updated_"+oldValue) {
			t.Fatalf("final content lost edit for %q", oldValue)
		}
	}
}

func TestCanonicalFilePathMatchesEquivalentPaths(t *testing.T) {
	directory := t.TempDir()
	plain := filepath.Join(directory, "file.txt")
	equivalent := filepath.Join(directory, ".", "file.txt")
	if canonicalFilePath(plain) != canonicalFilePath(equivalent) {
		t.Fatalf("equivalent paths have different lock keys")
	}
}

func TestCanonicalFilePathResolvesSymlinkedParentForNewFile(t *testing.T) {
	directory := t.TempDir()
	realDirectory := filepath.Join(directory, "real")
	if err := os.Mkdir(realDirectory, 0755); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	symlinkDirectory := filepath.Join(directory, "link")
	if err := os.Symlink(realDirectory, symlinkDirectory); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}

	realPath := filepath.Join(realDirectory, "new.txt")
	symlinkPath := filepath.Join(symlinkDirectory, "new.txt")
	if canonicalFilePath(realPath) != canonicalFilePath(symlinkPath) {
		t.Fatal("symlinked parent produced a different lock key")
	}
}
