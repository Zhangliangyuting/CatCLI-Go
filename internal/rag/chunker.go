package rag

import (
	"fmt"
	"strconv"
	"strings"
	"unicode/utf8"
)

const ChunkerSchemaVersion = 1

const (
	DefaultMaxChunkBytes = 12 * 1024
	DefaultOverlapLines  = 3
)

type ChunkConfig struct {
	MaxChunkBytes int
	OverlapLines  int
}

func (config ChunkConfig) normalized() ChunkConfig {
	if config.MaxChunkBytes <= 0 {
		config.MaxChunkBytes = DefaultMaxChunkBytes
	}
	if config.OverlapLines == 0 {
		config.OverlapLines = DefaultOverlapLines
	} else if config.OverlapLines < 0 {
		config.OverlapLines = 0
	}
	return config
}

type Chunker interface {
	Supports(path string) bool
	Chunk(path string, content []byte) ([]Chunk, error)
}

type ChunkerSet struct {
	chunkers []Chunker
	version  string
}

func NewDefaultChunkerSet(config ChunkConfig) *ChunkerSet {
	// Normalize separately for the version; constructors receive the raw config.
	versionConfig := config.normalized()
	return &ChunkerSet{chunkers: []Chunker{
		NewGoASTChunker(config),
		NewMarkdownChunker(config),
		NewTextChunker(config),
	}, version: strconv.Itoa(ChunkerSchemaVersion) + ":" + strconv.Itoa(versionConfig.MaxChunkBytes) + ":" + strconv.Itoa(versionConfig.OverlapLines)}
}

func (set *ChunkerSet) Version() string {
	if set == nil {
		return ""
	}
	return set.version
}

func (set *ChunkerSet) Chunk(path string, content []byte) ([]Chunk, error) {
	if set == nil {
		return nil, fmt.Errorf("chunker set is nil")
	}
	for _, chunker := range set.chunkers {
		if chunker.Supports(path) {
			return chunker.Chunk(path, content)
		}
	}
	return nil, fmt.Errorf("no chunker supports %s", path)
}

type chunkMetadata struct {
	path     string
	language string
	kind     string
	symbol   string
	heading  string
}

// chunksFromText expects config to have been normalized by the chunker constructor.
func chunksFromText(
	metadata chunkMetadata,
	content string,
	startLine int,
	config ChunkConfig,
) []Chunk {
	if strings.TrimSpace(content) == "" {
		return nil
	}
	lines := strings.Split(content, "\n")
	chunks := make([]Chunk, 0, 1)
	for start := 0; start < len(lines); {
		end := start
		bytes := 0
		for end < len(lines) {
			lineBytes := len(lines[end])
			if end > start {
				lineBytes++
			}
			if end > start && bytes+lineBytes > config.MaxChunkBytes {
				break
			}
			bytes += lineBytes
			end++
		}
		if end == start {
			end++
		}

		text := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		if text != "" {
			chunks = append(chunks, Chunk{
				Path:      metadata.path,
				Language:  metadata.language,
				Kind:      metadata.kind,
				Symbol:    metadata.symbol,
				Heading:   metadata.heading,
				StartLine: startLine + start,
				EndLine:   startLine + end - 1,
				Content:   text,
			})
		}

		if end >= len(lines) {
			break
		}
		next := end - config.OverlapLines
		if next <= start {
			next = end
		}
		start = next
	}
	return chunks
}

func validateText(path string, content []byte) error {
	if !utf8.Valid(content) {
		return fmt.Errorf("chunk %s: content is not valid UTF-8", path)
	}
	return nil
}
