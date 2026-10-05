package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/spf13/viper"
)

func TestAgentTimeoutConfiguration(t *testing.T) {
	cfg := defaultConfig()
	v := viper.New()
	setDefaults(v, cfg)
	v.Set("openai_compatible.api_key", "test-api-key")
	v.Set("agent.task_timeout", "2m")
	v.Set("agent.plan_timeout", "15m")

	if err := v.Unmarshal(&cfg); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if err := cfg.OpenAICompatible.resolveModelLimits(); err != nil {
		t.Fatalf("resolveModelLimits() error = %v", err)
	}
	if err := cfg.validate(); err != nil {
		t.Fatalf("validate() error = %v", err)
	}
	if cfg.Agent.TaskTimeout != 2*time.Minute {
		t.Fatalf("TaskTimeout = %s, want 2m", cfg.Agent.TaskTimeout)
	}
	if cfg.Agent.PlanTimeout != 15*time.Minute {
		t.Fatalf("PlanTimeout = %s, want 15m", cfg.Agent.PlanTimeout)
	}
}

func TestBuiltInModelLimitsFillZeroValues(t *testing.T) {
	cfg := OpenAICompatibleConfig{Model: " DeepSeek-V4-Pro "}

	if err := cfg.resolveModelLimits(); err != nil {
		t.Fatalf("resolveModelLimits() error = %v", err)
	}

	if cfg.ContextWindowTokens != 1024*1024 {
		t.Fatalf("ContextWindowTokens = %d, want %d", cfg.ContextWindowTokens, 1024*1024)
	}
	if cfg.MaxOutputTokens != 384*1024 {
		t.Fatalf("MaxOutputTokens = %d, want %d", cfg.MaxOutputTokens, 384*1024)
	}
	if cfg.OutputReserveTokens != 32*1024 {
		t.Fatalf("OutputReserveTokens = %d, want %d", cfg.OutputReserveTokens, 32*1024)
	}
	if cfg.CompactionMaxTokens != 4*1024 {
		t.Fatalf("CompactionMaxTokens = %d, want %d", cfg.CompactionMaxTokens, 4*1024)
	}
	if cfg.UsableInputTokens() != 992*1024 {
		t.Fatalf("UsableInputTokens() = %d, want %d", cfg.UsableInputTokens(), 992*1024)
	}
}

func TestConfiguredModelLimitsOverrideBuiltInValues(t *testing.T) {
	cfg := OpenAICompatibleConfig{
		Model:               "deepseek-v4-pro",
		ContextWindowTokens: 200_000,
		MaxOutputTokens:     40_000,
		OutputReserveTokens: 12_000,
		CompactionMaxTokens: 3_000,
	}

	if err := cfg.resolveModelLimits(); err != nil {
		t.Fatalf("resolveModelLimits() error = %v", err)
	}

	if cfg.ContextWindowTokens != 200_000 || cfg.MaxOutputTokens != 40_000 || cfg.OutputReserveTokens != 12_000 || cfg.CompactionMaxTokens != 3_000 {
		t.Fatalf("configured overrides were changed: %+v", cfg)
	}
	if cfg.UsableInputTokens() != 188_000 {
		t.Fatalf("UsableInputTokens() = %d, want 188000", cfg.UsableInputTokens())
	}
}

func TestUnknownModelRequiresConfiguredLimits(t *testing.T) {
	cfg := OpenAICompatibleConfig{Model: "provider-new-model"}

	err := cfg.resolveModelLimits()
	if err == nil {
		t.Fatal("resolveModelLimits() error = nil, want missing-limit error")
	}
}

func TestUnknownModelAcceptsCompleteConfiguredLimits(t *testing.T) {
	cfg := OpenAICompatibleConfig{
		Model:               "provider-new-model",
		ContextWindowTokens: 128_000,
		MaxOutputTokens:     16_000,
		OutputReserveTokens: 8_000,
		CompactionMaxTokens: 2_000,
	}

	if err := cfg.resolveModelLimits(); err != nil {
		t.Fatalf("resolveModelLimits() error = %v", err)
	}
}

func TestDefaultProviderMatchesDeepSeekConfiguration(t *testing.T) {
	cfg := defaultConfig()
	if cfg.OpenAICompatible.BaseURL != "https://api.deepseek.com" {
		t.Fatalf("default BaseURL = %q, want DeepSeek", cfg.OpenAICompatible.BaseURL)
	}
	if cfg.OpenAICompatible.Model != "deepseek-v4-pro" {
		t.Fatalf("default Model = %q, want deepseek-v4-pro", cfg.OpenAICompatible.Model)
	}
}

func TestLoadEmbeddingFromYAMLWithEnvironmentOverride(t *testing.T) {
	original, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "config"), 0700); err != nil {
		t.Fatal(err)
	}
	yaml := "debug: false\nembedding:\n  model: bge-m3\n  base_url: http://localhost:11434/v1\n  api_key: ollama\n"
	if err := os.WriteFile(filepath.Join(root, "config", "config.yaml"), []byte(yaml), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(root); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(original) })
	t.Setenv("CATCLI_OPENAI_COMPATIBLE_API_KEY", "chat-test-key")
	t.Setenv("CATCLI_EMBEDDING_MODEL", "override-model")
	t.Setenv("CATCLI_DEBUG", "true")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Embedding.Model != "override-model" || cfg.Embedding.BaseURL != "http://localhost:11434/v1" || cfg.Embedding.APIKey != "ollama" {
		t.Fatalf("embedding config = %+v", cfg.Embedding)
	}
	if !cfg.Debug {
		t.Fatal("CATCLI_DEBUG did not override YAML debug setting")
	}
}
