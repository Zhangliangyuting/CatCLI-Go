package tool

import (
	"fmt"
	"os"
)

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

func WriteFileHandler(args map[string]interface{}) (string, error) {
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

	if _, err := os.Stat(filePath); err == nil && !overwrite {
		return "", fmt.Errorf("file already exists: %s, set overwrite=true to replace it", filePath)
	} else if err != nil && !os.IsNotExist(err) {
		return "", err
	}

	if err := os.WriteFile(filePath, []byte(content), 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("write_file completed, wrote %d bytes to %s", len(content), filePath), nil
}
