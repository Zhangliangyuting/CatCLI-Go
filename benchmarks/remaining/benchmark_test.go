package remaining

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/memory"
	"AgentCLI/internal/tool"
	"AgentCLI/internal/workspace"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

// These live evaluations are opt-in to keep ordinary test runs deterministic
// and free of model API charges. Every result is checked against disk or the
// execution/permission records, independently of the model's own answer.
func TestWriteFileBenchmark(t *testing.T) {
	client := liveClient(t)
	for _, tc := range []struct {
		name, prompt, path, want string
		initial                  string
	}{
		{name: "create", prompt: "Create notes.txt with exactly this one line: release: cobalt-17", path: "notes.txt", want: "release: cobalt-17"},
		{name: "overwrite_after_read", prompt: "Replace the contents of notes.txt with exactly: release: cobalt-18. Read the existing file first and preserve the expected version check.", path: "notes.txt", initial: "release: cobalt-17\n", want: "release: cobalt-18"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := t.TempDir()
			if tc.initial != "" {
				mustWrite(t, root, tc.path, tc.initial)
			}
			trace, answer, err := runLive(t, client, root, []string{"read_file", "write_file"}, tc.prompt, approveOnly("write_file"), nil)
			got, readErr := os.ReadFile(filepath.Join(root, tc.path))
			if err != nil {
				t.Fatalf("agent run: %v", err)
			}
			if readErr != nil || strings.TrimSuffix(string(got), "\n") != tc.want {
				t.Fatalf("file=%q readErr=%v; want %q; answer=%q", got, readErr, tc.want, answer)
			}
			if !trace.hasTool("write_file") {
				t.Errorf("write_file was not called: %v", trace.tools)
			}
			t.Logf("answer=%q %s", answer, trace.summary())
		})
	}
}

func TestEditFileBenchmark(t *testing.T) {
	client := liveClient(t)
	root := t.TempDir()
	mustWrite(t, root, "service.yaml", "service: cobalt\nport: 8142\n")
	trace, answer, err := runLive(t, client, root, []string{"read_file", "edit_file"},
		"In service.yaml change only the port from 8142 to 8143. Read it first and use its current full-file hash.", approveOnly("edit_file"), nil)
	got, readErr := os.ReadFile(filepath.Join(root, "service.yaml"))
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	if readErr != nil || string(got) != "service: cobalt\nport: 8143\n" {
		t.Fatalf("file=%q readErr=%v; answer=%q", got, readErr, answer)
	}
	if !trace.hasTool("edit_file") {
		t.Errorf("edit_file was not called: %v", trace.tools)
	}
	t.Logf("answer=%q %s", answer, trace.summary())
}

func TestCreateProjectBenchmark(t *testing.T) {
	client := liveClient(t)
	root := t.TempDir()
	mustWrite(t, root, "cobalt/README.md", "# Cobalt\n") // partially completed project
	prompt := `Complete project cobalt with these files: README.md containing a level-one heading "Cobalt", config.yaml setting port to 8031, and src/main.go declaring package main. Keep the existing matching README and create the two missing files.`
	trace, answer, err := runLive(t, client, root, []string{"read_file", "create_project"}, prompt, approveOnly("create_project"), nil)
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	for path, want := range map[string]string{"cobalt/README.md": "# Cobalt\n", "cobalt/config.yaml": "port: 8031\n", "cobalt/src/main.go": "package main\n"} {
		got, readErr := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if readErr != nil || string(got) != want {
			t.Errorf("%s=%q err=%v want %q", path, got, readErr, want)
		}
	}
	if !trace.hasTool("create_project") {
		t.Errorf("create_project was not called: %v", trace.tools)
	}
	t.Logf("answer=%q %s", answer, trace.summary())
}

