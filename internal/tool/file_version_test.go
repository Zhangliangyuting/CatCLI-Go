package tool

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileMutationsRequireCurrentReadHash(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("version A"), 0600); err != nil {
		t.Fatal(err)
	}
	read, err := ReadFileHandler(ctx, map[string]any{"path": path})
	if err != nil || !strings.Contains(read, "full_file_sha256="+contentHash([]byte("version A"))) {
		t.Fatalf("read did not return file version: %q %v", read, err)
	}
	oldHash := contentHash([]byte("version A"))
	if err := os.WriteFile(path, []byte("version B"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		run  func(string) error
	}{
		{"write_file", func(hash string) error {
			_, err := WriteFileHandler(ctx, map[string]any{"file_path": path, "content": "version C", "overwrite": true, "expected_hash": hash})
			return err
		}},
		{"edit_file", func(hash string) error {
			_, err := EditFileHandler(ctx, map[string]any{"file_path": path, "old_string": "version", "new_string": "changed", "expected_hash": hash})
			return err
		}},
		{"create_project", func(hash string) error {
			_, err := CreateProjectHandler(ctx, map[string]any{"root": root, "overwrite": true, "files": []any{map[string]any{"path": "file.txt", "content": "version C", "expected_hash": hash}}})
			return err
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run(""); err == nil || !strings.Contains(err.Error(), "expected_hash") {
				t.Fatalf("missing hash: %v", err)
			}
			if err := tc.run(oldHash); err == nil || !strings.Contains(err.Error(), "file changed since read") {
				t.Fatalf("stale hash: %v", err)
			}
			data, err := os.ReadFile(path)
			if err != nil || string(data) != "version B" {
				t.Fatalf("stale write changed target: %q %v", data, err)
			}
		})
	}
	if _, err := WriteFileHandler(ctx, map[string]any{"file_path": path, "content": "version C", "overwrite": true, "expected_hash": contentHash([]byte("version B"))}); err != nil {
		t.Fatalf("current hash rejected: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "version C" {
		t.Fatalf("current hash did not write: %q", data)
	}
}
