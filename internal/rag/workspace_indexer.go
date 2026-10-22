package rag

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"AgentCLI/internal/llm"
	"AgentCLI/internal/workspace"
)

const (
	DefaultMaxIndexFileBytes  int64 = 1 << 20
	DefaultEmbeddingBatchSize       = 64
)

var ignoredIndexDirectories = map[string]bool{
	".catcli":      true,
	".git":         true,
	".idea":        true,
	".vscode":      true,
	"node_modules": true,
	"vendor":       true,
}

var ignoredIndexExtensions = map[string]bool{
	".db":       true,
	".jks":      true,
	".key":      true,
	".keystore": true,
	".p12":      true,
	".pem":      true,
	".pfx":      true,
	".sqlite":   true,
	".sqlite3":  true,
}

type WorkspaceIndexerConfig struct {
	MaxFileBytes       int64
	EmbeddingBatchSize int
	EmbeddingModel     string
}

func (config WorkspaceIndexerConfig) normalized() WorkspaceIndexerConfig {
	if config.MaxFileBytes <= 0 {
		config.MaxFileBytes = DefaultMaxIndexFileBytes
	}
	if config.EmbeddingBatchSize <= 0 {
		config.EmbeddingBatchSize = DefaultEmbeddingBatchSize
	}
	return config
}

type IndexReport struct {
	ScannedFiles   int
	IndexedFiles   int
	UnchangedFiles int
	SkippedFiles   int
	DeletedFiles   int
	Chunks         int
	EmbeddedChunks int
	ReusedChunks   int
	RebuiltIndex   bool
}

// WorkspaceIndexer keeps one workspace's persisted RAG index synchronized
// with the current text files on disk.
type WorkspaceIndexer struct {
	workspace *workspace.Workspace
	chunkers  *ChunkerSet
	embedder  llm.Embedder
	store     ChunkStore
	config    WorkspaceIndexerConfig
}

func NewWorkspaceIndexer(
	work *workspace.Workspace,
	chunkers *ChunkerSet,
	embedder llm.Embedder,
	store ChunkStore,
	config WorkspaceIndexerConfig,
) (*WorkspaceIndexer, error) {
	if work == nil || strings.TrimSpace(work.Root()) == "" {
		return nil, fmt.Errorf("workspace is not initialized")
	}
	if chunkers == nil {
		return nil, fmt.Errorf("chunker set is nil")
	}
	if embedder == nil {
		return nil, fmt.Errorf("embedder is nil")
	}
	if store == nil {
		return nil, fmt.Errorf("chunk store is nil")
	}
	config = config.normalized()
	if strings.TrimSpace(config.EmbeddingModel) == "" {
		return nil, fmt.Errorf("embedding model is empty")
	}
	if strings.TrimSpace(chunkers.Version()) == "" {
		return nil, fmt.Errorf("chunker version is empty")
	}
	return &WorkspaceIndexer{
		workspace: work,
		chunkers:  chunkers,
		embedder:  embedder,
		store:     store,
		config:    config,
	}, nil
}

