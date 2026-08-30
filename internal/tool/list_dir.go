package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
)

func ListDirDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "list_dir",
			Description: "列出指定目录下的文件和文件夹",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"path": map[string]interface{}{
						"type":        "string",
						"description": "要列出的目录路径",
					},
				},
				"required": []string{"path"},
			},
		},
	}
}

func ListDirHandler(
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

	entries, err := os.ReadDir(pathValue)
	if err != nil {
		return "", err
	}

	var result []map[string]interface{}

	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		entryType := "file"
		if entry.IsDir() {
			entryType = "dir"
		}

		result = append(result, map[string]interface{}{
			"name": entry.Name(),
			"type": entryType,
		})
	}

	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
