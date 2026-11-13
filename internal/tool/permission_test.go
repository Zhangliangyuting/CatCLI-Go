package tool

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"AgentCLI/internal/workspace"
)

func TestPermissionGate(t *testing.T) {
	for _, action := range []PermissionAction{Allow, Ask, Deny, "invalid", ""} {
		for _, approve := range []bool{false, true} {
			t.Run(string(action)+map[bool]string{false: "/decline", true: "/approve"}[approve], func(t *testing.T) {
				registry := NewToolRegistry()
				calls, prompts := 0, 0
				candidate := Tool{Definition: Definition{FunctionDefinition: FunctionDefinition{Name: "test"}}, Handler: func(context.Context, map[string]any) (string, error) { calls++; return "done", nil }}
				candidate.Permission = action
				registry.register(candidate)
				registry.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { prompts++; return approve, nil })
				_, err := registry.Execute(t.Context(), "test", nil)
				allowed := action == Allow || ((action == Ask || action == "") && approve)
				if allowed != (err == nil) || allowed != (calls == 1) {
					t.Fatalf("allowed=%v calls=%d error=%v", allowed, calls, err)
				}
				if !allowed && !errors.Is(err, ErrPermissionDenied) {
					t.Fatal(err)
				}
				if (action == Ask || action == "") != (prompts == 1) {
					t.Fatalf("prompts=%d", prompts)
				}
			})
		}
	}
}
func TestStaticToolPermission(t *testing.T) {
	for _, action := range []PermissionAction{Allow, Ask, Deny} {
		r := NewToolRegistry()
		calls, prompts := 0, 0
		r.register(Tool{Definition: Definition{FunctionDefinition: FunctionDefinition{Name: "test"}}, Permission: action, Handler: func(context.Context, map[string]any) (string, error) { calls++; return "ok", nil }})
		r.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { prompts++; return true, nil })
		_, err := r.Execute(t.Context(), "test", nil)
		wantCalls, wantPrompts := 0, 0
		if action == Allow {
			wantCalls = 1
		}
		if action == Ask {
			wantCalls, wantPrompts = 1, 1
		}
		if calls != wantCalls || prompts != wantPrompts {
			t.Fatalf("action=%s calls=%d prompts=%d", action, calls, prompts)
		}
		if action == Deny && !errors.Is(err, ErrPermissionDenied) {
			t.Fatalf("deny error=%v", err)
		}
		if action != Deny && err != nil {
			t.Fatal(err)
		}
	}
	// An omitted field is the safe default: ask, then execute only after approval.
	r := newApprovedTestRegistry()
	called := false
	r.RegisterTool(Definition{FunctionDefinition: FunctionDefinition{Name: "default"}}, func(context.Context, map[string]any) (string, error) { called = true; return "", nil })
	if _, err := r.Execute(t.Context(), "default", nil); err != nil || !called {
		t.Fatalf("default ask err=%v called=%v", err, called)
	}
}
func TestPermissionDefaultAndVerification(t *testing.T) {
	r := NewToolRegistry()
	invoked := false
	r.register(Tool{Definition: Definition{FunctionDefinition: FunctionDefinition{Name: "test"}}, Handler: func(context.Context, map[string]any) (string, error) { invoked = true; return "", nil }, Verify: func(context.Context, map[string]any, string) (*Verification, error) { invoked = true; return nil, nil }, PrepareVerification: func(context.Context, map[string]any) (string, error) { invoked = true; return "", nil }})
	r.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { return false, nil })
	_, executeErr := r.Execute(t.Context(), "test", nil)
	_, verifyErr := r.Verify(t.Context(), "test", nil, "")
	if !errors.Is(executeErr, ErrPermissionDenied) || verifyErr != nil {
		t.Fatalf("execute=%v verify=%v", executeErr, verifyErr)
	}
	if !invoked {
		t.Fatal("verification should run directly; it does not execute the tool")
	}
}

