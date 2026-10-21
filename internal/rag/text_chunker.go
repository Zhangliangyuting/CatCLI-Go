package rag

import (
	"path/filepath"
	"strings"
)

type TextChunker struct {
	config ChunkConfig
}

func NewTextChunker(config ChunkConfig) *TextChunker {
	return &TextChunker{config: config.normalized()}
}

func (*TextChunker) Supports(string) bool {
	return true
}

func (chunker *TextChunker) Chunk(path string, content []byte) ([]Chunk, error) {
	if err := validateText(path, content); err != nil {
		return nil, err
	}
	language := strings.TrimPrefix(strings.ToLower(filepath.Ext(path)), ".")
	if language == "" {
		language = "text"
	}
	lines := strings.Split(string(content), "\n")
	chunks := make([]Chunk, 0)
	paragraphStart := -1
	for index := 0; index <= len(lines); index++ {
		blank := index == len(lines) || strings.TrimSpace(lines[index]) == ""
		if !blank && paragraphStart < 0 {
			paragraphStart = index
		}
		if !blank || paragraphStart < 0 {
			continue
		}
		chunks = append(chunks, chunksFromText(chunkMetadata{
			path: path, language: language, kind: "paragraph",
		}, strings.Join(lines[paragraphStart:index], "\n"), paragraphStart+1, chunker.config)...)
		paragraphStart = -1
	}
	return chunks, nil
}
