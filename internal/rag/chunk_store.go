package rag

import (
	"context"
	"time"
)

// IndexedFile records the state used to decide whether a workspace file must
// be chunked and embedded again.
type IndexedFile struct {
	Path        string
	ContentHash string
	Size        int64
	ModifiedAt  time.Time
}

// EmbeddedChunk is a retrievable chunk and its persisted vector.
type EmbeddedChunk struct {
	Chunk
	ID          string
	Ordinal     int
	ContentHash string
	Embedding   []float32
}

type IndexMetadata struct {
	EmbeddingModel      string
	EmbeddingDimensions int
	ChunkerVersion      string
}

// ChunkReader is the read-only portion of a persisted RAG index.
type ChunkReader interface {
	LoadChunks(ctx context.Context) ([]EmbeddedChunk, error)
}

// ChunkStore persists the workspace files, chunks, and embeddings used by RAG.
type ChunkStore interface {
	ChunkReader
	LoadIndexMetadata(ctx context.Context) (metadata IndexMetadata, exists bool, err error)
	ResetIndex(ctx context.Context, metadata IndexMetadata) error
	SetEmbeddingDimensions(ctx context.Context, dimensions int) error
	FileHash(ctx context.Context, path string) (hash string, exists bool, err error)
	ListFiles(ctx context.Context) ([]IndexedFile, error)
	LoadFileChunks(ctx context.Context, path string) ([]EmbeddedChunk, error)
	ReplaceFile(ctx context.Context, file IndexedFile, chunks []EmbeddedChunk) error
	DeleteFile(ctx context.Context, path string) error
	Close() error
}