func TestCommandExecutionBenchmark(t *testing.T) {
	client := liveClient(t)
	root := t.TempDir()
	var requested []string
	confirm := func(_ context.Context, request tool.PermissionRequest) (bool, error) {
		if command, _ := request.Arguments["command"].(string); request.Tool == "execute_command" {
			requested = append(requested, command)
			return command == "pwd", nil
		}
		return false, nil
	}
	trace, answer, err := runLive(t, client, root, []string{"execute_command"}, "Use execute_command to run exactly pwd and tell me the working directory.", confirm, nil)
	if err != nil {
		t.Fatalf("agent run: %v", err)
	}
	if len(requested) == 0 {
		t.Fatal("no command permission request was observed")
	}
	if !contains(requested, "pwd") {
		t.Errorf("pwd was not requested: %v", requested)
	}
	if !strings.Contains(answer, root) {
		t.Errorf("answer %q does not report workspace %q", answer, root)
	}
	t.Logf("approved_commands=%v %s", requested, trace.summary())
}

func TestInterruptedRecoveryBenchmark(t *testing.T) {
	client := liveClient(t)
	root := t.TempDir()
	mustWrite(t, root, "status.txt", "state: before-crash\n")
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(tool.NewBuiltinProvider(work))
	store, err := execution.NewJSONLEventStore(filepath.Join(t.TempDir(), "events"))
	if err != nil {
		t.Fatal(err)
	}
	const conversation = "recovery-benchmark"
	recorder := execution.NewRecorder(store, conversation)
	run, err := recorder.StartRun(t.Context(), "Read status.txt and report its state", "", execution.ReActRun)
	if err != nil {
		t.Fatal(err)
	}
	_, err = run.StartToolCall(t.Context(), execution.ToolCallEventData{ToolCallID: "crashed-call", ToolName: "read_file", Arguments: `{"path":"status.txt"}`})
	if err != nil {
		t.Fatal(err)
	}
	// Deliberately leave the started call without a result to model a crash.
	if err := os.WriteFile(filepath.Join(root, "status.txt"), []byte("state: after-crash\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	events, err := store.Load(t.Context(), conversation, 0)
	if err != nil {
		t.Fatal(err)
	}
	runs, err := execution.Replay(events)
	if err != nil {
		t.Fatal(err)
	}
	latest := runs[len(runs)-1]
	if latest.Status != execution.Interrupted || len(latest.Calls) != 1 || latest.Calls[0].Status != execution.Unknown {
		t.Fatalf("fixture did not replay as an interrupted unknown call: %+v", latest)
	}
	trace := liveTrace{}
	runner := agent.NewReActAgent(client, registry, agent.WithRecorder(recorder), agent.WithSystemPrompt("Continue the interrupted task. Treat old tool records as history only; re-read the current workspace before answering."))
	ctx, cancel := context.WithTimeout(agent.WithObserver(t.Context(), trace.observe), 2*time.Minute)
	defer cancel()
	started := time.Now()
	answer, err := runner.Resume(ctx, latest)
	trace.elapsed = time.Since(started)
	if err != nil {
		t.Fatalf("resume: %v", err)
	}
	if !strings.Contains(answer, "after-crash") {
		t.Errorf("answer %q did not use current state", answer)
	}
	if !trace.hasTool("read_file") {
		t.Errorf("resume did not re-read: %v", trace.tools)
	}
	updatedEvents, err := store.Load(t.Context(), conversation, 0)
	if err != nil {
		t.Fatal(err)
	}
	updatedRuns, err := execution.Replay(updatedEvents)
	if err != nil {
		t.Fatal(err)
	}
	if updatedRuns[0].Status != execution.Resumed || updatedRuns[len(updatedRuns)-1].Status != execution.Completed {
		t.Errorf("recovery run states = %+v", updatedRuns)
	}
	t.Logf("answer=%q %s", answer, trace.summary())
}

func TestFactLifecycleBenchmark(t *testing.T) {
	client := liveClient(t)
	extractor, err := memory.NewLLMFactExtractor(client)
	if err != nil {
		t.Fatal(err)
	}
	manager := memory.NewManager(nil)
	retriever := memory.NewMemoryRetriever(nil)
	builder := memory.NewContextBuilder(manager, retriever, 2048)
	root := t.TempDir()
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(tool.NewBuiltinProvider(work))
	registry = registry.ReadOnlySubset()
	store, err := execution.NewJSONLEventStore(filepath.Join(t.TempDir(), "events"))
	if err != nil {
		t.Fatal(err)
	}
	trace := liveTrace{}
	runner := agent.NewReActAgent(client, registry,
		agent.WithRecorder(execution.NewRecorder(store, "fact-benchmark")), agent.WithMemoryManager(manager), agent.WithContextBuilder(builder),
		agent.WithSystemPrompt("Answer from stored facts or current workspace. If a fact is absent, say you do not know."))
	factAgent, err := agent.NewFactAwareAgent(runner, manager, extractor)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(agent.WithObserver(t.Context(), trace.observe), 3*time.Minute)
	defer cancel()
	started := time.Now()
	if _, err := factAgent.Run(ctx, "For this project, remember that the deployment region is amber-3."); err != nil {
		t.Fatalf("store fact turn: %v", err)
	}
	if len(manager.Facts()) == 0 {
		t.Fatal("explicit project fact was not stored")
	}
	if _, err := factAgent.Run(ctx, "Remember this updated project fact: this project uses cobalt-7 as its deployment region going forward, replacing amber-3."); err != nil {
		t.Fatalf("update fact turn: %v", err)
	}
	facts := manager.Facts()
	if len(facts) != 1 || !strings.Contains(facts[0].Content(), "cobalt-7") || strings.Contains(facts[0].Content(), "amber-3") {
		t.Fatalf("fact update did not replace the prior value: %+v", facts)
	}
	var secondTrace liveTrace
	// Share manager and agent protocol state; a new request asks for the recalled fact.
	answer, err := runner.Run(agent.WithObserver(ctx, secondTrace.observe), "What deployment region should this project use?")
	if err != nil {
		t.Fatalf("recall fact turn: %v", err)
	}
	if !strings.Contains(strings.ToLower(answer), "amber-3") {
		t.Errorf("fact was not recalled: %q", answer)
	}
	trace.elapsed = time.Since(started)
	trace.tools = append(trace.tools, secondTrace.tools...)
	trace.totalTokens += secondTrace.totalTokens
	trace.elapsed += secondTrace.elapsed
	t.Logf("facts=%d answer=%q %s", len(facts), answer, trace.summary())
}

func TestPermissionBenchmark(t *testing.T) {
	client := liveClient(t)
	for _, approve := range []bool{true, false} {
		name := "deny"
		if approve {
			name = "approve"
		}
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			var requests int
			confirm := func(_ context.Context, request tool.PermissionRequest) (bool, error) {
				requests++
				return approve && request.Tool == "write_file", nil
			}
			trace, answer, err := runLive(t, client, root, []string{"write_file"}, "Create approval.txt containing exactly: permission-path-ok", confirm, nil)
			if err != nil {
				t.Fatalf("agent run: %v", err)
			}
			data, readErr := os.ReadFile(filepath.Join(root, "approval.txt"))
			if requests == 0 {
				t.Fatal("permission confirmer was not called")
			}
			if approve && (readErr != nil || string(data) != "permission-path-ok") {
				t.Errorf("approved write missing: content=%q err=%v", data, readErr)
			}
			if !approve && readErr == nil {
				t.Errorf("denied write changed workspace: %q", data)
			}
			t.Logf("approved=%t requests=%d answer=%q %s", approve, requests, answer, trace.summary())
		})
	}
}

