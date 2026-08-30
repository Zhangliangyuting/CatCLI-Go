package config

import (
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
