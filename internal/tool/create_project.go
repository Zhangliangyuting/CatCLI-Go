package tool

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func (p BuiltinProvider) verifyCreateProject(ctx context.Context, args map[string]any, _ string) (*Verification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, ok := args["root"].(string)
	if !ok || root == "" {
		return nil, fmt.Errorf("root is required")
	}
	root, err := p.workspace.Resolve(root)
	if err != nil {
		return nil, err
	}
	files, ok := args["files"].([]any)
	if !ok || len(files) == 0 {
		return nil, fmt.Errorf("files is required")
	}
	seen := make(map[string]bool)
	var evidence []string
	for _, raw := range files {
		file, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("invalid file item")
		}
		path, ok := file["path"].(string)
		if !ok || path == "" || filepath.IsAbs(path) {
			return nil, fmt.Errorf("file path must be nonempty and relative")
		}
		clean := filepath.Clean(path)
		if clean == ".." || strings.HasPrefix(clean, ".."+string(os.PathSeparator)) {
			return nil, fmt.Errorf("file path cannot escape root")
		}
		path, err = p.workspace.Resolve(filepath.Join(root, clean))
		if err != nil {
			return nil, err
		}
		// Reject ambiguous duplicate/symlink-alias targets rather than inventing a result.
		if seen[path] {
			return nil, nil
		}
		seen[path] = true
		content, ok := file["content"].(string)
		if !ok {
			return nil, fmt.Errorf("file content is required")
		}
		check, err := p.verifyWriteFile(ctx, map[string]any{"file_path": path, "content": content}, "")
		if err != nil || check == nil {
			return nil, err
		}
		evidence = append(evidence, check.Evidence)
	}
	return &Verification{
		Evidence: "create_project postcondition verified: all requested files match; " + strings.Join(evidence, "; "),
		Result:   fmt.Sprintf("create_project recovery verified: all %d requested file(s) match under %s; no files written", len(files), root),
	}, nil
}

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
