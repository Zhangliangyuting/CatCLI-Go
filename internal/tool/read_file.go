package tool

import (
	"fmt"
	"os"
)

func ReadFileDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "read_file",
			Description: "读取指定文件的内容",
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

func ReadFileHandler(args map[string]interface{}) (string, error) {
	pathValue, ok := args["path"].(string)
	if !ok || pathValue == "" {
		return "", fmt.Errorf("path is required")
	}

	data, err := os.ReadFile(pathValue)
	if err != nil {
		return "", err
	}

	return string(data), nil
}
