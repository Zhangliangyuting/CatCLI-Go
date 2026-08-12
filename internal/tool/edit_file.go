package tool

import (
	"fmt"
	"os"
	"strings"
)

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

func EditFileHandler(args map[string]interface{}) (string, error) {
	filePath, ok := args["file_path"].(string)
	if !ok || filePath == "" {
		return "", fmt.Errorf("file_path is required")
	}

	oldString, ok := args["old_string"].(string)
	if !ok || oldString == "" {
		return "", fmt.Errorf("old_string is required")
	}

	newString, ok := args["new_string"].(string)
	if !ok {
		return "", fmt.Errorf("new_string is required")
	}

	replaceAll := false
	if value, ok := args["replace_all"].(bool); ok {
		replaceAll = value
	}

	data, err := os.ReadFile(filePath)
	if err != nil {
		return "", err
	}

	content := string(data)

	count := strings.Count(content, oldString)
	if count == 0 {
		return "", fmt.Errorf("old_string not found in file")
	}
	if !replaceAll && count > 1 {
		return "", fmt.Errorf("old_string appears %d times in file, set replace_all=true or provide a more specific old_string", count)
	}

	var updated string
	if replaceAll {
		updated = strings.ReplaceAll(content, oldString, newString)
	} else {
		updated = strings.Replace(content, oldString, newString, 1)
	}

	if err := os.WriteFile(filePath, []byte(updated), 0644); err != nil {
		return "", err
	}

	return fmt.Sprintf("edit_file completed, replaced %d occurrence(s)", count), nil
}