func TestAuthorizedCallPreparesEvidenceWithoutSecondPrompt(t *testing.T) {
	r := NewToolRegistry()
	prepared, executed, prompts := 0, 0, 0
	r.register(Tool{
		Definition:          Definition{FunctionDefinition: FunctionDefinition{Name: "edit"}},
		Permission:          Ask,
		Handler:             func(context.Context, map[string]any) (string, error) { executed++; return "ok", nil },
		PrepareVerification: func(context.Context, map[string]any) (string, error) { prepared++; return "proof", nil },
	})
	r.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { prompts++; return true, nil })
	approved, err := r.Authorize(t.Context(), "edit", map[string]any{"path": "file"})
	if err != nil {
		t.Fatal(err)
	}
	proof, err := approved.PrepareVerification(t.Context())
	if err != nil || proof != "proof" {
		t.Fatalf("proof=%q err=%v", proof, err)
	}
	if _, err := approved.Execute(t.Context()); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 || prepared != 1 || executed != 1 {
		t.Fatalf("prompts=%d prepared=%d executed=%d", prompts, prepared, executed)
	}
}
func TestPermissionSubsetAndArguments(t *testing.T) {
	r := NewToolRegistry()
	r.RegisterTool(Definition{FunctionDefinition: FunctionDefinition{Name: "test"}}, func(_ context.Context, args map[string]any) (string, error) {
		if args["nested"].(map[string]any)["value"] != "original" {
			t.Fatal("approval mutated execution arguments")
		}
		return "ok", nil
	})
	subset := r.Subset("test")
	r.SetPermissionConfirmer(func(_ context.Context, request PermissionRequest) (bool, error) {
		request.Arguments["nested"].(map[string]any)["value"] = "changed"
		return true, nil
	})
	if _, err := subset.Execute(t.Context(), "test", map[string]any{"nested": map[string]any{"value": "original"}}); err != nil {
		t.Fatal(err)
	}
}
func TestPermissionPromptsSerializeAndCancel(t *testing.T) {
	r := NewToolRegistry()
	var calls atomic.Int32
	r.RegisterTool(Definition{FunctionDefinition: FunctionDefinition{Name: "test"}}, func(context.Context, map[string]any) (string, error) { calls.Add(1); return "", nil })
	entered := make(chan struct{}, 2)
	release := make(chan struct{})
	r.SetPermissionConfirmer(func(ctx context.Context, _ PermissionRequest) (bool, error) {
		entered <- struct{}{}
		select {
		case <-release:
			return true, nil
		case <-ctx.Done():
			return false, ctx.Err()
		}
	})
	var wg sync.WaitGroup
	wg.Go(func() { _, _ = r.Execute(t.Context(), "test", nil) })
	<-entered
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { _, err := r.Subset("test").Execute(ctx, "test", nil); done <- err }()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("queued approval did not cancel")
	}
	select {
	case <-entered:
		t.Fatal("overlapping prompts")
	default:
	}
	close(release)
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal(calls.Load())
	}
}
func TestBuiltinPermissionBoundaries(t *testing.T) {
	root := t.TempDir()
	ws, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	r := NewToolRegistry()
	r.RegisterProvider(NewBuiltinProvider(ws))
	prompts := 0
	r.SetPermissionConfirmer(func(context.Context, PermissionRequest) (bool, error) { prompts++; return true, nil })
	_, err = r.Execute(t.Context(), "read_file", map[string]any{"path": "../outside"})
	if err == nil || prompts != 0 {
		t.Fatalf("err=%v prompts=%d", err, prompts)
	}
	_, err = r.Execute(t.Context(), "execute_command", map[string]any{"command": "rm -rf anything"})
	if err == nil || prompts != 1 {
		t.Fatalf("err=%v prompts=%d", err, prompts)
	}
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("hello"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Execute(t.Context(), "read_file", map[string]any{"path": "file"}); err != nil {
		t.Fatal(err)
	}
	if prompts != 1 {
		t.Fatal("workspace observation prompted")
	}
	// A static denial never reaches the handler, even with an approving confirmer.
	r.register(Tool{Definition: Definition{FunctionDefinition: FunctionDefinition{Name: "denied"}}, Permission: Deny, Handler: func(context.Context, map[string]any) (string, error) { t.Fatal("denied tool executed"); return "", nil }})
	if _, err := r.Execute(t.Context(), "denied", nil); !errors.Is(err, ErrPermissionDenied) {
		t.Fatal(err)
	}
}
