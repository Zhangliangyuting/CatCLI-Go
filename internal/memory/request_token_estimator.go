package memory

import (
	"AgentCLI/internal/llm"
	"AgentCLI/internal/tool"
	"encoding/json"
	"fmt"
	"math"
	"sync"
)

const (
	defaultRequestSafetyRatio = 1.05
	calibrationWeight         = 0.25
	minimumCalibrationFactor  = 0.50
	maximumCalibrationFactor  = 4.00
)

// RequestTokenEstimator estimates the complete logical model input, including
// messages, tool calls, and tool definitions. Observe uses provider-reported
// prompt tokens to improve later estimates.
type RequestTokenEstimator interface {
	Estimate(messages []llm.Message, tools []tool.Definition) (int, error)
	Observe(estimatedTokens, actualPromptTokens int)
}

// CalibratedRequestTokenEstimator is dependency-free. It tokenizes the
// serialized logical request with TokenCounter, adds a safety margin, and
// gradually corrects its estimate using actual API usage.
type CalibratedRequestTokenEstimator struct {
	mu                sync.RWMutex
	counter           TokenCounter
	calibrationFactor float64
	safetyRatio       float64
}

func NewCalibratedRequestTokenEstimator(counter TokenCounter) *CalibratedRequestTokenEstimator {
	if counter == nil {
		counter = ApproxTokenCounter{}
	}
	return &CalibratedRequestTokenEstimator{
		counter:           counter,
		calibrationFactor: 1,
		safetyRatio:       defaultRequestSafetyRatio,
	}
}

func (estimator *CalibratedRequestTokenEstimator) Estimate(
	messages []llm.Message,
	tools []tool.Definition,
) (int, error) {
	if estimator == nil || estimator.counter == nil {
		return 0, fmt.Errorf("request token estimator is nil")
	}
	payload := struct {
		Messages []llm.Message     `json:"messages"`
		Tools    []tool.Definition `json:"tools,omitempty"`
	}{
		Messages: messages,
		Tools:    tools,
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return 0, fmt.Errorf("encode logical LLM request: %w", err)
	}
	baseTokens := estimator.counter.Count(string(encoded))

	estimator.mu.RLock()
	factor := estimator.calibrationFactor
	safety := estimator.safetyRatio
	estimator.mu.RUnlock()

	tokens := int(math.Ceil(float64(baseTokens) * factor * safety))
	if tokens < 1 {
		tokens = 1
	}
	return tokens, nil
}

func (estimator *CalibratedRequestTokenEstimator) Observe(
	estimatedTokens int,
	actualPromptTokens int,
) {
	if estimator == nil || estimatedTokens <= 0 || actualPromptTokens <= 0 {
		return
	}
	estimator.mu.Lock()
	defer estimator.mu.Unlock()

	// Target a safety-adjusted version of the observed provider usage. EWMA
	// smoothing avoids one unusual request destabilizing future estimates.
	correction := float64(actualPromptTokens) * estimator.safetyRatio / float64(estimatedTokens)
	estimator.calibrationFactor *= 1 + calibrationWeight*(correction-1)
	if estimator.calibrationFactor < minimumCalibrationFactor {
		estimator.calibrationFactor = minimumCalibrationFactor
	}
	if estimator.calibrationFactor > maximumCalibrationFactor {
		estimator.calibrationFactor = maximumCalibrationFactor
	}
}
