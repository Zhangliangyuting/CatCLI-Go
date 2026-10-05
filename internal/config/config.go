package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	ConfigFile       string                 `mapstructure:"-"`
	Debug            bool                   `mapstructure:"debug"`
	OpenAICompatible OpenAICompatibleConfig `mapstructure:"openai_compatible"`
	Embedding        EmbeddingConfig        `mapstructure:"embedding"`
	Agent            AgentConfig            `mapstructure:"agent"`
	Providers        ProvidersConfig        `mapstructure:"providers"`
	Tools            ToolsConfig            `mapstructure:"tools"`
}

type OpenAICompatibleConfig struct {
	APIKey              string `mapstructure:"api_key"`
	BaseURL             string `mapstructure:"base_url"`
	Model               string `mapstructure:"model"`
	ContextWindowTokens int    `mapstructure:"context_window_tokens"`
	MaxOutputTokens     int    `mapstructure:"max_output_tokens"`
	OutputReserveTokens int    `mapstructure:"output_reserve_tokens"`
	CompactionMaxTokens int    `mapstructure:"compaction_max_tokens"`
}

type EmbeddingConfig struct {
	Model   string `mapstructure:"model"`
	BaseURL string `mapstructure:"base_url"`
	APIKey  string `mapstructure:"api_key"`
}

type AgentConfig struct {
	MaxReplanAttempts int           `mapstructure:"max_replan_attempts"`
	MaxWorkers        int           `mapstructure:"max_workers"`
	TaskTimeout       time.Duration `mapstructure:"task_timeout"`
	PlanTimeout       time.Duration `mapstructure:"plan_timeout"`
}

type ProvidersConfig struct {
	Enabled []string `mapstructure:"enabled"`
}

type ToolsConfig struct {
	Enabled []string `mapstructure:"enabled"`
}

func Load() (Config, error) {
	cfg := defaultConfig()

	_ = godotenv.Load()

	v := viper.New()

	// 读取配置文件默认值到 Viper
	setDefaults(v, cfg)

	v.SetConfigName("config")
	v.SetConfigType("yaml")
	v.AddConfigPath("config")

	//优先级: 环境变量 > configs/config.yaml > 默认值.
	v.SetEnvPrefix("CATCLI")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.BindEnv("openai_compatible.api_key"); err != nil {
		return Config{}, err
	}
	if err := v.BindEnv("debug"); err != nil {
		return Config{}, err
	}

	if err := v.BindEnv("openai_compatible.base_url"); err != nil {
		return Config{}, err
	}

	if err := v.BindEnv("openai_compatible.model"); err != nil {
		return Config{}, err
	}
	if err := v.BindEnv("openai_compatible.context_window_tokens"); err != nil {
		return Config{}, err
	}
	if err := v.BindEnv("openai_compatible.max_output_tokens"); err != nil {
		return Config{}, err
	}
	if err := v.BindEnv("openai_compatible.output_reserve_tokens"); err != nil {
		return Config{}, err
	}
	if err := v.BindEnv("openai_compatible.compaction_max_tokens"); err != nil {
		return Config{}, err
	}

	for _, key := range []string{"embedding.model", "embedding.base_url", "embedding.api_key"} {
		if err := v.BindEnv(key); err != nil {
			return Config{}, err
		}
	}

	//查找并解析yaml文件
	if err := v.ReadInConfig(); err != nil {
		if _, ok := err.(viper.ConfigFileNotFoundError); !ok {
			return Config{}, err
		}
	}

	if err := v.Unmarshal(&cfg); err != nil {
		return Config{}, err
	}
	if err := cfg.OpenAICompatible.resolveModelLimits(); err != nil {
		return Config{}, err
	}
	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	if configFile := v.ConfigFileUsed(); configFile != "" {
		absolutePath, err := filepath.Abs(configFile)
		if err != nil {
			return Config{}, fmt.Errorf("resolve config file path: %w", err)
		}
		cfg.ConfigFile = absolutePath
	}

	return cfg, nil
}

