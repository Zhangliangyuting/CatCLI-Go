package config

import (
	"fmt"
	"strings"
)

const defaultOutputReserveTokens = 32 * 1024
const defaultCompactionMaxTokens = 4 * 1024

// ModelLimits describes hard limits published for a model. The values in the
// built-in table are defaults only; OpenAICompatibleConfig can override them
// for compatible providers that expose a model with different limits.
type ModelLimits struct {
	ContextWindowTokens int
	MaxOutputTokens     int
}

var builtInModelLimits = map[string]ModelLimits{
	"deepseek-v4-flash": {
		ContextWindowTokens: 1024 * 1024,
		MaxOutputTokens:     384 * 1024,
	},
	"deepseek-v4-pro": {
		ContextWindowTokens: 1024 * 1024,
		MaxOutputTokens:     384 * 1024,
	},
	"deepseek-v4-flash-vision-exp": {
		ContextWindowTokens: 1024 * 1024,
		MaxOutputTokens:     384 * 1024,
	},
}

// BuiltInModelLimits returns a copy of the built-in limits for model. Model
// names are matched case-insensitively and surrounding whitespace is ignored.
func BuiltInModelLimits(model string) (ModelLimits, bool) {
	limits, ok := builtInModelLimits[normalizeModelName(model)]
	return limits, ok
}

// resolveModelLimits fills zero-valued fields from the built-in model table.
// A non-zero configured value is always kept as an explicit override.
func (config *OpenAICompatibleConfig) resolveModelLimits() error {
	if config == nil {
		return fmt.Errorf("openai-compatible config is nil")
	}

	builtIn, knownModel := BuiltInModelLimits(config.Model)
	if config.ContextWindowTokens == 0 {
		if !knownModel {
			return fmt.Errorf(
				"no built-in context limit for model %q; set openai_compatible.context_window_tokens",
				config.Model,
			)
		}
		config.ContextWindowTokens = builtIn.ContextWindowTokens
	}
	if config.MaxOutputTokens == 0 {
		if !knownModel {
			return fmt.Errorf(
				"no built-in output limit for model %q; set openai_compatible.max_output_tokens",
				config.Model,
			)
		}
		config.MaxOutputTokens = builtIn.MaxOutputTokens
	}
	if config.OutputReserveTokens == 0 {
		config.OutputReserveTokens = defaultOutputReserveTokens
		if config.OutputReserveTokens > config.MaxOutputTokens {
			config.OutputReserveTokens = config.MaxOutputTokens
		}
	}
	if config.CompactionMaxTokens == 0 {
		config.CompactionMaxTokens = defaultCompactionMaxTokens
		if config.CompactionMaxTokens > config.MaxOutputTokens {
			config.CompactionMaxTokens = config.MaxOutputTokens
		}
	}

	return nil
}

// UsableInputTokens is the budget that may be occupied by system instructions,
// memory entries, tool definitions, and the current request. The same output
// reserve is sent as max_tokens, so compaction and request generation agree.
func (config OpenAICompatibleConfig) UsableInputTokens() int {
	return config.ContextWindowTokens - config.OutputReserveTokens
}

func normalizeModelName(model string) string {
	return strings.ToLower(strings.TrimSpace(model))
}
