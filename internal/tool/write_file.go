package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

// verifyWriteFile reads through the same workspace boundary and file lock as
// the built-in writer. It never writes or infers non-execution from a mismatch.
func (p BuiltinProvider) verifyWriteFile(ctx context.Context, args map[string]any, _ string) (*Verification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, fmt.Errorf("content is required")
	}
	path, err := p.workspace.Resolve(path)
	if err != nil {
		return nil, err
	}
	unlock := fileLocks.rlock(path)
	defer unlock()
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() != int64(len(content)) {
		return nil, nil
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	// Bound reads even if another process grows the file after Stat.
	data, err := io.ReadAll(io.LimitReader(f, int64(len(content))+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if !bytes.Equal(data, []byte(content)) {
		return nil, nil
	}
	sum := sha256.Sum256(data)
	return &Verification{
		Evidence: fmt.Sprintf("write_file postcondition verified: path=%q bytes=%d sha256=%x; current full content matches requested content; historical execution is not established", path, len(data), sum),
		Result:   fmt.Sprintf("write_file recovery verified: current file content already matches requested content (%d bytes); no write performed", len(data)),
	}, nil
}

func WriteFileDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "write_file",
			Description: "写入完整文件内容。默认只创建新文件；如果文件已存在，必须显式设置 overwrite=true 才会覆盖。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "要写入的文件路径",
					},
					"content": map[string]interface{}{
						"type":        "string",
						"description": "要写入文件的完整内容",
					},
					"overwrite": map[string]interface{}{
						"type":        "boolean",
						"description": "文件已存在时是否允许覆盖，默认 false",
					},
				},
				"required": []string{"file_path", "content"},
			},
		},
	}
}

func WriteFileHandler(
	ctx context.Context,
	args map[string]interface{},
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	content, ok := args["content"].(string)
	if !ok {
		return "", fmt.Errorf("content is required")
	}

	overwrite := false
	if value, ok := args["overwrite"].(bool); ok {
		overwrite = value
	}

	// Keep the existence check and write in one critical section.
	unlock := fileLocks.lock(filePath)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	if _, err := os.Stat(filePath); err == nil && !overwrite {
		return "", fmt.Errorf("file already exists: %s, set overwrite=true to replace it", filePath)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("write_file completed, wrote %d bytes to %s", len(content), filePath), nil
}
