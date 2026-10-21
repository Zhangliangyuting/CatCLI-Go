package rag

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
)

type GoASTChunker struct {
	config ChunkConfig
}

func NewGoASTChunker(config ChunkConfig) *GoASTChunker {
	return &GoASTChunker{config: config.normalized()}
}

func (*GoASTChunker) Supports(path string) bool {
	return strings.EqualFold(filepath.Ext(path), ".go")
}

func (chunker *GoASTChunker) Chunk(path string, content []byte) ([]Chunk, error) {
	if err := validateText(path, content); err != nil {
		return nil, err
	}
	files := token.NewFileSet()
	file, err := parser.ParseFile(files, path, content, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("parse Go file %s: %w", path, err)
	}

	chunks := make([]Chunk, 0, len(file.Decls)+1)
	headerEnd := file.Name.End()
	for _, declaration := range file.Decls {
		general, ok := declaration.(*ast.GenDecl)
		if ok && general.Tok == token.IMPORT {
			headerEnd = general.End()
		}
	}
	headerStart := file.Pos()
	if parsedFile := files.File(file.Pos()); parsedFile != nil {
		headerStart = parsedFile.Pos(0)
	}
	chunks = append(chunks, chunker.nodeChunks(
		path,
		content,
		files,
		headerStart,
		headerEnd,
		"file_header",
		file.Name.Name,
	)...)

	for _, declaration := range file.Decls {
		kind, symbol, include := goDeclarationMetadata(files, declaration)
		if !include {
			continue
		}
		start := declaration.Pos()
		switch typed := declaration.(type) {
		case *ast.FuncDecl:
			if typed.Doc != nil {
				start = typed.Doc.Pos()
			}
		case *ast.GenDecl:
			if typed.Doc != nil {
				start = typed.Doc.Pos()
			}
		}
		chunks = append(chunks, chunker.nodeChunks(
			path,
			content,
			files,
			start,
			declaration.End(),
			kind,
			symbol,
		)...)
	}
	return chunks, nil
}

func (chunker *GoASTChunker) nodeChunks(
	path string,
	content []byte,
	files *token.FileSet,
	start token.Pos,
	end token.Pos,
	kind string,
	symbol string,
) []Chunk {
	file := files.File(start)
	if file == nil {
		return nil
	}
	startOffset := file.Offset(start)
	endOffset := file.Offset(end)
	if startOffset < 0 || endOffset < startOffset || endOffset > len(content) {
		return nil
	}
	return chunksFromText(chunkMetadata{
		path: path, language: "go", kind: kind, symbol: symbol,
	}, string(content[startOffset:endOffset]), files.Position(start).Line, chunker.config)
}

func goDeclarationMetadata(files *token.FileSet, declaration ast.Decl) (string, string, bool) {
	switch typed := declaration.(type) {
	case *ast.FuncDecl:
		if typed.Recv == nil || len(typed.Recv.List) == 0 {
			return "function", typed.Name.Name, true
		}
		return "method", receiverName(files, typed.Recv.List[0].Type) + "." + typed.Name.Name, true
	case *ast.GenDecl:
		if typed.Tok == token.IMPORT {
			return "", "", false
		}
		names := make([]string, 0, len(typed.Specs))
		for _, specification := range typed.Specs {
			switch value := specification.(type) {
			case *ast.TypeSpec:
				names = append(names, value.Name.Name)
			case *ast.ValueSpec:
				for _, name := range value.Names {
					names = append(names, name.Name)
				}
			}
		}
		return strings.ToLower(typed.Tok.String()), strings.Join(names, ","), true
	default:
		return "declaration", "", true
	}
}

func receiverName(files *token.FileSet, expression ast.Expr) string {
	var output bytes.Buffer
	if err := format.Node(&output, files, expression); err != nil {
		return "receiver"
	}
	return strings.TrimPrefix(output.String(), "*")
}
