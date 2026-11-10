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

func TestWriteFileVerification(t *testing.T) {
	root := t.TempDir()
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := NewToolRegistry()
	if err := r.RegisterEnabledTools([]Provider{NewBuiltinProvider(work)}, []string{"write_file"}); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, content string
		exists, match bool
	}{
		{"matching", "expected", true, true},
		{"empty", "", true, true},
		{"different", "changed!", true, false},
		{"longer", "expected extra", true, false},
		{"missing", "expected", false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(root, tc.name)
			if tc.exists {
				if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
					t.Fatal(err)
				}
			}
			want := "expected"
			if tc.name == "empty" {
				want = ""
			}
			check, err := r.Verify(t.Context(), "write_file", map[string]any{"file_path": tc.name, "content": want}, "")
			if err != nil || (check != nil) != tc.match {
				t.Fatalf("verification=%+v error=%v", check, err)
			}
			if check != nil && (!strings.Contains(check.Evidence, "sha256=") || !strings.Contains(check.Result, "no write performed")) {
				t.Fatalf("missing evidence: %+v", check)
			}
			data, err := os.ReadFile(path)
			if tc.exists && (err != nil || string(data) != tc.content) {
				t.Fatal("verification modified file")
			}
			if !tc.exists && !os.IsNotExist(err) {
				t.Fatal("verification created file")
			}
		})
	}
	// The verifier survives both provider registration paths and role subsets.
	all := NewToolRegistry()
	all.RegisterProvider(NewBuiltinProvider(work))
	check, err := all.Subset("write_file").Verify(t.Context(), "write_file", map[string]any{"file_path": "matching", "content": "expected"}, "")
	if err != nil || check == nil {
		t.Fatalf("lost verifier during registration/subset: %v", err)
	}

	outside := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(outside, []byte("expected"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{outside, "link", "../escape"} {
		if check, err := r.Verify(t.Context(), "write_file", map[string]any{"file_path": path, "content": "expected"}, ""); err == nil || check != nil {
			t.Fatalf("accepted escaped path %q", path)
		}
	}
	for _, args := range []map[string]any{nil, {"file_path": "matching"}, {"path": "matching", "content": "expected"}} {
		if _, err := r.Verify(t.Context(), "write_file", args, ""); err == nil {
			t.Fatal("accepted malformed arguments")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := r.Verify(ctx, "write_file", nil, ""); !errors.Is(err, context.Canceled) {
		t.Fatalf("ignored cancellation: %v", err)
	}
}

func TestCustomWriteFileDoesNotAcquireBuiltinVerification(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterTool(WriteFileDefinition(), func(context.Context, map[string]any) (string, error) {
		t.Fatal("executed during verification")
		return "", nil
	})
	for _, name := range []string{"write_file", "mcp__filesystem__write_file"} {
		if check, err := r.Verify(t.Context(), name, nil, ""); check != nil || err != nil {
			t.Fatalf("unexpected custom verifier: %+v %v", check, err)
		}
	}
}
