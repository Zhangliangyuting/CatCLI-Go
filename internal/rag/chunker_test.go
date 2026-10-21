package rag

import (
	"strconv"
	"strings"
	"testing"
)

func TestChunkersHonorDisabledOverlap(t *testing.T) {
	config := ChunkConfig{MaxChunkBytes: 5, OverlapLines: -1}
	for _, test := range []struct {
		name  string
		chunk func(string, []byte) ([]Chunk, error)
	}{
		{"text", NewTextChunker(config).Chunk},
		{"default set", NewDefaultChunkerSet(config).Chunk},
	} {
		t.Run(test.name, func(t *testing.T) {
			chunks, err := test.chunk("notes.txt", []byte("aa\nbb\ncc\ndd"))
			if err != nil {
				t.Fatal(err)
			}
			if len(chunks) != 2 || chunks[0].Content != "aa\nbb" || chunks[1].Content != "cc\ndd" {
				t.Fatalf("expected two chunks without repeated lines, got %#v", chunks)
			}
		})
	}
}

func TestDefaultChunkerSetVersionUsesEffectiveConfig(t *testing.T) {
	prefix := strconv.Itoa(ChunkerSchemaVersion) + ":12288:"
	if got := NewDefaultChunkerSet(ChunkConfig{}).Version(); got != prefix+"3" {
		t.Fatalf("default version = %q, want %q", got, prefix+"3")
	}
	if got := NewDefaultChunkerSet(ChunkConfig{OverlapLines: -1}).Version(); got != prefix+"0" {
		t.Fatalf("disabled overlap version = %q, want %q", got, prefix+"0")
	}
}

func TestGoASTChunkerUsesDeclarationsAsChunks(t *testing.T) {
	source := `package sample

import "fmt"

type Workspace struct {
	Root string
}

// Resolve returns one path.
func (workspace *Workspace) Resolve(path string) string {
	return fmt.Sprintf("%s/%s", workspace.Root, path)
}

func helper() {}
`
	chunks, err := NewGoASTChunker(ChunkConfig{}).Chunk("workspace.go", []byte(source))
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}

	assertChunk(t, chunks, "file_header", "sample")
	assertChunk(t, chunks, "type", "Workspace")
	method := assertChunk(t, chunks, "method", "Workspace.Resolve")
	if method.StartLine != 9 || !strings.Contains(method.Content, "Resolve(path string)") {
		t.Fatalf("method chunk = %#v", method)
	}
	assertChunk(t, chunks, "function", "helper")
}

func TestMarkdownChunkerUsesHeadingHierarchyAndIgnoresFencedHeadings(t *testing.T) {
	source := `# Memory
overview

## Retriever
details

~~~markdown
# Not a heading
~~~

## Compaction
summary
`
	chunks, err := NewMarkdownChunker(ChunkConfig{}).Chunk("README.md", []byte(source))
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) != 3 {
		t.Fatalf("chunk count = %d, want 3: %#v", len(chunks), chunks)
	}
	if chunks[0].Heading != "Memory" || chunks[1].Heading != "Memory > Retriever" || chunks[2].Heading != "Memory > Compaction" {
		t.Fatalf("headings = %q, %q, %q", chunks[0].Heading, chunks[1].Heading, chunks[2].Heading)
	}
	if !strings.Contains(chunks[1].Content, "# Not a heading") {
		t.Fatalf("fenced code was not retained: %q", chunks[1].Content)
	}
}

func TestTextChunkerUsesParagraphsAndSplitsLargeParagraph(t *testing.T) {
	source := "first paragraph\n\nline-one\nline-two\nline-three\nline-four"
	chunks, err := NewTextChunker(ChunkConfig{
		MaxChunkBytes: 20,
		OverlapLines:  1,
	}).Chunk("notes.txt", []byte(source))
	if err != nil {
		t.Fatalf("Chunk() error = %v", err)
	}
	if len(chunks) < 3 {
		t.Fatalf("chunk count = %d, want at least 3: %#v", len(chunks), chunks)
	}
	if chunks[0].Kind != "paragraph" || chunks[0].StartLine != 1 || chunks[0].EndLine != 1 {
		t.Fatalf("first paragraph = %#v", chunks[0])
	}
	if !strings.Contains(chunks[1].Content, "line-two") || !strings.Contains(chunks[2].Content, "line-two") {
		t.Fatalf("expected line overlap: %#v", chunks)
	}
}

func TestDefaultChunkerSetRoutesByFileType(t *testing.T) {
	set := NewDefaultChunkerSet(ChunkConfig{})
	goChunks, err := set.Chunk("main.go", []byte("package main\n\nfunc main() {}"))
	if err != nil {
		t.Fatalf("Chunk(Go) error = %v", err)
	}
	if assertChunk(t, goChunks, "function", "main").Language != "go" {
		t.Fatalf("Go chunks = %#v", goChunks)
	}

	markdownChunks, err := set.Chunk("README.md", []byte("# Title\nbody"))
	if err != nil {
		t.Fatalf("Chunk(Markdown) error = %v", err)
	}
	if len(markdownChunks) != 1 || markdownChunks[0].Language != "markdown" {
		t.Fatalf("Markdown chunks = %#v", markdownChunks)
	}
}

func TestChunkersRejectInvalidUTF8(t *testing.T) {
	_, err := NewDefaultChunkerSet(ChunkConfig{}).Chunk("binary.dat", []byte{0xff, 0xfe})
	if err == nil {
		t.Fatal("Chunk() error = nil, want invalid UTF-8 error")
	}
}

func assertChunk(t *testing.T, chunks []Chunk, kind, symbol string) Chunk {
	t.Helper()
	for _, chunk := range chunks {
		if chunk.Kind == kind && chunk.Symbol == symbol {
			return chunk
		}
	}
	t.Fatalf("missing chunk kind=%q symbol=%q in %#v", kind, symbol, chunks)
	return Chunk{}
}
