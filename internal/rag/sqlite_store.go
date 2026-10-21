package rag

import (
	"context"
	"database/sql"
	"encoding/binary"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

const (
	metadataEmbeddingModel      = "embedding_model"
	metadataEmbeddingDimensions = "embedding_dimensions"
	metadataChunkerVersion      = "chunker_version"
)

const sqliteSchema = `
CREATE TABLE IF NOT EXISTS rag_metadata (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE TABLE IF NOT EXISTS rag_files (
    path         TEXT PRIMARY KEY,
    content_hash TEXT NOT NULL,
    size         INTEGER NOT NULL,
    modified_at  INTEGER NOT NULL,
    indexed_at   INTEGER NOT NULL
);

CREATE TABLE IF NOT EXISTS rag_chunks (
    id           TEXT PRIMARY KEY,
    path         TEXT NOT NULL,
    ordinal      INTEGER NOT NULL,
    language     TEXT NOT NULL,
    kind         TEXT NOT NULL,
    symbol       TEXT NOT NULL,
    heading      TEXT NOT NULL,
    start_line   INTEGER NOT NULL,
    end_line     INTEGER NOT NULL,
    content      TEXT NOT NULL,
    content_hash TEXT NOT NULL,
    embedding    BLOB NOT NULL,
    FOREIGN KEY (path) REFERENCES rag_files(path) ON DELETE CASCADE,
    UNIQUE(path, ordinal)
);

CREATE INDEX IF NOT EXISTS idx_rag_chunks_path ON rag_chunks(path);
CREATE INDEX IF NOT EXISTS idx_rag_chunks_symbol ON rag_chunks(symbol);

INSERT INTO rag_metadata(key, value) VALUES ('schema_version', '1')
ON CONFLICT(key) DO NOTHING;
`

// SQLiteStore persists a workspace RAG index in one local SQLite database.
type SQLiteStore struct {
	db *sql.DB
}

var _ ChunkStore = (*SQLiteStore)(nil)

func NewSQLiteStore(path string) (*SQLiteStore, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, fmt.Errorf("SQLite store path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return nil, fmt.Errorf("resolve SQLite store path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absolute), 0o755); err != nil {
		return nil, fmt.Errorf("create SQLite store directory: %w", err)
	}

	db, err := sql.Open("sqlite", absolute)
	if err != nil {
		return nil, fmt.Errorf("open SQLite store: %w", err)
	}
	// A workspace index has one local writer. One pooled connection also keeps
	// connection-scoped PRAGMAs consistent for every operation.
	db.SetMaxOpenConns(1)
	store := &SQLiteStore{db: db}
	if err := store.initialize(context.Background()); err != nil {
		_ = db.Close()
		return nil, err
	}
	return store, nil
}

func (store *SQLiteStore) initialize(ctx context.Context) error {
	if store == nil || store.db == nil {
		return fmt.Errorf("SQLite store is not initialized")
	}
	for _, statement := range []string{
		"PRAGMA journal_mode = WAL",
		"PRAGMA foreign_keys = ON",
		"PRAGMA busy_timeout = 5000",
		sqliteSchema,
	} {
		if _, err := store.db.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("initialize SQLite store: %w", err)
		}
	}
	return nil
}

func (store *SQLiteStore) FileHash(ctx context.Context, path string) (string, bool, error) {
	if err := store.ready(); err != nil {
		return "", false, err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", false, fmt.Errorf("indexed file path is empty")
	}
	var hash string
	err := store.db.QueryRowContext(ctx,
		"SELECT content_hash FROM rag_files WHERE path = ?", path,
	).Scan(&hash)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, fmt.Errorf("load indexed file hash %q: %w", path, err)
	}
	return hash, true, nil
}