func (indexer *WorkspaceIndexer) Index(ctx context.Context) (IndexReport, error) {
	var report IndexReport
	if indexer == nil {
		return report, fmt.Errorf("workspace indexer is nil")
	}
	if err := ctx.Err(); err != nil {
		return report, err
	}

	metadata, metadataExists, err := indexer.store.LoadIndexMetadata(ctx)
	if err != nil {
		return report, fmt.Errorf("load workspace index metadata: %w", err)
	}
	desiredMetadata := IndexMetadata{
		EmbeddingModel: strings.TrimSpace(indexer.config.EmbeddingModel),
		ChunkerVersion: indexer.chunkers.Version(),
	}
	if !metadataExists || metadata.EmbeddingModel != desiredMetadata.EmbeddingModel ||
		metadata.ChunkerVersion != desiredMetadata.ChunkerVersion {
		if err := indexer.store.ResetIndex(ctx, desiredMetadata); err != nil {
			return report, fmt.Errorf("reset incompatible workspace index: %w", err)
		}
		metadata = desiredMetadata
		report.RebuiltIndex = true
	}

	storedFiles, err := indexer.store.ListFiles(ctx)
	if err != nil {
		return report, fmt.Errorf("list existing workspace index: %w", err)
	}
	storedByPath := make(map[string]IndexedFile, len(storedFiles))
	for _, file := range storedFiles {
		storedByPath[file.Path] = file
	}

	paths, skipped, err := indexer.scan(ctx)
	report.SkippedFiles += skipped
	if err != nil {
		return report, err
	}
	current := make(map[string]bool, len(paths))
	embeddingDimensions := metadata.EmbeddingDimensions
	for _, candidate := range paths {
		if err := ctx.Err(); err != nil {
			return report, err
		}
		report.ScannedFiles++
		file, content, skipped, err := indexer.readFile(ctx, candidate)
		if err != nil {
			return report, err
		}
		if skipped {
			report.SkippedFiles++
			continue
		}
		current[file.Path] = true
		if stored, ok := storedByPath[file.Path]; ok && stored.ContentHash == file.ContentHash {
			report.UnchangedFiles++
			continue
		}
		chunks, err := indexer.chunkers.Chunk(file.Path, content)
		if err != nil {
			return report, fmt.Errorf("chunk workspace file %q: %w", file.Path, err)
		}
		var previous []EmbeddedChunk
		if _, exists := storedByPath[file.Path]; exists {
			previous, err = indexer.store.LoadFileChunks(ctx, file.Path)
			if err != nil {
				return report, fmt.Errorf("load previous chunks for %q: %w", file.Path, err)
			}
		}
		embedded, dimensions, reused, err := indexer.embedChunks(ctx, chunks, embeddingDimensions, previous)
		if err != nil {
			return report, fmt.Errorf("embed workspace file %q: %w", file.Path, err)
		}
		if embeddingDimensions == 0 && dimensions > 0 {
			embeddingDimensions = dimensions
			if err := indexer.store.SetEmbeddingDimensions(ctx, dimensions); err != nil {
				return report, fmt.Errorf("store embedding dimensions: %w", err)
			}
		}
		if err := indexer.store.ReplaceFile(ctx, file, embedded); err != nil {
			return report, fmt.Errorf("store indexed file %q: %w", file.Path, err)
		}
		report.IndexedFiles++
		report.Chunks += len(embedded)
		report.ReusedChunks += reused
		report.EmbeddedChunks += len(embedded) - reused
	}

	for _, stored := range storedFiles {
		if current[stored.Path] {
			continue
		}
		if err := indexer.store.DeleteFile(ctx, stored.Path); err != nil {
			return report, fmt.Errorf("delete stale indexed file %q: %w", stored.Path, err)
		}
		report.DeletedFiles++
	}
	return report, nil
}

