package tool

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCreateProjectResumesPartialCreation(t *testing.T) {
	root := t.TempDir()
	blocked := filepath.Join(root, "b.txt")
	if err := os.WriteFile(blocked, []byte("unrelated"), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"root": root, "files": []any{
		map[string]any{"path": "a.txt", "content": "a"},
		map[string]any{"path": "b.txt", "content": "b"},
		map[string]any{"path": "nested/empty.txt", "content": ""},
	}}
	if _, err := CreateProjectHandler(t.Context(), args); err == nil {
		t.Fatal("overwrote conflicting file without permission")
	}
	first := filepath.Join(root, "a.txt")
	stamp := time.Unix(1000000000, 0)
	if err := os.Chtimes(first, stamp, stamp); err != nil {
		t.Fatal(err)
	}
	if data, err := os.ReadFile(blocked); err != nil || string(data) != "unrelated" {
		t.Fatalf("conflicting file changed: %q %v", data, err)
	}
	if err := os.Remove(blocked); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := CreateProjectHandler(t.Context(), args); err != nil {
			t.Fatal(err)
		}
	}
	info, err := os.Stat(first)
	if err != nil || !info.ModTime().Equal(stamp) {
		t.Fatalf("completed file was rewritten: %v %v", info, err)
	}
	for name, want := range map[string]string{"a.txt": "a", "b.txt": "b", "nested/empty.txt": ""} {
		if data, err := os.ReadFile(filepath.Join(root, name)); err != nil || string(data) != want {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
}

func TestCreateProjectRetryAfterOverwrite(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("old"), 0600); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"root": root, "overwrite": true, "files": []any{
		map[string]any{"path": "file", "content": "new", "expected_hash": contentHash([]byte("old"))},
	}}
	for range 2 {
		if _, err := CreateProjectHandler(t.Context(), args); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(path, []byte("someone else's change"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := CreateProjectHandler(t.Context(), args); err == nil || !strings.Contains(err.Error(), "file changed since read") {
		t.Fatalf("expected version conflict: %v", err)
	}
	if data, _ := os.ReadFile(path); string(data) != "someone else's change" {
		t.Fatalf("conflict overwritten: %q", data)
	}
}