func (store *SQLiteStore) LoadIndexMetadata(ctx context.Context) (IndexMetadata, bool, error) {
	if err := store.ready(); err != nil {
		return IndexMetadata{}, false, err
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT key, value FROM rag_metadata
WHERE key IN (?, ?, ?)
`, metadataEmbeddingModel, metadataEmbeddingDimensions, metadataChunkerVersion)
	if err != nil {
		return IndexMetadata{}, false, fmt.Errorf("load RAG index metadata: %w", err)
	}
	defer rows.Close()
	values := make(map[string]string, 3)
	for rows.Next() {
		var key, value string
		if err := rows.Scan(&key, &value); err != nil {
			return IndexMetadata{}, false, fmt.Errorf("scan RAG index metadata: %w", err)
		}
		values[key] = value
	}
	if err := rows.Err(); err != nil {
		return IndexMetadata{}, false, fmt.Errorf("iterate RAG index metadata: %w", err)
	}
	if len(values) == 0 {
		return IndexMetadata{}, false, nil
	}
	if len(values) != 3 {
		return IndexMetadata{}, false, fmt.Errorf("RAG index metadata is incomplete")
	}
	dimensions, err := strconv.Atoi(values[metadataEmbeddingDimensions])
	if err != nil || dimensions < 0 {
		return IndexMetadata{}, false, fmt.Errorf("invalid RAG embedding dimensions %q", values[metadataEmbeddingDimensions])
	}
	return IndexMetadata{
		EmbeddingModel:      values[metadataEmbeddingModel],
		EmbeddingDimensions: dimensions,
		ChunkerVersion:      values[metadataChunkerVersion],
	}, true, nil
}

func (store *SQLiteStore) ResetIndex(ctx context.Context, metadata IndexMetadata) error {
	if err := store.ready(); err != nil {
		return err
	}
	if strings.TrimSpace(metadata.EmbeddingModel) == "" || strings.TrimSpace(metadata.ChunkerVersion) == "" {
		return fmt.Errorf("RAG index metadata is incomplete")
	}
	if metadata.EmbeddingDimensions < 0 {
		return fmt.Errorf("RAG embedding dimensions cannot be negative")
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin resetting RAG index: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.ExecContext(ctx, "DELETE FROM rag_files"); err != nil {
		return fmt.Errorf("clear RAG files: %w", err)
	}
	for key, value := range map[string]string{
		metadataEmbeddingModel:      metadata.EmbeddingModel,
		metadataEmbeddingDimensions: strconv.Itoa(metadata.EmbeddingDimensions),
		metadataChunkerVersion:      metadata.ChunkerVersion,
	} {
		if _, err := tx.ExecContext(ctx, `
INSERT INTO rag_metadata(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, key, value); err != nil {
			return fmt.Errorf("store RAG metadata %q: %w", key, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit RAG index reset: %w", err)
	}
	return nil
}

func (store *SQLiteStore) SetEmbeddingDimensions(ctx context.Context, dimensions int) error {
	if err := store.ready(); err != nil {
		return err
	}
	if dimensions <= 0 {
		return fmt.Errorf("RAG embedding dimensions must be positive")
	}
	if _, err := store.db.ExecContext(ctx, `
INSERT INTO rag_metadata(key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value
`, metadataEmbeddingDimensions, strconv.Itoa(dimensions)); err != nil {
		return fmt.Errorf("store RAG embedding dimensions: %w", err)
	}
	return nil
}

func (store *SQLiteStore) ListFiles(ctx context.Context) ([]IndexedFile, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT path, content_hash, size, modified_at
FROM rag_files
ORDER BY path
`)
	if err != nil {
		return nil, fmt.Errorf("list indexed files: %w", err)
	}
	defer rows.Close()

	var files []IndexedFile
	for rows.Next() {
		var file IndexedFile
		var modifiedAt int64
		if err := rows.Scan(&file.Path, &file.ContentHash, &file.Size, &modifiedAt); err != nil {
			return nil, fmt.Errorf("scan indexed file: %w", err)
		}
		file.ModifiedAt = time.Unix(0, modifiedAt)
		files = append(files, file)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate indexed files: %w", err)
	}
	return files, nil
}

func (store *SQLiteStore) ReplaceFile(ctx context.Context, file IndexedFile, chunks []EmbeddedChunk) error {
	if err := store.ready(); err != nil {
		return err
	}
	if err := validateIndexedFile(file); err != nil {
		return err
	}
	for index := range chunks {
		if err := validateEmbeddedChunk(file.Path, chunks[index]); err != nil {
			return fmt.Errorf("chunk %d: %w", index, err)
		}
	}

	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin replacing indexed file %q: %w", file.Path, err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
INSERT INTO rag_files(path, content_hash, size, modified_at, indexed_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT(path) DO UPDATE SET
    content_hash = excluded.content_hash,
    size = excluded.size,
    modified_at = excluded.modified_at,
    indexed_at = excluded.indexed_at
`, file.Path, file.ContentHash, file.Size, file.ModifiedAt.UnixNano(), time.Now().UnixNano()); err != nil {
		return fmt.Errorf("upsert indexed file %q: %w", file.Path, err)
	}
	if _, err := tx.ExecContext(ctx, "DELETE FROM rag_chunks WHERE path = ?", file.Path); err != nil {
		return fmt.Errorf("delete old chunks for %q: %w", file.Path, err)
	}
	statement, err := tx.PrepareContext(ctx, `
INSERT INTO rag_chunks(
    id, path, ordinal, language, kind, symbol, heading,
    start_line, end_line, content, content_hash, embedding
) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
`)
	if err != nil {
		return fmt.Errorf("prepare chunks for %q: %w", file.Path, err)
	}
	defer statement.Close()
	for _, chunk := range chunks {
		if _, err := statement.ExecContext(ctx,
			chunk.ID, chunk.Path, chunk.Ordinal, chunk.Language, chunk.Kind,
			chunk.Symbol, chunk.Heading, chunk.StartLine, chunk.EndLine,
			chunk.Content, chunk.ContentHash, encodeVector(chunk.Embedding),
		); err != nil {
			return fmt.Errorf("insert chunk %q: %w", chunk.ID, err)
		}
	}
	if err := statement.Close(); err != nil {
		return fmt.Errorf("close chunk statement for %q: %w", file.Path, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit indexed file %q: %w", file.Path, err)
	}
	return nil
}

func (store *SQLiteStore) DeleteFile(ctx context.Context, path string) error {
	if err := store.ready(); err != nil {
		return err
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return fmt.Errorf("indexed file path is empty")
	}
	if _, err := store.db.ExecContext(ctx, "DELETE FROM rag_files WHERE path = ?", path); err != nil {
		return fmt.Errorf("delete indexed file %q: %w", path, err)
	}
	return nil
}

func (store *SQLiteStore) LoadChunks(ctx context.Context) ([]EmbeddedChunk, error) {
	if err := store.ready(); err != nil {
		return nil, err
	}
	rows, err := store.db.QueryContext(ctx, `
SELECT id, path, ordinal, language, kind, symbol, heading,
       start_line, end_line, content, content_hash, embedding
FROM rag_chunks
ORDER BY path, ordinal
`)
	if err != nil {
		return nil, fmt.Errorf("load RAG chunks: %w", err)
	}
	defer rows.Close()

	var chunks []EmbeddedChunk
	for rows.Next() {
		var chunk EmbeddedChunk
		var encoded []byte
		if err := rows.Scan(
			&chunk.ID, &chunk.Path, &chunk.Ordinal, &chunk.Language,
			&chunk.Kind, &chunk.Symbol, &chunk.Heading, &chunk.StartLine,
			&chunk.EndLine, &chunk.Content, &chunk.ContentHash, &encoded,
		); err != nil {
			return nil, fmt.Errorf("scan RAG chunk: %w", err)
		}
		chunk.Embedding, err = decodeVector(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode embedding for chunk %q: %w", chunk.ID, err)
		}
		chunks = append(chunks, chunk)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate RAG chunks: %w", err)
	}
	return chunks, nil
}

func (store *SQLiteStore) Close() error {
	if store == nil || store.db == nil {
		return nil
	}
	return store.db.Close()
}

func (store *SQLiteStore) ready() error {
	if store == nil || store.db == nil {
		return fmt.Errorf("SQLite store is not initialized")
	}
	return nil
}

func validateIndexedFile(file IndexedFile) error {
	if strings.TrimSpace(file.Path) == "" {
		return fmt.Errorf("indexed file path is empty")
	}
	if strings.TrimSpace(file.ContentHash) == "" {
		return fmt.Errorf("indexed file content hash is empty")
	}
	if file.Size < 0 {
		return fmt.Errorf("indexed file size cannot be negative")
	}
	return nil
}

func validateEmbeddedChunk(path string, chunk EmbeddedChunk) error {
	if strings.TrimSpace(chunk.ID) == "" {
		return fmt.Errorf("ID is empty")
	}
	if chunk.Path != path {
		return fmt.Errorf("path %q does not match file path %q", chunk.Path, path)
	}
	if chunk.Ordinal < 0 {
		return fmt.Errorf("ordinal cannot be negative")
	}
	if strings.TrimSpace(chunk.Content) == "" {
		return fmt.Errorf("content is empty")
	}
	if strings.TrimSpace(chunk.ContentHash) == "" {
		return fmt.Errorf("content hash is empty")
	}
	if len(chunk.Embedding) == 0 {
		return fmt.Errorf("embedding is empty")
	}
	return nil
}

func encodeVector(vector []float32) []byte {
	encoded := make([]byte, len(vector)*4)
	for index, value := range vector {
		binary.LittleEndian.PutUint32(encoded[index*4:], math.Float32bits(value))
	}
	return encoded
}

func decodeVector(encoded []byte) ([]float32, error) {
	if len(encoded)%4 != 0 {
		return nil, fmt.Errorf("vector byte length %d is not divisible by 4", len(encoded))
	}
	vector := make([]float32, len(encoded)/4)
	for index := range vector {
		vector[index] = math.Float32frombits(binary.LittleEndian.Uint32(encoded[index*4:]))
	}
	return vector, nil
}
