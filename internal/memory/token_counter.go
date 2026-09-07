package memory

import "unicode/utf8"

// TokenCounter allows a model-specific tokenizer to be supplied by the caller.
type TokenCounter interface {
	Count(content string) int
}

// TokenCounterFunc adapts a function to TokenCounter.
type TokenCounterFunc func(content string) int

func (f TokenCounterFunc) Count(content string) int {
	return f(content)
}

// ApproxTokenCounter provides a dependency-free estimate. It counts non-ASCII
// runes as one token and groups ASCII text at roughly four bytes per token.
// A model-specific counter should be used when an exact context limit matters.
type ApproxTokenCounter struct{}

func (ApproxTokenCounter) Count(content string) int {
	if content == "" {
		return 0
	}

	tokens := 0
	asciiBytes := 0
	flushASCII := func() {
		if asciiBytes > 0 {
			tokens += (asciiBytes + 3) / 4
			asciiBytes = 0
		}
	}

	for len(content) > 0 {
		r, size := utf8.DecodeRuneInString(content)
		content = content[size:]
		if r <= 127 {
			asciiBytes += size
			continue
		}

		flushASCII()
		tokens++
	}
	flushASCII()

	return tokens
}