func runLive(t *testing.T, client *llm.OpenAICompatibleClient, root string, names []string, prompt string, confirmer tool.PermissionConfirmer, options []agent.ReActAgentOption) (liveTrace, string, error) {
	t.Helper()
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	registry := tool.NewToolRegistry()
	registry.RegisterProvider(tool.NewBuiltinProvider(work))
	registry = registry.Subset(names...)
	if confirmer != nil {
		registry.SetPermissionConfirmer(confirmer)
	}
	store, err := execution.NewJSONLEventStore(filepath.Join(t.TempDir(), "events"))
	if err != nil {
		t.Fatal(err)
	}
	conversation, err := execution.NewID()
	if err != nil {
		t.Fatal(err)
	}
	opts := append([]agent.ReActAgentOption{agent.WithRecorder(execution.NewRecorder(store, conversation))}, options...)
	runner := agent.NewReActAgent(client, registry, opts...)
	trace := liveTrace{}
	ctx, cancel := context.WithTimeout(agent.WithObserver(t.Context(), trace.observe), 3*time.Minute)
	defer cancel()
	started := time.Now()
	answer, runErr := runner.Run(ctx, prompt)
	trace.elapsed = time.Since(started)
	return trace, answer, runErr
}

func liveClient(t *testing.T) *llm.OpenAICompatibleClient {
	t.Helper()
	if os.Getenv("CATCLI_RUN_AGENT_BENCHMARK") != "1" {
		t.Skip("set CATCLI_RUN_AGENT_BENCHMARK=1 to call the configured model")
	}
	apiKey, baseURL, model := os.Getenv("CATCLI_BENCH_API_KEY"), os.Getenv("CATCLI_BENCH_BASE_URL"), os.Getenv("CATCLI_BENCH_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		cwd, err := os.Getwd()
		if err != nil {
			t.Fatal(err)
		}
		for dir := cwd; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				env, readErr := godotenv.Read(filepath.Join(dir, ".env"))
				if readErr == nil {
					if apiKey == "" {
						apiKey = env["CATCLI_OPENAI_COMPATIBLE_API_KEY"]
					}
					if baseURL == "" {
						baseURL = env["CATCLI_OPENAI_COMPATIBLE_BASE_URL"]
					}
					if model == "" {
						model = env["CATCLI_OPENAI_COMPATIBLE_MODEL"]
					}
				}
				break
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
		}
	}
	if apiKey == "" || baseURL == "" || model == "" {
		t.Fatal("set CATCLI_BENCH_* or CATCLI_OPENAI_COMPATIBLE_* model settings")
	}
	client, err := llm.NewOpenAICompatibleClient(apiKey, baseURL, model)
	if err != nil {
		t.Fatal(err)
	}
	return client
}