func defaultConfig() Config {
	return Config{
		OpenAICompatible: OpenAICompatibleConfig{
			BaseURL: "https://api.deepseek.com",
			Model:   "deepseek-v4-pro",
		},
		Agent: AgentConfig{
			MaxReplanAttempts: 3,
			MaxWorkers:        3,
			TaskTimeout:       5 * time.Minute,
			PlanTimeout:       30 * time.Minute,
		},
		Providers: ProvidersConfig{
			Enabled: []string{"builtin"},
		},
		Tools: ToolsConfig{
			Enabled: []string{
				"list_dir",
				"read_file",
				"edit_file",
			},
		},
	}
}

func setDefaults(v *viper.Viper, cfg Config) {
	v.SetDefault("debug", cfg.Debug)
	v.SetDefault("openai_compatible.base_url", cfg.OpenAICompatible.BaseURL)
	v.SetDefault("openai_compatible.model", cfg.OpenAICompatible.Model)
	v.SetDefault("openai_compatible.context_window_tokens", cfg.OpenAICompatible.ContextWindowTokens)
	v.SetDefault("openai_compatible.max_output_tokens", cfg.OpenAICompatible.MaxOutputTokens)
	v.SetDefault("openai_compatible.output_reserve_tokens", cfg.OpenAICompatible.OutputReserveTokens)
	v.SetDefault("openai_compatible.compaction_max_tokens", cfg.OpenAICompatible.CompactionMaxTokens)
	v.SetDefault("agent.max_replan_attempts", cfg.Agent.MaxReplanAttempts)
	v.SetDefault("agent.max_workers", cfg.Agent.MaxWorkers)
	v.SetDefault("agent.task_timeout", cfg.Agent.TaskTimeout)
	v.SetDefault("agent.plan_timeout", cfg.Agent.PlanTimeout)
	v.SetDefault("tools.enabled", cfg.Tools.Enabled)
	v.SetDefault("providers.enabled", cfg.Providers.Enabled)
}

func (c Config) validate() error {
	if c.OpenAICompatible.APIKey == "" {
		return errors.New("openai_compatible.api_key is required")
	}
	if c.OpenAICompatible.ContextWindowTokens <= 0 {
		return errors.New("openai_compatible.context_window_tokens must be positive")
	}
	if c.OpenAICompatible.MaxOutputTokens <= 0 {
		return errors.New("openai_compatible.max_output_tokens must be positive")
	}
	if c.OpenAICompatible.MaxOutputTokens > c.OpenAICompatible.ContextWindowTokens {
		return errors.New("openai_compatible.max_output_tokens must not exceed context_window_tokens")
	}
	if c.OpenAICompatible.OutputReserveTokens <= 0 {
		return errors.New("openai_compatible.output_reserve_tokens must be positive")
	}
	if c.OpenAICompatible.OutputReserveTokens > c.OpenAICompatible.MaxOutputTokens {
		return errors.New("openai_compatible.output_reserve_tokens must not exceed max_output_tokens")
	}
	if c.OpenAICompatible.CompactionMaxTokens <= 0 {
		return errors.New("openai_compatible.compaction_max_tokens must be positive")
	}
	if c.OpenAICompatible.CompactionMaxTokens > c.OpenAICompatible.MaxOutputTokens {
		return errors.New("openai_compatible.compaction_max_tokens must not exceed max_output_tokens")
	}
	if c.OpenAICompatible.UsableInputTokens() <= 0 {
		return errors.New("openai_compatible.output_reserve_tokens must be smaller than context_window_tokens")
	}
	if c.Agent.MaxReplanAttempts < 0 {
		return errors.New("agent.max_replan_attempts must not be negative")
	}
	if c.Agent.MaxWorkers < 1 {
		return errors.New("agent.max_workers must be positive")
	}
	if c.Agent.TaskTimeout <= 0 {
		return errors.New("agent.task_timeout must be positive")
	}
	if c.Agent.PlanTimeout <= 0 {
		return errors.New("agent.plan_timeout must be positive")
	}
	if c.Agent.PlanTimeout < c.Agent.TaskTimeout {
		return errors.New("agent.plan_timeout must not be shorter than agent.task_timeout")
	}
	return nil
}
