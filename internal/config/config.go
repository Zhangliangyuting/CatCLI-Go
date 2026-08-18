package config

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/joho/godotenv"
	"github.com/spf13/viper"
)

type Config struct {
	ConfigFile       string                 `mapstructure:"-"`
	OpenAICompatible OpenAICompatibleConfig `mapstructure:"openai_compatible"`
	Providers        ProvidersConfig        `mapstructure:"providers"`
	Tools            ToolsConfig            `mapstructure:"tools"`
}

type OpenAICompatibleConfig struct {
	APIKey  string `mapstructure:"api_key"`
	BaseURL string `mapstructure:"base_url"`
	Model   string `mapstructure:"model"`
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

	if err := v.BindEnv("openai_compatible.base_url"); err != nil {
		return Config{}, err
	}

	if err := v.BindEnv("openai_compatible.model"); err != nil {
		return Config{}, err
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
			BaseURL: "https://open.bigmodel.cn/api/paas/v4",
			Model:   "glm-5.1",
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
	v.SetDefault("openai_compatible.base_url", cfg.OpenAICompatible.BaseURL)
	v.SetDefault("openai_compatible.model", cfg.OpenAICompatible.Model)
	v.SetDefault("tools.enabled", cfg.Tools.Enabled)
	v.SetDefault("providers.enabled", cfg.Providers.Enabled)
}

func (c Config) validate() error {
	if c.OpenAICompatible.APIKey == "" {
		return errors.New("openai_compatible.api_key is required")
	}
	return nil
}
