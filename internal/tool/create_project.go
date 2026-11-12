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
			Description: "根据文件列表创建项目。已有文件内容与目标相同时跳过并视为成功。内容不同时默认不覆盖；覆盖必须提供该文件最近读取的 expected_hash，哈希变化时先重新读取。",
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
								"expected_hash": map[string]interface{}{
									"type":        "string",
									"description": "覆盖已有文件时必填：该文件最近读取的 full_file_sha256；新建文件不填",
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
		expected, err := expectedFileHash(fileMap)
		if err != nil {
			return "", err
		}

		fullPath := filepath.Join(root, cleanRelativePath)
		if err := writeProjectFile(ctx, fullPath, content, overwrite, expected); err != nil {
			return "", err
		}

		createdFiles++
	}

	return fmt.Sprintf("create_project completed, ensured %d file(s) match requested content under %s", createdFiles, root), nil
}

func writeProjectFile(
	ctx context.Context,
	fullPath string,
	content string,
	overwrite bool,
	expected string,
) error {
	unlock := fileLocks.lock(fullPath)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	info, err := os.Stat(fullPath)
	switch {
	case err == nil:
		if !info.Mode().IsRegular() {
			return fmt.Errorf("expected a regular file: %s", fullPath)
		}
		actual, err := fileSHA256(ctx, fullPath)
		if err != nil {
			return err
		}
		// The requested state already exists, including after a partial prior run.
		// No overwrite permission or still-current pre-edit hash is needed to skip.
		if actual == contentHash([]byte(content)) {
			return nil
		}
		if !overwrite {
			return fmt.Errorf("file already exists with different content: %s", fullPath)
		}
		if expected == "" {
			return fmt.Errorf("expected_hash is required to overwrite an existing file; read %s first", fullPath)
		}
		if actual != expected {
			return fmt.Errorf("file changed since read: expected_hash does not match; read %s again", fullPath)
		}
	case os.IsNotExist(err):
		if expected != "" {
			return fmt.Errorf("file is missing: expected_hash requires an existing file; read %s again", fullPath)
		}
	case err != nil:
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	return atomicWriteFile(ctx, fullPath, []byte(content), info == nil)
}