func (indexer *WorkspaceIndexer) scan(ctx context.Context) ([]string, int, error) {
	paths := make([]string, 0)
	skipped := 0
	err := filepath.WalkDir(indexer.workspace.Root(), func(path string, entry fs.DirEntry, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		if walkErr != nil {
			return fmt.Errorf("scan workspace path %q: %w", path, walkErr)
		}
		if path == indexer.workspace.Root() {
			return nil
		}
		if entry.IsDir() {
			if ignoredIndexDirectories[entry.Name()] {
				return filepath.SkipDir
			}
			return nil
		}
		if entry.Type()&os.ModeSymlink != 0 || !entry.Type().IsRegular() {
			skipped++
			return nil
		}
		if ignoreIndexFile(entry.Name()) {
			skipped++
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("stat workspace file %q: %w", path, err)
		}
		if info.Size() > indexer.config.MaxFileBytes {
			skipped++
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, skipped, err
	}
	sort.Strings(paths)
	return paths, skipped, nil
}

func (indexer *WorkspaceIndexer) readFile(
	ctx context.Context,
	absolutePath string,
) (IndexedFile, []byte, bool, error) {
	if err := ctx.Err(); err != nil {
		return IndexedFile{}, nil, false, err
	}
	info, err := os.Stat(absolutePath)
	if err != nil {
		return IndexedFile{}, nil, false, fmt.Errorf("stat workspace file %q: %w", absolutePath, err)
	}
	content, err := os.ReadFile(absolutePath)
	if err != nil {
		return IndexedFile{}, nil, false, fmt.Errorf("read workspace file %q: %w", absolutePath, err)
	}
	if int64(len(content)) > indexer.config.MaxFileBytes || !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return IndexedFile{}, nil, true, nil
	}
	relativePath, err := filepath.Rel(indexer.workspace.Root(), absolutePath)
	if err != nil {
		return IndexedFile{}, nil, false, fmt.Errorf("resolve indexed path %q: %w", absolutePath, err)
	}
	relativePath = filepath.ToSlash(relativePath)
	file := IndexedFile{
		Path:        relativePath,
		ContentHash: hashText(content),
		Size:        int64(len(content)),
		ModifiedAt:  info.ModTime(),
	}
	return file, content, false, nil
}

func (indexer *WorkspaceIndexer) embedChunks(
	ctx context.Context,
	chunks []Chunk,
	expectedDimension int,
	previous []EmbeddedChunk,
) ([]EmbeddedChunk, int, int, error) {
	if err := ctx.Err(); err != nil {
		return nil, expectedDimension, 0, err
	}
	if len(chunks) == 0 {
		return nil, expectedDimension, 0, nil
	}
	embedded := make([]EmbeddedChunk, len(chunks))
	dimension := expectedDimension
	// Previous chunks are from this file and the currently compatible index.
	// Reuse by content rather than ID: ordinal and source lines may have moved.
	cached := make(map[string][]float32, len(previous))
	for _, chunk := range previous {
		if dimension > 0 && len(chunk.Embedding) == dimension &&
			chunk.ContentHash == hashText([]byte(chunk.Content)) {
			cached[chunk.ContentHash] = chunk.Embedding
		}
	}
	reused := 0
	pending := make([]int, 0, len(chunks))
	for ordinal, chunk := range chunks {
		contentHash := hashText([]byte(chunk.Content))
		embedded[ordinal] = EmbeddedChunk{
			Chunk: chunk, ID: chunkID(chunk.Path, ordinal, contentHash),
			Ordinal: ordinal, ContentHash: contentHash,
		}
		if vector, exists := cached[contentHash]; exists {
			embedded[ordinal].Embedding = vector
			reused++
		} else {
			pending = append(pending, ordinal)
		}
	}
	for start := 0; start < len(pending); start += indexer.config.EmbeddingBatchSize {
		end := min(start+indexer.config.EmbeddingBatchSize, len(pending))
		texts := make([]string, end-start)
		for index := start; index < end; index++ {
			texts[index-start] = chunks[pending[index]].Content
		}
		vectors, err := indexer.embedder.EmbedBatch(ctx, texts)
		if err != nil {
			return nil, dimension, reused, err
		}
		if len(vectors) != len(texts) {
			return nil, dimension, reused, fmt.Errorf("embedding batch returned %d vectors for %d chunks", len(vectors), len(texts))
		}
		for offset, vector := range vectors {
			ordinal := pending[start+offset]
			if len(vector) == 0 {
				return nil, dimension, reused, fmt.Errorf("embedding for chunk %d is empty", ordinal)
			}
			if dimension == 0 {
				dimension = len(vector)
			} else if len(vector) != dimension {
				return nil, dimension, reused, fmt.Errorf("embedding for chunk %d has dimension %d, want %d", ordinal, len(vector), dimension)
			}
			embedded[ordinal].Embedding = vector
		}
	}
	return embedded, dimension, reused, nil
}

func ignoreIndexFile(name string) bool {
	lower := strings.ToLower(name)
	if lower == ".env" || strings.HasPrefix(lower, ".env.") ||
		lower == "id_rsa" || lower == "id_ed25519" {
		return true
	}
	return ignoredIndexExtensions[strings.ToLower(filepath.Ext(name))]
}

func hashText(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func chunkID(path string, ordinal int, contentHash string) string {
	digest := sha256.Sum256([]byte(path + "\x00" + strconv.Itoa(ordinal) + "\x00" + contentHash))
	return hex.EncodeToString(digest[:])
}
