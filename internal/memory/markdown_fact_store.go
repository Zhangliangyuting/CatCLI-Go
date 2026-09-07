package memory

import (
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const factEndMarker = "<!-- /catcli-fact -->"

type MarkdownFactStore struct {
	mu          sync.Mutex
	projectPath string
	userPath    string
}

func NewMarkdownFactStore(projectPath, userPath string) (*MarkdownFactStore, error) {
	if strings.TrimSpace(projectPath) == "" {
		return nil, fmt.Errorf("project memory path is empty")
	}
	if strings.TrimSpace(userPath) == "" {
		return nil, fmt.Errorf("user memory path is empty")
	}
	return &MarkdownFactStore{
		projectPath: projectPath,
		userPath:    userPath,
	}, nil
}

func (s *MarkdownFactStore) Load() ([]StoredFact, error) {
	if s == nil {
		return nil, fmt.Errorf("markdown fact store is nil")
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	var facts []StoredFact
	for _, item := range []struct {
		scope FactScope
		path  string
	}{
		{scope: FactScopeProject, path: s.projectPath},
		{scope: FactScopeUser, path: s.userPath},
	} {
		loaded, err := loadMarkdownFacts(item.path, item.scope)
		if err != nil {
			return nil, err
		}
		facts = append(facts, loaded...)
	}
	return facts, nil
}

func (s *MarkdownFactStore) Upsert(fact StoredFact) error {
	if s == nil {
		return fmt.Errorf("markdown fact store is nil")
	}
	if err := validateStoredFact(fact); err != nil {
		return err
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathForScope(fact.Scope)
	if err != nil {
		return err
	}
	facts, err := loadMarkdownFacts(path, fact.Scope)
	if err != nil {
		return err
	}
	updated := false
	for index := range facts {
		if facts[index].Key == fact.Key {
			facts[index] = fact
			updated = true
			break
		}
	}
	if !updated {
		facts = append(facts, fact)
	}
	return writeMarkdownFacts(path, fact.Scope, facts)
}

func (s *MarkdownFactStore) Remove(scope FactScope, key string) error {
	if s == nil {
		return fmt.Errorf("markdown fact store is nil")
	}
	if strings.TrimSpace(key) == "" {
		return fmt.Errorf("fact key is empty")
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	path, err := s.pathForScope(scope)
	if err != nil {
		return err
	}
	facts, err := loadMarkdownFacts(path, scope)
	if err != nil {
		return err
	}
	kept := facts[:0]
	for _, fact := range facts {
		if fact.Key != key {
			kept = append(kept, fact)
		}
	}
	return writeMarkdownFacts(path, scope, kept)
}

func (s *MarkdownFactStore) pathForScope(scope FactScope) (string, error) {
	switch scope {
	case FactScopeProject:
		return s.projectPath, nil
	case FactScopeUser:
		return s.userPath, nil
	case FactScopeSession:
		return "", fmt.Errorf("session facts cannot be persisted")
	default:
		return "", fmt.Errorf("invalid fact scope %q", scope)
	}
}

func loadMarkdownFacts(path string, scope FactScope) ([]StoredFact, error) {
	data, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s facts: %w", scope, err)
	}

	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	var facts []StoredFact
	for index := 0; index < len(lines); index++ {
		key, ok, err := parseFactStartMarker(lines[index])
		if err != nil {
			return nil, fmt.Errorf("parse %s facts: %w", scope, err)
		}
		if !ok {
			continue
		}

		start := index + 1
		end := start
		for end < len(lines) && strings.TrimSpace(lines[end]) != factEndMarker {
			end++
		}
		if end == len(lines) {
			return nil, fmt.Errorf("parse %s facts: fact %q has no end marker", scope, key)
		}
		content := strings.TrimSpace(strings.Join(lines[start:end], "\n"))
		fact := StoredFact{Scope: scope, Key: key, Content: content}
		if err := validateStoredFact(fact); err != nil {
			return nil, fmt.Errorf("parse %s facts: %w", scope, err)
		}
		facts = append(facts, fact)
		index = end
	}
	return facts, nil
}

func writeMarkdownFacts(path string, scope FactScope, facts []StoredFact) error {
	var output strings.Builder
	fmt.Fprintf(&output, "# %s Memory\n\n", titleScope(scope))
	output.WriteString("This file is managed by CatCLI. Fact text may be edited, but keep the surrounding markers intact.\n")
	for _, fact := range facts {
		if err := validateStoredFact(fact); err != nil {
			return err
		}
		output.WriteString("\n")
		output.WriteString(factStartMarker(fact.Key))
		output.WriteString("\n")
		output.WriteString(strings.TrimSpace(fact.Content))
		output.WriteString("\n")
		output.WriteString(factEndMarker)
		output.WriteString("\n")
	}

	directory := filepath.Dir(path)
	if err := os.MkdirAll(directory, 0o700); err != nil {
		return fmt.Errorf("create fact store directory: %w", err)
	}
	temporary, err := os.CreateTemp(directory, ".catcli-memory-*.tmp")
	if err != nil {
		return fmt.Errorf("create temporary fact file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer os.Remove(temporaryPath)
	if err := temporary.Chmod(0o600); err != nil {
		temporary.Close()
		return fmt.Errorf("set fact file permissions: %w", err)
	}
	if _, err := temporary.WriteString(output.String()); err != nil {
		temporary.Close()
		return fmt.Errorf("write temporary fact file: %w", err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close temporary fact file: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace fact file: %w", err)
	}
	return nil
}

func validateStoredFact(fact StoredFact) error {
	if fact.Scope != FactScopeProject && fact.Scope != FactScopeUser {
		return fmt.Errorf("fact scope %q is not persistent", fact.Scope)
	}
	if strings.TrimSpace(fact.Key) == "" {
		return fmt.Errorf("fact key is empty")
	}
	if strings.TrimSpace(fact.Content) == "" {
		return fmt.Errorf("fact %q content is empty", fact.Key)
	}
	return nil
}

func factStartMarker(key string) string {
	encoded := base64.RawURLEncoding.EncodeToString([]byte(key))
	return "<!-- catcli-fact:" + encoded + " -->"
}

func parseFactStartMarker(line string) (string, bool, error) {
	line = strings.TrimSpace(line)
	const prefix = "<!-- catcli-fact:"
	const suffix = " -->"
	if !strings.HasPrefix(line, prefix) || !strings.HasSuffix(line, suffix) {
		return "", false, nil
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(line, prefix), suffix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil {
		return "", false, fmt.Errorf("invalid fact key marker: %w", err)
	}
	return string(decoded), true, nil
}

func titleScope(scope FactScope) string {
	switch scope {
	case FactScopeProject:
		return "Project"
	case FactScopeUser:
		return "User"
	default:
		return string(scope)
	}
}
