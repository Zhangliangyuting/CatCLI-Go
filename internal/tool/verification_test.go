package tool

import (
	"AgentCLI/internal/workspace"
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func verificationRegistry(t *testing.T) (*ToolRegistry, string) {
	t.Helper()
	root := t.TempDir()
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := newApprovedTestRegistry()
	r.RegisterProvider(NewBuiltinProvider(work))
	return r, root
}

func TestReadAndCommandVerificationRequiresFreshExecution(t *testing.T) {
	r, root := verificationRegistry(t)
	for _, name := range []string{"read_file", "list_dir", "execute_command"} {
		args := map[string]any{"path": "does-not-exist", "command": "touch " + filepath.Join(root, "marker")}
		check, err := r.Verify(t.Context(), name, args, "")
		if err != nil || check == nil || !check.Retry || check.Evidence == "" || check.Result != "" {
			t.Fatalf("%s verification=%+v error=%v", name, check, err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if _, err := r.Verify(ctx, name, args, ""); !errors.Is(err, context.Canceled) {
			t.Fatal("ignored cancellation")
		}
	}
	if _, err := os.Stat(filepath.Join(root, "marker")); !os.IsNotExist(err) {
		t.Fatal("verification executed command")
	}
}

func TestEditFileVerificationUsesPreparedFullFileHash(t *testing.T) {
	for _, tc := range []struct {
		name, before, old, replacement, expected string
		all                                      bool
	}{
		{"single", "header old tail", "old", "new", "header new tail", false},
		{"all", "old old", "old", "new", "new new", true},
		{"delete", "before old after", "old", "", "before  after", false},
		{"overlap", "old tail", "old", "old new", "old new tail", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, root := verificationRegistry(t)
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte(tc.before), 0600); err != nil {
				t.Fatal(err)
			}
			args := map[string]any{"file_path": "file", "old_string": tc.old, "new_string": tc.replacement, "replace_all": tc.all, "expected_hash": contentHash([]byte(tc.before))}
			approved, err := r.Authorize(t.Context(), "edit_file", args)
			if err != nil {
				t.Fatal(err)
			}
			proof, err := approved.PrepareVerification(t.Context())
			if err != nil || proof == "" {
				t.Fatalf("prepare: %q %v", proof, err)
			}
			if tc.before != tc.expected {
				if check, err := r.Verify(t.Context(), "edit_file", args, proof); err != nil || check != nil {
					t.Fatalf("unedited file accepted: %+v %v", check, err)
				}
			}
			if _, err := approved.Execute(t.Context()); err != nil {
				t.Fatal(err)
			}
			check, err := r.Verify(t.Context(), "edit_file", args, proof)
			if err != nil || check == nil || check.Retry || !strings.Contains(check.Evidence, "after_sha256=") {
				t.Fatalf("edit verification: %+v %v", check, err)
			}
			data, _ := os.ReadFile(path)
			if string(data) != tc.expected {
				t.Fatalf("verification changed content: %q", data)
			}
			for _, missing := range []string{"", "{", `{}`} {
				if check, _ := r.Verify(t.Context(), "edit_file", args, missing); check != nil {
					t.Fatal("accepted absent or malformed evidence")
				}
			}
			other := cloneArguments(args)
			other["new_string"] = "different"
			if check, _ := r.Verify(t.Context(), "edit_file", other, proof); check != nil {
				t.Fatal("accepted evidence for different arguments")
			}
			if err := os.WriteFile(path, []byte(tc.expected+" unrelated change"), 0600); err != nil {
				t.Fatal(err)
			}
			if check, err := r.Verify(t.Context(), "edit_file", args, proof); err != nil || check != nil {
				t.Fatal("accepted only a matching substring")
			}
		})
	}
}

func TestEditVerificationRejectsInvalidPreparationAndEscape(t *testing.T) {
	r, root := verificationRegistry(t)
	if err := os.WriteFile(filepath.Join(root, "file"), []byte("old old"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, args := range []map[string]any{
		{"file_path": "file", "old_string": "old", "new_string": "new"}, // ambiguous single replacement
		{"file_path": "../outside", "old_string": "old", "new_string": "new"},
		{"file_path": "file", "old_string": "missing", "new_string": "new"},
		{"file_path": "file", "old_string": ""},
	} {
		approved, err := r.Authorize(t.Context(), "edit_file", args)
		proof := ""
		if err == nil {
			proof, err = approved.PrepareVerification(t.Context())
		}
		if err == nil {
			t.Fatalf("invalid preparation accepted: %q %v", proof, err)
		}
	}
}

func TestCreateProjectVerificationChecksEveryFileWithoutWriting(t *testing.T) {
	r, root := verificationRegistry(t)
	args := map[string]any{"root": "project", "files": []any{
		map[string]any{"path": "a.txt", "content": "a"},
		map[string]any{"path": "sub/b.txt", "content": ""},
	}}
	if check, err := r.Verify(t.Context(), "create_project", args, ""); err != nil || check != nil {
		t.Fatal("accepted absent project")
	}
	if _, err := os.Stat(filepath.Join(root, "project")); !os.IsNotExist(err) {
		t.Fatal("verification created directory")
	}
	if err := os.MkdirAll(filepath.Join(root, "project"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project/a.txt"), []byte("a"), 0600); err != nil {
		t.Fatal(err)
	}
	if check, err := r.Verify(t.Context(), "create_project", args, ""); err != nil || check != nil {
		t.Fatal("accepted partial project")
	}
	if err := os.MkdirAll(filepath.Join(root, "project/sub"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "project/sub/b.txt"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	check, err := r.Verify(t.Context(), "create_project", args, "")
	if err != nil || check == nil || check.Retry || !strings.Contains(check.Result, "all 2 requested") {
		t.Fatalf("project verification: %+v %v", check, err)
	}
	if err := os.WriteFile(filepath.Join(root, "project/a.txt"), []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	if check, err := r.Verify(t.Context(), "create_project", args, ""); err != nil || check != nil {
		t.Fatal("accepted changed project")
	}
	for _, path := range []string{"../escape", filepath.Join(root, "absolute")} {
		bad := map[string]any{"root": "project", "files": []any{map[string]any{"path": path, "content": "x"}}}
		if check, err := r.Verify(t.Context(), "create_project", bad, ""); err == nil || check != nil {
			t.Fatal("accepted escaping project file")
		}
	}
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "x"), []byte("x"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "project/link")); err != nil {
		t.Fatal(err)
	}
	bad := map[string]any{"root": "project", "files": []any{map[string]any{"path": "link/x", "content": "x"}}}
	if check, err := r.Verify(t.Context(), "create_project", bad, ""); err == nil || check != nil {
		t.Fatal("accepted symlink escape")
	}
}
