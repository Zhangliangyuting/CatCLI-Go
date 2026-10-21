package rag

import (
	"context"
	"path/filepath"
	"reflect"
	"testing"
	"time"
)

func TestSQLiteStorePersistsAndReopensChunks(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".catcli", "rag.db")
	store, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	file := IndexedFile{
		Path:        "internal/memory/manager.go",
		ContentHash: "file-hash-1",
		Size:        120,
		ModifiedAt:  time.Unix(100, 0),
	}
	want := []EmbeddedChunk{{
		Chunk: Chunk{
			Path:      file.Path,
			Language:  "go",
			Kind:      "method",
			Symbol:    "Manager.AddMessage",
			StartLine: 10,
			EndLine:   20,
			Content:   "func (manager *Manager) AddMessage() {}",
		},
		ID:          "chunk-1",
		Ordinal:     0,
		ContentHash: "chunk-hash-1",
		Embedding:   []float32{0.25, -0.5, 1.75},
	}}
	if err := store.ReplaceFile(context.Background(), file, want); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := NewSQLiteStore(path)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	hash, exists, err := reopened.FileHash(context.Background(), file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || hash != file.ContentHash {
		t.Fatalf("FileHash() = %q, %t; want %q, true", hash, exists, file.ContentHash)
	}
	files, err := reopened.ListFiles(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != file.Path || files[0].ContentHash != file.ContentHash ||
		files[0].Size != file.Size || !files[0].ModifiedAt.Equal(file.ModifiedAt) {
		t.Fatalf("ListFiles() = %#v, want %#v", files, file)
	}
	got, err := reopened.LoadChunks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("LoadChunks() = %#v, want %#v", got, want)
	}
}

func TestSQLiteStorePersistsAndResetsIndexMetadata(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	metadata := IndexMetadata{EmbeddingModel: "embed-v1", ChunkerVersion: "chunker-v1"}
	if err := store.ResetIndex(ctx, metadata); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEmbeddingDimensions(ctx, 768); err != nil {
		t.Fatal(err)
	}
	got, exists, err := store.LoadIndexMetadata(ctx)
	if err != nil {
		t.Fatal(err)
	}
	metadata.EmbeddingDimensions = 768
	if !exists || got != metadata {
		t.Fatalf("LoadIndexMetadata() = %#v, %t; want %#v, true", got, exists, metadata)
	}
	file := IndexedFile{Path: "main.go", ContentHash: "hash", Size: 1, ModifiedAt: time.Now()}
	if err := store.ReplaceFile(ctx, file, []EmbeddedChunk{embeddedChunk("one", file.Path, 0, "content", "chunk")}); err != nil {
		t.Fatal(err)
	}
	if err := store.ResetIndex(ctx, IndexMetadata{EmbeddingModel: "embed-v2", ChunkerVersion: "chunker-v2"}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.LoadChunks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(chunks) != 0 {
		t.Fatalf("chunks remain after reset: %#v", chunks)
	}
}

func TestSQLiteStoreReplacesAndDeletesFileChunks(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	file := IndexedFile{Path: "README.md", ContentHash: "old", Size: 10, ModifiedAt: time.Unix(1, 0)}
	old := embeddedChunk("old-chunk", file.Path, 0, "old content", "old-chunk-hash")
	if err := store.ReplaceFile(ctx, file, []EmbeddedChunk{old}); err != nil {
		t.Fatal(err)
	}

	file.ContentHash = "new"
	file.Size = 20
	newChunk := embeddedChunk("new-chunk", file.Path, 0, "new content", "new-chunk-hash")
	if err := store.ReplaceFile(ctx, file, []EmbeddedChunk{newChunk}); err != nil {
		t.Fatal(err)
	}
	chunks, err := store.LoadChunks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chunks, []EmbeddedChunk{newChunk}) {
		t.Fatalf("chunks after replacement = %#v", chunks)
	}

	if err := store.DeleteFile(ctx, file.Path); err != nil {
		t.Fatal(err)
	}
	if chunks, err := store.LoadChunks(ctx); err != nil || len(chunks) != 0 {
		t.Fatalf("chunks after deletion = %#v, error = %v", chunks, err)
	}
	if _, exists, err := store.FileHash(ctx, file.Path); err != nil || exists {
		t.Fatalf("FileHash() after deletion exists = %t, error = %v", exists, err)
	}
}

func TestSQLiteStoreReplaceFileRollsBackOnChunkConflict(t *testing.T) {
	store, err := NewSQLiteStore(filepath.Join(t.TempDir(), "rag.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer store.Close()
	ctx := context.Background()
	file := IndexedFile{Path: "main.go", ContentHash: "original", Size: 10, ModifiedAt: time.Unix(1, 0)}
	original := embeddedChunk("existing", file.Path, 0, "original", "original-chunk")
	if err := store.ReplaceFile(ctx, file, []EmbeddedChunk{original}); err != nil {
		t.Fatal(err)
	}

	file.ContentHash = "replacement"
	first := embeddedChunk("duplicate", file.Path, 0, "first", "first-hash")
	second := embeddedChunk("duplicate", file.Path, 1, "second", "second-hash")
	if err := store.ReplaceFile(ctx, file, []EmbeddedChunk{first, second}); err == nil {
		t.Fatal("ReplaceFile() error = nil, want duplicate ID error")
	}
	hash, exists, err := store.FileHash(ctx, file.Path)
	if err != nil {
		t.Fatal(err)
	}
	if !exists || hash != "original" {
		t.Fatalf("hash after rollback = %q, %t", hash, exists)
	}
	chunks, err := store.LoadChunks(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(chunks, []EmbeddedChunk{original}) {
		t.Fatalf("chunks after rollback = %#v", chunks)
	}
}

func embeddedChunk(id, path string, ordinal int, content, hash string) EmbeddedChunk {
	return EmbeddedChunk{
		Chunk: Chunk{
			Path:      path,
			Language:  "text",
			Kind:      "paragraph",
			StartLine: ordinal + 1,
			EndLine:   ordinal + 1,
			Content:   content,
		},
		ID:          id,
		Ordinal:     ordinal,
		ContentHash: hash,
		Embedding:   []float32{float32(ordinal + 1), 0.5},
	}
}
