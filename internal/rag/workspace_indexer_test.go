package rag

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"AgentCLI/internal/workspace"
)

type recordingEmbedder struct {
	batches []int
	err     error
}

func (embedder *recordingEmbedder) Embed(ctx context.Context, text string) ([]float32, error) {
	vectors, err := embedder.EmbedBatch(ctx, []string{text})
	if err != nil {
		return nil, err
	}
	return vectors[0], nil
}

func (embedder *recordingEmbedder) EmbedBatch(_ context.Context, texts []string) ([][]float32, error) {
	if embedder.err != nil {
		return nil, embedder.err
	}
	embedder.batches = append(embedder.batches, len(texts))
	vectors := make([][]float32, len(texts))
	for index, text := range texts {
		vectors[index] = []float32{float32(len(text)), float32(index + 1)}
	}
	return vectors, nil
}

func TestWorkspaceIndexerBuildsUpdatesAndCleansIndex(t *testing.T) {
	root := t.TempDir()
	writeIndexFile(t, root, "main.go", "package sample\n\nfunc First() {}\n")
	writeIndexFile(t, root, "README.md", "# Sample\nDocumentation.\n")
	writeIndexFile(t, root, "Dockerfile", "FROM scratch\n")
	writeIndexFile(t, root, ".env", "SECRET=do-not-index\n")
	writeIndexFile(t, root, ".git/ignored.go", "package ignored\n")
	writeIndexFile(t, root, "node_modules/dependency.js", "export const ignored = true\n")
	writeIndexBytes(t, root, "image.bin", []byte{'P', 'N', 'G', 0, 1})

	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	embedder := &recordingEmbedder{}
	indexer, err := NewWorkspaceIndexer(
		work,
		NewDefaultChunkerSet(ChunkConfig{}),
		embedder,
		store,
		WorkspaceIndexerConfig{EmbeddingBatchSize: 2, EmbeddingModel: "test-embedding"},
	)
	if err != nil {
		t.Fatal(err)
	}

	first, err := indexer.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first.IndexedFiles != 3 || first.UnchangedFiles != 0 || first.DeletedFiles != 0 || first.Chunks != 4 {
		t.Fatalf("first report = %#v", first)
	}
	files, err := store.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(files); !reflect.DeepEqual(got, []string{"Dockerfile", "README.md", "main.go"}) {
		t.Fatalf("indexed paths = %v", got)
	}
	firstBatchCount := len(embedder.batches)

	second, err := indexer.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if second.IndexedFiles != 0 || second.UnchangedFiles != 3 || len(embedder.batches) != firstBatchCount {
		t.Fatalf("second report = %#v, embedding batches = %v", second, embedder.batches)
	}

	writeIndexFile(t, root, "main.go", "package sample\n\nfunc Second() {}\n")
	if err := os.Remove(filepath.Join(root, "README.md")); err != nil {
		t.Fatal(err)
	}
	third, err := indexer.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if third.IndexedFiles != 1 || third.UnchangedFiles != 1 || third.DeletedFiles != 1 {
		t.Fatalf("third report = %#v", third)
	}
	files, err = store.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := indexedPaths(files); !reflect.DeepEqual(got, []string{"Dockerfile", "main.go"}) {
		t.Fatalf("indexed paths after update = %v", got)
	}
	chunks, err := store.LoadChunks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, chunk := range chunks {
		if chunk.Path == "README.md" || strings.Contains(chunk.Content, "First") {
			t.Fatalf("stale chunk remains: %#v", chunk)
		}
	}
}

