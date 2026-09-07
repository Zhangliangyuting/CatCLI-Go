package tool

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"unicode/utf8"
)

const maxReadFileBytes int64 = 256 * 1024
const defaultReadChunkBytes = 16 * 1024
const maxReadChunkBytes = 64 * 1024

func ReadFileDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "read_file",
			Description: "分页读取不超过 256 KiB 的 UTF-8 文本文件；默认返回 16 KiB，并在未读完时提供 next_offset。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "要读取的文件路径",
					},
					"offset": map[string]interface{}{
						"type":        "integer",
						"minimum":     0,
						"description": "从哪个 UTF-8 字节偏移开始读取；默认 0",
					},
					"limit": map[string]interface{}{
						"type":        "integer",
						"minimum":     1,
						"maximum":     maxReadChunkBytes,
						"description": "本次最多读取的字节数；默认 16384，最大 65536",
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
	offset, err := readFileIntegerArgument(args, "offset", 0, 0)
	if err != nil {
		return "", err
	}
	limit, err := readFileIntegerArgument(args, "limit", defaultReadChunkBytes, maxReadChunkBytes)
	if err != nil {
		return "", err
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
	if offset > len(data) {
		return "", fmt.Errorf("offset %d exceeds file size %d", offset, len(data))
	}
	if offset < len(data) && !utf8.RuneStart(data[offset]) {
		return "", fmt.Errorf("offset %d is not at a UTF-8 character boundary", offset)
	}
	end := offset + limit
	if end > len(data) {
		end = len(data)
	}
	for end > offset && !utf8.Valid(data[offset:end]) {
		end--
	}
	chunk := string(data[offset:end])
	if end < len(data) {
		chunk += fmt.Sprintf(
			"\n\n[catcli: bytes %d-%d of %d; continue with offset=%d]",
			offset,
			end,
			len(data),
			end,
		)
	}

	return chunk, nil
}

func readFileIntegerArgument(
	args map[string]interface{},
	name string,
	defaultValue int,
	maximum int,
) (int, error) {
	value, exists := args[name]
	if !exists {
		return defaultValue, nil
	}
	numeric, ok := value.(float64)
	if !ok || math.Trunc(numeric) != numeric || numeric < 0 {
		return 0, fmt.Errorf("%s must be a non-negative integer", name)
	}
	maxInt := int(^uint(0) >> 1)
	if numeric > float64(maxInt) {
		return 0, fmt.Errorf("%s is too large", name)
	}
	integer := int(numeric)
	if name == "limit" && integer == 0 {
		return 0, fmt.Errorf("limit must be positive")
	}
	if maximum > 0 && integer > maximum {
		return 0, fmt.Errorf("%s must not exceed %d", name, maximum)
	}
	return integer, nil
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
