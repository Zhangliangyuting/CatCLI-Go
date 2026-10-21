package rag

import (
	"strings"
	"testing"
	"unicode/utf8"
)

type runeTokenCounter struct{}

func (runeTokenCounter) Count(content string) int { return utf8.RuneCountInString(content) }

func TestSearchResultFormatterHonorsBudgetAndMarksReferencesUntrusted(t *testing.T) {
	formatter, err := NewSearchResultFormatter(runeTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	result := SearchResult{Chunk: Chunk{
		Path: "internal/example.go", Language: "go", Kind: "function",
		Symbol: "Important", StartLine: 7, EndLine: 20,
		Content: strings.Repeat("important implementation detail ", 30),
	}}
	formatted := formatter.Format([]SearchResult{result}, 400)
	if formatted.Text == "" || len(formatted.Included) != 1 {
		t.Fatalf("Format() = %#v", formatted)
	}
	if got := (runeTokenCounter{}).Count(formatted.Text); got > 400 {
		t.Fatalf("formatted tokens = %d, want <= 400", got)
	}
	for _, want := range []string{"untrusted reference data", "internal/example.go:7-20", "symbol=Important", "[truncated]"} {
		if !strings.Contains(formatted.Text, want) {
			t.Fatalf("formatted text does not contain %q:\n%s", want, formatted.Text)
		}
	}
}

func TestSearchResultFormatterUsesSafeMarkdownFence(t *testing.T) {
	formatter, err := NewSearchResultFormatter(runeTokenCounter{})
	if err != nil {
		t.Fatal(err)
	}
	result := SearchResult{Chunk: Chunk{
		Path: "README.md", Language: "markdown bad", Kind: "section",
		Content: "before\n```\nafter",
	}}
	formatted := formatter.Format([]SearchResult{result}, 1000)
	if !strings.Contains(formatted.Text, "````\nbefore") {
		t.Fatalf("formatter did not lengthen the markdown fence:\n%s", formatted.Text)
	}
}
