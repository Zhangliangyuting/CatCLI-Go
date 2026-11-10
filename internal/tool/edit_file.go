package tool

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// Only hashes and identities are persisted, never an extra copy of file contents.
type editVerificationData struct {
	Path, ArgumentsHash, BeforeHash, AfterHash string
	Replacements                               int
}

func (p BuiltinProvider) prepareEditVerification(ctx context.Context, args map[string]any) (string, error) {
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return "", fmt.Errorf("file_path is required")
	}
	path, err := p.workspace.Resolve(path)
	if err != nil {
		return "", err
	}
	old, replacement, all, err := editArguments(args)
	if err != nil {
		return "", err
	}
	unlock := fileLocks.rlock(path)
	defer unlock()
	data, err := readEditVerificationFile(ctx, path)
	if err != nil {
		return "", err
	}
	updated, count, err := applyFileEdit(string(data), old, replacement, all)
	if err != nil {
		return "", err
	}
	bound := cloneArguments(args)
	bound["file_path"] = path
	encoded, err := json.Marshal(bound)
	if err != nil {
		return "", err
	}
	proof, err := json.Marshal(editVerificationData{Path: path, ArgumentsHash: contentHash(encoded), BeforeHash: contentHash(data), AfterHash: contentHash([]byte(updated)), Replacements: count})
	return string(proof), err
}

func (p BuiltinProvider) verifyEditFile(ctx context.Context, args map[string]any, saved string) (*Verification, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if saved == "" {
		return nil, nil
	} // Old logs cannot prove an edit's full postcondition.
	var proof editVerificationData
	if err := json.Unmarshal([]byte(saved), &proof); err != nil {
		return nil, err
	}
	path, ok := args["file_path"].(string)
	if !ok || path == "" {
		return nil, fmt.Errorf("file_path is required")
	}
	path, err := p.workspace.Resolve(path)
	if err != nil {
		return nil, err
	}
	if _, _, _, err := editArguments(args); err != nil {
		return nil, err
	}
	bound := cloneArguments(args)
	bound["file_path"] = path
	encoded, err := json.Marshal(bound)
	if err != nil {
		return nil, err
	}
	if proof.Path != path || proof.ArgumentsHash != contentHash(encoded) || proof.BeforeHash == "" || proof.AfterHash == "" || proof.Replacements <= 0 {
		return nil, nil
	}
	unlock := fileLocks.rlock(path)
	defer unlock()
	data, err := readEditVerificationFile(ctx, path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if contentHash(data) != proof.AfterHash {
		return nil, nil
	}
	return &Verification{
		Evidence: fmt.Sprintf("edit_file postcondition verified: path=%q before_sha256=%s after_sha256=%s; current full file matches prepared edit; historical execution is not established", path, proof.BeforeHash, proof.AfterHash),
		Result:   fmt.Sprintf("edit_file recovery verified: current full file matches expected edit (%d replacement(s)); no edit performed", proof.Replacements),
	}, nil
}

func contentHash(data []byte) string { return fmt.Sprintf("%x", sha256.Sum256(data)) }

func readEditVerificationFile(ctx context.Context, path string) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	const limit = 16 << 20
	if !info.Mode().IsRegular() || info.Size() > limit {
		return nil, fmt.Errorf("edit verification requires a regular file up to %d bytes", limit)
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if len(data) > limit {
		return nil, fmt.Errorf("file exceeds edit verification limit")
	}
	return data, nil
}

func editArguments(args map[string]any) (string, string, bool, error) {
	old, ok := args["old_string"].(string)
	if !ok || old == "" {
		return "", "", false, fmt.Errorf("old_string is required")
	}
	replacement, ok := args["new_string"].(string)
	if !ok {
		return "", "", false, fmt.Errorf("new_string is required")
	}
	all, _ := args["replace_all"].(bool)
	return old, replacement, all, nil
}

func applyFileEdit(content, old, replacement string, all bool) (string, int, error) {
	count := strings.Count(content, old)
	if count == 0 {
		return "", 0, fmt.Errorf("old_string not found in file")
	}
	if !all && count > 1 {
		return "", 0, fmt.Errorf("old_string appears %d times in file, set replace_all=true or provide a more specific old_string", count)
	}
	if all {
		return strings.ReplaceAll(content, old, replacement), count, nil
	}
	return strings.Replace(content, old, replacement, 1), 1, nil
}

func EditFileDefinition() Definition {
	return Definition{
		Type: "function",
		FunctionDefinition: FunctionDefinition{
			Name:        "edit_file",
			Description: "对文件进行精确字符串替换。默认要求 old_string 在文件中只出现一次；如果 replace_all 为 true，则替换所有匹配项。",
			Parameters: map[string]interface{}{
				"type": "object",
				"properties": map[string]interface{}{
					"file_path": map[string]interface{}{
						"type":        "string",
						"description": "要编辑的文件路径",
					},
					"old_string": map[string]interface{}{
						"type":        "string",
						"description": "要被替换的原始字符串，必须与文件内容精确匹配",
					},
					"new_string": map[string]interface{}{
						"type":        "string",
						"description": "替换后的新字符串",
					},
					"replace_all": map[string]interface{}{
						"type":        "boolean",
						"description": "是否替换所有匹配项。false 时要求 old_string 只出现一次",
					},
				},
				"required": []string{"file_path", "old_string", "new_string"},
			},
		},
	}
}

func EditFileHandler(
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

	oldString, newString, replaceAll, err := editArguments(args)
	if err != nil {
		return "", err
	}

	// The lock covers the complete read-modify-write transaction. Locking only
	// os.WriteFile would still allow two editors to calculate from stale data.
	unlock := fileLocks.lock(filePath)
	defer unlock()

	if err := ctx.Err(); err != nil {
		return "", err
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	updated, count, err := applyFileEdit(string(data), oldString, newString, replaceAll)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("edit_file completed, replaced %d occurrence(s)", count), nil
}
