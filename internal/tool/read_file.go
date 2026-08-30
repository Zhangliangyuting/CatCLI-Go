package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"unicode/utf8"
)

const maxReadFileBytes int64 = 256 * 1024

func ReadFileDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "read_file",
			Description: "读取不超过 256 KiB 的 UTF-8 文本文件；不支持可执行文件、图片、压缩包等二进制文件。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "要读取的文件路径",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

func ReadFileHandler(
	ctx context.Context,
	args map[string]interface{},
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	pathValue, ok := args["path"].(string)
	if !ok || pathValue == "" {
		return "", fmt.Errorf("path is required")
	}

	unlock := fileLocks.rlock(pathValue)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	file, err := os.Open(pathValue)
	if err != nil {
		return "", err
	}
	defer file.Close()

	info, err := file.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() > maxReadFileBytes {
		return "", fmt.Errorf(
			"file is too large: %d bytes exceeds the %d-byte limit",
			info.Size(),
			maxReadFileBytes,
		)
	}

	// LimitReader also protects against a file growing after the Stat call.
	data, err := io.ReadAll(io.LimitReader(file, maxReadFileBytes+1))
	if err != nil {
		return "", err
	}
	if int64(len(data)) > maxReadFileBytes {
		return "", fmt.Errorf(
			"file is too large: exceeds the %d-byte limit",
			maxReadFileBytes,
		)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if isBinaryContent(data) {
		return "", fmt.Errorf("binary file is not supported: %s", pathValue)
	}

	return string(data), nil
}

func isBinaryContent(data []byte) bool {
	if bytes.IndexByte(data, 0) >= 0 || !utf8.Valid(data) {
		return true
	}

	// Reject terminal control bytes while allowing normal text whitespace.
	for _, value := range data {
		if (value < 0x20 && value != '\n' && value != '\r' && value != '\t') || value == 0x7f {
			return true
		}
	}
	return false
}
