package tool

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

func TestFileToolsPublishCompleteReplacement(t *testing.T) {
	for _, name := range []string{"write_file", "edit_file", "create_project"} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
				t.Fatal(err)
			}
			old, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer old.Close()
			args := map[string]any{"file_path": path, "content": "new", "overwrite": true, "expected_hash": contentHash([]byte("old"))}
			switch name {
			case "write_file":
				_, err = WriteFileHandler(t.Context(), args)
			case "edit_file":
				args["old_string"], args["new_string"] = "old", "new"
				_, err = EditFileHandler(t.Context(), args)
			case "create_project":
				_, err = CreateProjectHandler(t.Context(), map[string]any{"root": root, "overwrite": true, "files": []any{
					map[string]any{"path": "file", "content": "new", "expected_hash": args["expected_hash"]},
				}})
			}
			if err != nil {
				t.Fatal(err)
			}
			if data, err := io.ReadAll(old); err != nil || string(data) != "old" {
				t.Fatalf("old handle changed: %q %v", data, err)
			}
			if data, err := os.ReadFile(path); err != nil || string(data) != "new" {
				t.Fatalf("new contents: %q %v", data, err)
			}
			info, err := os.Stat(path)
			if err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("permissions changed: %v %v", info, err)
			}
			assertNoWriteTemps(t, root)
		})
	}
}

// Cancel at the final context check, after the temporary file has been synced.
type cancelBeforePublish struct {
	context.Context
	checks int
}

func (c *cancelBeforePublish) Err() error {
	c.checks++
	if c.checks >= 2 {
		return context.Canceled
	}
	return nil
}

func TestAtomicWriteFailurePreservesTarget(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx := &cancelBeforePublish{Context: t.Context()}
	if err := atomicWriteFile(ctx, path, []byte("new"), false); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancellation: %v", err)
	}
	if err := atomicWriteFile(t.Context(), path, []byte("new"), true); !errors.Is(err, os.ErrExist) {
		t.Fatalf("create-only replaced existing target: %v", err)
	}
	if data, err := os.ReadFile(path); err != nil || string(data) != "old" {
		t.Fatalf("target changed on failure: %q %v", data, err)
	}
	assertNoWriteTemps(t, root)
}

func assertNoWriteTemps(t *testing.T, root string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".catcli-write-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("temporary files left behind: %v %v", matches, err)
	}
}
