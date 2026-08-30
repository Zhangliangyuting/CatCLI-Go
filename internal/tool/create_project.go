package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func CreateProjectDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "create_project",
			Description: "根据文件列表创建一个项目结构，包括目录和文件。默认不覆盖已有文件。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"root": map[string]interface{}{
						"type":        "string",
						"description": "项目根目录",
					},
					"files": map[string]interface{}{
						"type":        "array",
						"description": "要创建的文件列表",
						"items": map[string]interface{}{
							"type": "object",
							"properties": map[string]interface{}{
								"path": map[string]interface{}{
									"type":        "string",
									"description": "相对 root 的文件路径",
								},
								"content": map[string]interface{}{
									"type":        "string",
									"description": "文件内容",
								},
							},
							"required": []string{"path", "content"},
						},
					},
					"overwrite": map[string]interface{}{
						"type":        "boolean",
						"description": "是否覆盖已有文件，默认 false",
					},
				},
				"required": []string{"root", "files"},
			},
		},
	}
}

func CreateProjectHandler(
	ctx context.Context,
	args map[string]interface{},
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	root, ok := args["root"].(string)
	if !ok || root == "" {
		return "", fmt.Errorf("root is required")
	}

	rawFiles, ok := args["files"].([]interface{})
	if !ok || len(rawFiles) == 0 {
		return "", fmt.Errorf("files is required")
	}

	overwrite := false
	if value, ok := args["overwrite"].(bool); ok {
		overwrite = value
	}

	createdFiles := 0

	for _, rawFile := range rawFiles {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		fileMap, ok := rawFile.(map[string]interface{})
		if !ok {
			return "", fmt.Errorf("invalid file item")
		}

		relativePath, ok := fileMap["path"].(string)
		if !ok || relativePath == "" {
			return "", fmt.Errorf("file path is required")
		}

		if filepath.IsAbs(relativePath) {
			return "", fmt.Errorf("file path must be relative: %s", relativePath)
		}

		cleanRelativePath := filepath.Clean(relativePath)
		if cleanRelativePath == ".." || strings.HasPrefix(cleanRelativePath, ".."+string(os.PathSeparator)) {
			return "", fmt.Errorf("file path cannot escape root: %s", relativePath)
		}

		content, ok := fileMap["content"].(string)
		if !ok {
			return "", fmt.Errorf("file content is required")
		}

		fullPath := filepath.Join(root, cleanRelativePath)
		if err := writeProjectFile(ctx, fullPath, content, overwrite); err != nil {
			return "", err
		}

		createdFiles++
	}

	return fmt.Sprintf("create_project completed, wrote %d file(s) under %s", createdFiles, root), nil
}

func writeProjectFile(
	ctx context.Context,
	fullPath string,
	content string,
	overwrite bool,
) error {
	unlock := fileLocks.lock(fullPath)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(fullPath); err == nil && !overwrite {
		return fmt.Errorf("file already exists: %s", fullPath)
	} else if err != nil && !os.IsNotExist(err) {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(fullPath, []byte(content), 0644)
}
