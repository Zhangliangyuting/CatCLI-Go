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

func TestEditFileHandlerRejectsConcurrentStaleVersions(t *testing.T) {
	const edits = 32

	filePath := filepath.Join(t.TempDir(), "shared.txt")
	parts := make([]string, edits)
	for i := range edits {
		parts[i] = fmt.Sprintf("value_%02d", i)
	}
	initial := strings.Join(parts, "\n")
	if err := os.WriteFile(filePath, []byte(initial), 0644); err != nil {
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
				"file_path":     filePath,
				"old_string":    oldValue,
				"new_string":    "updated_" + oldValue,
				"expected_hash": contentHash([]byte(initial)),
			})
			errs <- err
		}(i)
	}

	close(start)
	workers.Wait()
	close(errs)
	succeeded := 0
	for err := range errs {
		if err == nil {
			succeeded++
		} else if !strings.Contains(err.Error(), "file changed since read") {
			t.Fatalf("unexpected edit error = %v", err)
		}
	}
	if succeeded != 1 {
		t.Fatalf("%d edits succeeded with the same expected hash, want 1", succeeded)
	}

	content, err := os.ReadFile(filePath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if strings.Count(string(content), "updated_value_") != 1 {
		t.Fatalf("unexpected final content: %q", content)
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
