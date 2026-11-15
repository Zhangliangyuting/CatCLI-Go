package readonly

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/execution"
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"AgentCLI/internal/workspace"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/joho/godotenv"
)

func TestReadOnlyAgentBenchmark(t *testing.T) {
	if os.Getenv("CATCLI_RUN_AGENT_BENCHMARK") != "1" {
		t.Skip("set CATCLI_RUN_AGENT_BENCHMARK=1 to call the configured model")
	}
	apiKey := os.Getenv("CATCLI_BENCH_API_KEY")
	baseURL := os.Getenv("CATCLI_BENCH_BASE_URL")
	model := os.Getenv("CATCLI_BENCH_MODEL")
	if apiKey == "" || baseURL == "" || model == "" {
		dotenv, err := readProjectDotEnv()
		if err != nil {
			dotenv = map[string]string{}
		}
		if apiKey == "" {
			apiKey = firstNonEmpty(os.Getenv("CATCLI_OPENAI_COMPATIBLE_API_KEY"), dotenv["CATCLI_OPENAI_COMPATIBLE_API_KEY"])
		}
		if baseURL == "" {
			baseURL = firstNonEmpty(os.Getenv("CATCLI_OPENAI_COMPATIBLE_BASE_URL"), dotenv["CATCLI_OPENAI_COMPATIBLE_BASE_URL"])
		}
		if model == "" {
			model = firstNonEmpty(os.Getenv("CATCLI_OPENAI_COMPATIBLE_MODEL"), dotenv["CATCLI_OPENAI_COMPATIBLE_MODEL"])
		}
	}
	if apiKey == "" || baseURL == "" || model == "" {
		t.Fatal("configure CATCLI_BENCH_* or CATCLI_OPENAI_COMPATIBLE_* model settings before running the live benchmark")
	}
	client, err := llm.NewOpenAICompatibleClient(apiKey, baseURL, model)
	if err != nil {
		t.Fatal(err)
	}

	cases := readOnlyCases()
	passes, totalTokens, totalCalls := 0, 0, 0
	for _, benchmarkCase := range cases {
		t.Run(benchmarkCase.name, func(t *testing.T) {
			workspaceRoot := t.TempDir()
			for path, contents := range benchmarkCase.files {
				fullPath := filepath.Join(workspaceRoot, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(fullPath), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(fullPath, []byte(contents), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before, err := snapshotWorkspace(workspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			work, err := workspace.New(workspaceRoot)
			if err != nil {
				t.Fatal(err)
			}
			registry := tool.NewToolRegistry()
			registry.RegisterProvider(tool.NewBuiltinProvider(work))
			registry = registry.ReadOnlySubset()

			logStore, err := execution.NewJSONLEventStore(filepath.Join(t.TempDir(), "execution"))
			if err != nil {
				t.Fatal(err)
			}
			conversationID := "readonly-bench-" + benchmarkCase.name
			runner := agent.NewReActAgent(client, registry,
				agent.WithRecorder(execution.NewRecorder(logStore, conversationID)),
				agent.WithSystemPrompt("Answer only from the current workspace. Use read-only tools to inspect relevant files. If the requested information is absent, say it is not specified. Do not guess."),
			)
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
			defer cancel()
			trace := benchmarkTrace{}
			started := time.Now()
			answer, runErr := runner.Run(agent.WithObserver(ctx, trace.observe), benchmarkCase.prompt)
			elapsed := time.Since(started)
			after, snapshotErr := snapshotWorkspace(workspaceRoot)
			if snapshotErr != nil {
				t.Fatal(snapshotErr)
			}
			issues := verifyReadOnlyResult(benchmarkCase, answer, runErr, trace, before, after)
			passed := len(issues) == 0
			if passed {
				passes++
			} else {
				for _, issue := range issues {
					t.Error(issue)
				}
			}
			totalTokens += trace.totalTokens
			totalCalls += len(trace.toolCalls)
			t.Logf("passed=%t elapsed=%s tool_calls=%d total_tokens=%d answer=%q", passed, elapsed.Round(time.Millisecond), len(trace.toolCalls), trace.totalTokens, answer)
		})
	}
	t.Logf("read-only benchmark summary: passed=%d/%d success_rate=%.1f%% tool_calls=%d total_tokens=%d", passes, len(cases), 100*float64(passes)/float64(len(cases)), totalCalls, totalTokens)
	if passes != len(cases) {
		t.Fatalf("read-only benchmark passed %d/%d cases", passes, len(cases))
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func readProjectDotEnv() (map[string]string, error) {
	workingDirectory, err := os.Getwd()
	if err != nil {
		return nil, err
	}
	for directory := workingDirectory; ; directory = filepath.Dir(directory) {
		if _, err := os.Stat(filepath.Join(directory, "go.mod")); err == nil {
			return godotenv.Read(filepath.Join(directory, ".env"))
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return nil, fmt.Errorf("could not find repository root from %s", workingDirectory)
		}
	}
}

type readOnlyBenchmarkCase struct {
	name           string
	prompt         string
	files          map[string]string
	required       []string
	forbidden      []string
	abstainPhrases []string
	expectedTools  []string
}

func readOnlyCases() []readOnlyBenchmarkCase {
	return []readOnlyBenchmarkCase{
		{
			name:     "single_file_lookup",
			prompt:   "Read service.yaml and report the service name and port.",
			files:    map[string]string{"service.yaml": "service: copper-api\nport: 8142\n"},
			required: []string{"copper-api", "8142"}, forbidden: []string{"8143"}, expectedTools: []string{"read_file"},
		},
		{
			name:   "multi_file_summary",
			prompt: "Using README.md and config/app.yaml, summarize the app purpose, port, and database.",
			files: map[string]string{
				"README.md":       "Copper is a small inventory API for tracking workshop parts.\n",
				"config/app.yaml": "server:\n  port: 9231\nstorage:\n  driver: sqlite\n",
			},
			required: []string{"inventory", "9231", "sqlite"}, forbidden: []string{"postgres", "5432"}, expectedTools: []string{"read_file"},
		},
		{
			name:     "directory_inventory",
			prompt:   "List the direct entries in configs/ and report their names.",
			files:    map[string]string{"configs/dev.yaml": "mode: dev\n", "configs/prod.yaml": "mode: prod\n", "README.md": "Config inventory fixture.\n"},
			required: []string{"dev.yaml", "prod.yaml"}, forbidden: []string{"secret.yaml"}, expectedTools: []string{"list_dir"},
		},
		{
			name:     "missing_information_abstention",
			prompt:   "Read service.yaml and report the timeout. If the file does not specify one, say so.",
			files:    map[string]string{"service.yaml": "service: copper-api\nport: 8142\n"},
			required: []string{"timeout"}, abstainPhrases: []string{"not specified", "not configured", "not present", "cannot determine", "doesn't specify", "does not specify"},
			expectedTools: []string{"read_file"},
		},
		{
			name:   "explicit_conflicting_values",
			prompt: "README.md and config/app.yaml mention different ports. Report both values and identify which file says each.",
			files: map[string]string{
				"README.md":       "The old local setup uses port 7000.\n",
				"config/app.yaml": "server:\n  port: 7400\n",
			},
			required: []string{"7000", "7400", "README", "app.yaml"}, expectedTools: []string{"read_file"},
		},
	}
}

type benchmarkTrace struct {
	toolCalls   []string
	totalTokens int
}

func (trace *benchmarkTrace) observe(event agent.Event) {
	switch event.Type {
	case agent.EventToolCall:
		trace.toolCalls = append(trace.toolCalls, event.Title)
	case agent.EventTokenUsage:
		for _, field := range strings.Fields(event.Content) {
			key, value, ok := strings.Cut(field, "=")
			if ok && key == "total" {
				var total int
				if _, err := fmt.Sscanf(value, "%d", &total); err == nil {
					trace.totalTokens += total
				}
			}
		}
	}
}

func verifyReadOnlyResult(benchmarkCase readOnlyBenchmarkCase, answer string, runErr error, trace benchmarkTrace, before, after map[string]string) []string {
	var issues []string
	if runErr != nil {
		issues = append(issues, "agent run failed: "+runErr.Error())
	}
	answerLower := strings.ToLower(answer)
	for _, required := range benchmarkCase.required {
		if !strings.Contains(answerLower, strings.ToLower(required)) {
			issues = append(issues, fmt.Sprintf("answer is missing required fact %q", required))
		}
	}
	for _, forbidden := range benchmarkCase.forbidden {
		if strings.Contains(answerLower, strings.ToLower(forbidden)) {
			issues = append(issues, fmt.Sprintf("answer contains forbidden claim %q", forbidden))
		}
	}
	if len(benchmarkCase.abstainPhrases) > 0 {
		abstained := false
		for _, phrase := range benchmarkCase.abstainPhrases {
			if strings.Contains(answerLower, strings.ToLower(phrase)) {
				abstained = true
				break
			}
		}
		if !abstained {
			issues = append(issues, "answer did not clearly abstain about the missing value")
		}
	}
	calledExpectedTool := false
	for _, call := range trace.toolCalls {
		if call != "read_file" && call != "list_dir" {
			issues = append(issues, "non-read-only tool was called: "+call)
		}
		for _, expected := range benchmarkCase.expectedTools {
			if call == expected {
				calledExpectedTool = true
			}
		}
	}
	if !calledExpectedTool {
		issues = append(issues, "agent did not call an expected read-only tool")
	}
	if len(before) != len(after) {
		issues = append(issues, "workspace entry set changed during read-only task")
	} else {
		for path, digest := range before {
			if after[path] != digest {
				issues = append(issues, "workspace content changed during read-only task: "+path)
			}
		}
	}
	return issues
}

func snapshotWorkspace(root string) (map[string]string, error) {
	snapshot := make(map[string]string)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." {
			return nil
		}
		if entry.IsDir() {
			snapshot[relative+string(filepath.Separator)] = "dir"
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		snapshot[relative] = fmt.Sprintf("%x", sha256.Sum256(data))
		return nil
	})
	return snapshot, err
}
