package tool

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"strings"
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
			Description: "写入完整文件内容。已有内容与请求相同时直接成功，不再写入。内容不同时，覆盖须设置 overwrite=true，并提供最近一次 read_file 返回的整文件哈希作为 expected_hash。哈希不匹配时先重新读取。",
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
					"expected_hash": map[string]interface{}{
						"type":        "string",
						"description": "覆盖已有文件时必填：读取该文件得到的 full_file_sha256；新建文件不填",
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
	expected, err := expectedFileHash(args)
	if err != nil {
		return "", err
	}

	// Keep the existence check and write in one critical section.
	unlock := fileLocks.lock(filePath)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	info, err := os.Stat(filePath)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return "", fmt.Errorf("expected a regular file: %s", filePath)
		}
		actual, err := fileSHA256(ctx, filePath)
		if err != nil {
			return "", err
		}
		if actual == contentHash([]byte(content)) {
			return fmt.Sprintf("write_file completed, content already matches %s; no write performed", filePath), nil
		}
		if !overwrite {
			return "", fmt.Errorf("file already exists: %s, set overwrite=true to replace it", filePath)
		}
		if expected == "" {
			return "", fmt.Errorf("expected_hash is required to overwrite an existing file; read it again first")
		}
		if actual != expected {
			return "", fmt.Errorf("file changed since read: expected_hash does not match; read %s again", filePath)
		}
	case os.IsNotExist(err):
		if expected != "" {
			return "", fmt.Errorf("file is missing: expected_hash requires an existing file; read %s again", filePath)
		}
	case err != nil:
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := atomicWriteFile(ctx, filePath, []byte(content), info == nil); err != nil {
		return "", err
	}

	return fmt.Sprintf("write_file completed, wrote %d bytes to %s", len(content), filePath), nil
}

// expectedFileHash is supplied by a prior read of the same file. An absent
// value means create-if-absent, never permission to overwrite an existing file.
func expectedFileHash(args map[string]any) (string, error) {
	raw, exists := args["expected_hash"]
	if !exists {
		return "", nil
	}
	value, ok := raw.(string)
	if !ok || len(value) != 64 {
		return "", fmt.Errorf("expected_hash must be a 64-character SHA-256 hex string")
	}
	if _, err := hex.DecodeString(value); err != nil {
		return "", fmt.Errorf("expected_hash must be a 64-character SHA-256 hex string")
	}
	return strings.ToLower(value), nil
}

func fileSHA256(ctx context.Context, path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	buf := make([]byte, 32*1024)
	for {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		n, err := f.Read(buf)
		if n > 0 {
			_, _ = h.Write(buf[:n])
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("%x", h.Sum(nil)), nil
}
