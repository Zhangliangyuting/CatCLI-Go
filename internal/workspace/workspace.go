package workspace

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const metadataDirectory = ".catcli"

// Workspace defines the project boundary used by tools, memory, and indexes.
type Workspace struct {
	root string
}

func New(root string) (*Workspace, error) {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil, fmt.Errorf("workspace root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve workspace root symlinks: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("stat workspace root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("workspace root is not a directory: %s", resolved)
	}
	return &Workspace{root: filepath.Clean(resolved)}, nil
}

func FromCurrentDirectory() (*Workspace, error) {
	root, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("get current directory: %w", err)
	}
	return New(root)
}

func (workspace *Workspace) Root() string {
	if workspace == nil {
		return ""
	}
	return workspace.root
}

func (workspace *Workspace) MetadataDir() string {
	if workspace == nil {
		return ""
	}
	return filepath.Join(workspace.root, metadataDirectory)
}

// Resolve converts path into an absolute workspace path and rejects lexical
// traversal and symlink traversal outside the workspace root.
func (workspace *Workspace) Resolve(path string) (string, error) {
	if workspace == nil || workspace.root == "" {
		return "", fmt.Errorf("workspace is not initialized")
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return "", fmt.Errorf("workspace path is empty")
	}

	candidate := path
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(workspace.root, candidate)
	}
	candidate = filepath.Clean(candidate)
	if !contains(workspace.root, candidate) {
		return "", fmt.Errorf("path escapes workspace: %s", path)
	}

	resolved, err := resolveExistingPrefix(candidate)
	if err != nil {
		return "", fmt.Errorf("resolve workspace path: %w", err)
	}
	if !contains(workspace.root, resolved) {
		return "", fmt.Errorf("path escapes workspace through symlink: %s", path)
	}
	return resolved, nil
}

func contains(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)) &&
		!filepath.IsAbs(relative)
}

func resolveExistingPrefix(path string) (string, error) {
	current := path
	var suffix []string
	for {
		_, err := os.Lstat(current)
		if err == nil {
			resolved, err := filepath.EvalSymlinks(current)
			if err != nil {
				return "", err
			}
			for index := len(suffix) - 1; index >= 0; index-- {
				resolved = filepath.Join(resolved, suffix[index])
			}
			return filepath.Clean(resolved), nil
		}
		if !os.IsNotExist(err) {
			return "", err
		}

		parent := filepath.Dir(current)
		if parent == current {
			return "", err
		}
		suffix = append(suffix, filepath.Base(current))
		current = parent
	}
}