type liveTrace struct {
	tools       []string
	factActions []string
	totalTokens int
	elapsed     time.Duration
}

func (trace *liveTrace) observe(event agent.Event) {
	switch event.Type {
	case agent.EventToolCall:
		trace.tools = append(trace.tools, event.Title)
	case agent.EventMemoryFact:
		trace.factActions = append(trace.factActions, event.Title)
	case agent.EventTokenUsage:
		for _, field := range strings.Fields(event.Content) {
			key, value, ok := strings.Cut(field, "=")
			if ok && key == "total" {
				var n int
				_, _ = fmt.Sscanf(value, "%d", &n)
				trace.totalTokens += n
			}
		}
	}
}
func (trace liveTrace) hasTool(name string) bool {
	for _, got := range trace.tools {
		if got == name {
			return true
		}
	}
	return false
}
func (trace liveTrace) summary() string {
	return fmt.Sprintf("tool_calls=%d tools=%v fact_actions=%v total_tokens=%d elapsed=%s", len(trace.tools), trace.tools, trace.factActions, trace.totalTokens, trace.elapsed.Round(time.Millisecond))
}
func mustWrite(t *testing.T, root, path, content string) {
	t.Helper()
	full := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}
func contains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}

func approveOnly(name string) tool.PermissionConfirmer {
	return func(_ context.Context, request tool.PermissionRequest) (bool, error) {
		return request.Tool == name, nil
	}
}
