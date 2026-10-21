package workspace

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestWorkspaceResolvesPathsInsideRoot(t *testing.T) {
	root := t.TempDir()
	workspace, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	resolved, err := workspace.Resolve(filepath.Join("internal", "new.go"))
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	want := filepath.Join(root, "internal", "new.go")
	if resolved != want {
		t.Fatalf("Resolve() = %q, want %q", resolved, want)
	}
	if workspace.MetadataDir() != filepath.Join(root, ".catcli") {
		t.Fatalf("MetadataDir() = %q", workspace.MetadataDir())
	}
}

func TestWorkspaceRejectsPathsOutsideRoot(t *testing.T) {
	root := t.TempDir()
	workspace, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	for _, path := range []string{
		filepath.Join("..", "outside.txt"),
		filepath.Join(filepath.Dir(root), "outside.txt"),
	} {
		if _, err := workspace.Resolve(path); err == nil {
			t.Fatalf("Resolve(%q) error = nil, want workspace escape error", path)
		}
	}
}

func TestWorkspaceRejectsSymlinkEscape(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation commonly requires elevated Windows privileges")
	}
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "outside-link")); err != nil {
		t.Fatalf("Symlink() error = %v", err)
	}
	workspace, err := New(root)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	if _, err := workspace.Resolve(filepath.Join("outside-link", "secret.txt")); err == nil {
		t.Fatal("Resolve() error = nil, want symlink escape error")
	}
}

func TestWorkspaceRejectsInvalidRoot(t *testing.T) {
	if _, err := New(""); err == nil {
		t.Fatal("New(empty) error = nil")
	}
	if _, err := New(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("New(missing) error = nil")
	}
}