func TestWorkspaceIndexerKeepsOldFileWhenEmbeddingFails(t *testing.T) {
	root := t.TempDir()
	writeIndexFile(t, root, "main.go", "package sample\n\nfunc Original() {}\n")
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunkers := NewDefaultChunkerSet(ChunkConfig{})
	indexer, err := NewWorkspaceIndexer(work, chunkers, &recordingEmbedder{}, store, WorkspaceIndexerConfig{EmbeddingModel: "test-embedding"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := indexer.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	originalHash, exists, err := store.FileHash(context.Background(), "main.go")
	if err != nil || !exists {
		t.Fatalf("original FileHash() = %q, %t, %v", originalHash, exists, err)
	}

	writeIndexFile(t, root, "main.go", "package sample\n\nfunc Replacement() {}\n")
	failing, err := NewWorkspaceIndexer(
		work, chunkers, &recordingEmbedder{err: errors.New("embedding unavailable")}, store, WorkspaceIndexerConfig{EmbeddingModel: "test-embedding"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := failing.Index(context.Background()); err == nil || !strings.Contains(err.Error(), "embedding unavailable") {
		t.Fatalf("Index() error = %v", err)
	}
	afterHash, exists, err := store.FileHash(context.Background(), "main.go")
	if err != nil || !exists || afterHash != originalHash {
		t.Fatalf("FileHash() after failure = %q, %t, %v; want %q", afterHash, exists, err, originalHash)
	}
	chunks, err := store.LoadChunks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !chunksContain(chunks, "Original") || chunksContain(chunks, "Replacement") {
		t.Fatalf("chunks after failed replacement = %#v", chunks)
	}
}

func TestWorkspaceIndexerBatchesEmbeddings(t *testing.T) {
	indexer := &WorkspaceIndexer{
		embedder: &recordingEmbedder{},
		config:   WorkspaceIndexerConfig{EmbeddingBatchSize: 2}.normalized(),
	}
	chunks := make([]Chunk, 5)
	for index := range chunks {
		chunks[index] = Chunk{Path: "notes.txt", Kind: "paragraph", Content: "chunk " + string(rune('a'+index))}
	}
	embedded, dimensions, err := indexer.embedChunks(context.Background(), chunks, 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := indexer.embedder.(*recordingEmbedder).batches; !reflect.DeepEqual(got, []int{2, 2, 1}) {
		t.Fatalf("batch sizes = %v", got)
	}
	if dimensions != 2 {
		t.Fatalf("embedding dimensions = %d, want 2", dimensions)
	}
	seen := make(map[string]bool, len(embedded))
	for ordinal, chunk := range embedded {
		if chunk.Ordinal != ordinal || chunk.ID == "" || seen[chunk.ID] {
			t.Fatalf("embedded chunk %d = %#v", ordinal, chunk)
		}
		seen[chunk.ID] = true
	}
}

func TestWorkspaceIndexerRebuildsWhenEmbeddingModelChanges(t *testing.T) {
	root := t.TempDir()
	writeIndexFile(t, root, "main.go", "package sample\n\nfunc Current() {}\n")
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	chunkers := NewDefaultChunkerSet(ChunkConfig{})
	firstEmbedder := &recordingEmbedder{}
	first, err := NewWorkspaceIndexer(work, chunkers, firstEmbedder, store, WorkspaceIndexerConfig{EmbeddingModel: "embed-v1"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondEmbedder := &recordingEmbedder{}
	second, err := NewWorkspaceIndexer(work, chunkers, secondEmbedder, store, WorkspaceIndexerConfig{EmbeddingModel: "embed-v2"})
	if err != nil {
		t.Fatal(err)
	}
	report, err := second.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.RebuiltIndex || report.IndexedFiles != 1 || len(secondEmbedder.batches) == 0 {
		t.Fatalf("model-change report = %#v, batches = %v", report, secondEmbedder.batches)
	}
	metadata, exists, err := store.LoadIndexMetadata(context.Background())
	if err != nil || !exists || metadata.EmbeddingModel != "embed-v2" || metadata.EmbeddingDimensions != 2 {
		t.Fatalf("metadata = %#v, %t, %v", metadata, exists, err)
	}
}

func TestWorkspaceIndexerRebuildsWhenChunkerConfigurationChanges(t *testing.T) {
	root := t.TempDir()
	writeIndexFile(t, root, "notes.md", "# Notes\n\n"+strings.Repeat("content line\n", 20))
	work, err := workspace.New(root)
	if err != nil {
		t.Fatal(err)
	}
	store, err := NewSQLiteStore(filepath.Join(work.MetadataDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	first, err := NewWorkspaceIndexer(
		work,
		NewDefaultChunkerSet(ChunkConfig{MaxChunkBytes: 80, OverlapLines: 1}),
		&recordingEmbedder{},
		store,
		WorkspaceIndexerConfig{EmbeddingModel: "embed-v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := first.Index(context.Background()); err != nil {
		t.Fatal(err)
	}
	secondEmbedder := &recordingEmbedder{}
	second, err := NewWorkspaceIndexer(
		work,
		NewDefaultChunkerSet(ChunkConfig{MaxChunkBytes: 160, OverlapLines: 1}),
		secondEmbedder,
		store,
		WorkspaceIndexerConfig{EmbeddingModel: "embed-v1"},
	)
	if err != nil {
		t.Fatal(err)
	}
	report, err := second.Index(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !report.RebuiltIndex || report.IndexedFiles != 1 || len(secondEmbedder.batches) == 0 {
		t.Fatalf("chunker-change report = %#v, batches = %v", report, secondEmbedder.batches)
	}
}

func writeIndexFile(t *testing.T, root, relative, content string) {
	t.Helper()
	writeIndexBytes(t, root, relative, []byte(content))
}

func writeIndexBytes(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(relative))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatal(err)
	}
}

func indexedPaths(files []IndexedFile) []string {
	paths := make([]string, len(files))
	for index, file := range files {
		paths[index] = file.Path
	}
	return paths
}

func chunksContain(chunks []EmbeddedChunk, text string) bool {
	for _, chunk := range chunks {
		if strings.Contains(chunk.Content, text) {
			return true
		}
	}
	return false
}
