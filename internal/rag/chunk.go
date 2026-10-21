package rag

// Chunk is one independently retrievable portion of a workspace document.
type Chunk struct {
	Path      string
	Language  string
	Kind      string
	Symbol    string
	Heading   string
	StartLine int
	EndLine   int
	Content   string
}
