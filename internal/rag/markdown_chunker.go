package rag

import (
	"path/filepath"
	"strings"
)

type MarkdownChunker struct {
	config ChunkConfig
}

func NewMarkdownChunker(config ChunkConfig) *MarkdownChunker {
	return &MarkdownChunker{config: config.normalized()}
}

func (*MarkdownChunker) Supports(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown":
		return true
	default:
		return false
	}
}

func (chunker *MarkdownChunker) Chunk(path string, content []byte) ([]Chunk, error) {
	if err := validateText(path, content); err != nil {
		return nil, err
	}
	lines := strings.Split(string(content), "\n")
	type section struct {
		start   int
		heading string
	}
	sections := []section{{start: 0}}
	headings := make([]string, 6)
	inFence := false
	fenceMarker := ""

	for index, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker := markdownFenceMarker(trimmed); marker != "" {
			if !inFence {
				inFence, fenceMarker = true, marker
			} else if marker == fenceMarker {
				inFence, fenceMarker = false, ""
			}
			continue
		}
		if inFence {
			continue
		}
		level, title, ok := markdownHeading(trimmed)
		if !ok {
			continue
		}
		headings[level-1] = title
		for deeper := level; deeper < len(headings); deeper++ {
			headings[deeper] = ""
		}
		pathParts := make([]string, 0, level)
		for _, heading := range headings[:level] {
			if heading != "" {
				pathParts = append(pathParts, heading)
			}
		}
		if index == 0 && len(sections) == 1 && sections[0].start == 0 && sections[0].heading == "" {
			sections[0].heading = strings.Join(pathParts, " > ")
		} else {
			sections = append(sections, section{start: index, heading: strings.Join(pathParts, " > ")})
		}
	}

	chunks := make([]Chunk, 0, len(sections))
	for index, section := range sections {
		end := len(lines)
		if index+1 < len(sections) {
			end = sections[index+1].start
		}
		kind := "section"
		if section.heading == "" {
			kind = "document"
		}
		chunks = append(chunks, chunksFromText(chunkMetadata{
			path: path, language: "markdown", kind: kind, heading: section.heading,
		}, strings.Join(lines[section.start:end], "\n"), section.start+1, chunker.config)...)
	}
	return chunks, nil
}

func markdownFenceMarker(line string) string {
	if strings.HasPrefix(line, "```") {
		return "```"
	}
	if strings.HasPrefix(line, "~~~") {
		return "~~~"
	}
	return ""
}

func markdownHeading(line string) (int, string, bool) {
	level := 0
	for level < len(line) && level < 6 && line[level] == '#' {
		level++
	}
	if level == 0 || level >= len(line) || line[level] != ' ' {
		return 0, "", false
	}
	title := strings.TrimSpace(strings.TrimRight(line[level+1:], "#"))
	return level, title, title != ""
}
