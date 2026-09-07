package memory

import (
	"AgentCLI/internal/llm"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Manager stores memory entries in insertion order and is safe for concurrent
// use. It does not evict automatically; callers choose when to remove entries
// or select a context that fits a token budget.
type Manager struct {
	mu           sync.RWMutex
	entryBuildMu sync.Mutex
	entries      []Entry
	entryIndexes map[string]int
	totalTokens  int
	facts        map[string]Entry
	factOrder    []string
	factTokens   int
	factStore    FactStore
	tokenCounter TokenCounter
	now          func() time.Time
	newID        func() (string, error)
}

// NewManagerWithFactStore creates a manager and restores persistent PROJECT
// and USER facts. SESSION facts intentionally remain process-local.
func NewManagerWithFactStore(tokenCounter TokenCounter, factStore FactStore) (*Manager, error) {
	manager := NewManager(tokenCounter)
	if factStore == nil {
		return manager, nil
	}

	storedFacts, err := factStore.Load()
	if err != nil {
		return nil, fmt.Errorf("load persistent facts: %w", err)
	}
	for _, stored := range storedFacts {
		entry, err := manager.buildFactEntry(stored.Scope, stored.Key, stored.Content, Metadata{})
		if err != nil {
			return nil, fmt.Errorf("restore %s fact %q: %w", stored.Scope, stored.Key, err)
		}
		if err := manager.AddEntry(entry); err != nil {
			return nil, fmt.Errorf("restore %s fact %q: %w", stored.Scope, stored.Key, err)
		}
	}
	manager.factStore = factStore
	return manager, nil
}

func NewManager(tokenCounter TokenCounter) *Manager {
	if tokenCounter == nil {
		tokenCounter = ApproxTokenCounter{}
	}

	return &Manager{
		entryIndexes: make(map[string]int),
		facts:        make(map[string]Entry),
		tokenCounter: tokenCounter,
		now:          time.Now,
		newID:        randomID,
	}
}

// UpsertFact creates or replaces a protected fact under a stable key. Facts
// are stored separately from the ordered, compressible conversation entries.
func (m *Manager) UpsertFact(
	scope FactScope,
	key string,
	content string,
	metadata Metadata,
) (Entry, error) {
	if m == nil {
		return Entry{}, fmt.Errorf("memory manager is nil")
	}
	entry, err := m.buildFactEntry(scope, key, content, metadata)
	if err != nil {
		return Entry{}, err
	}
	factID := entry.ID()

	m.mu.Lock()
	defer m.mu.Unlock()

	if _, exists := m.entryIndexes[factID]; exists {
		return Entry{}, fmt.Errorf("memory entry %q already exists", factID)
	}
	if scope != FactScopeSession && m.factStore != nil {
		if err := m.factStore.Upsert(StoredFact{Scope: scope, Key: key, Content: content}); err != nil {
			return Entry{}, fmt.Errorf("persist %s fact %q: %w", scope, key, err)
		}
	}
	if previous, exists := m.facts[factID]; exists {
		m.factTokens -= previous.TokenCount()
	} else {
		m.factOrder = append(m.factOrder, factID)
	}
	m.facts[factID] = entry
	m.factTokens += entry.TokenCount()
	return entry, nil
}

func (m *Manager) buildFactEntry(
	scope FactScope,
	key string,
	content string,
	metadata Metadata,
) (Entry, error) {
	if strings.TrimSpace(key) == "" {
		return Entry{}, fmt.Errorf("fact key is empty")
	}
	if !scope.Valid() {
		return Entry{}, fmt.Errorf("invalid fact scope %q", scope)
	}
	metadata.FactScope = scope
	metadata.FactKey = key

	m.entryBuildMu.Lock()
	timestamp := m.now().UTC()
	tokenCount := m.tokenCounter.Count(content)
	m.entryBuildMu.Unlock()

	return NewEntry(
		factStorageID(scope, key),
		content,
		Fact,
		timestamp,
		metadata,
		tokenCount,
	)
}

// Add creates and stores an entry. TokenCount is always derived from content.
func (m *Manager) Add(
	content string,
	typ Type,
	metadata Metadata,
) (Entry, error) {
	if typ == Fact {
		return Entry{}, fmt.Errorf("add fact: use UpsertFact with a stable key")
	}
	return m.add(content, typ, metadata, content)
}

// AddMessage stores a complete chat-protocol message. Assistant tool calls and
// tool call IDs are preserved so ContextMessages can reconstruct the message
// sequence without losing protocol information.
func (m *Manager) AddMessage(message llm.Message, toolName string) (Entry, error) {
	var typ Type
	switch message.Role {
	case "system", "user", "assistant":
		typ = Conversation
	case "tool":
		if strings.TrimSpace(message.ToolCallID) == "" {
			return Entry{}, fmt.Errorf("tool message has no tool call ID")
		}
		typ = ToolResult
	default:
		return Entry{}, fmt.Errorf("unsupported message role %q", message.Role)
	}

	encoded, err := json.Marshal(message)
	if err != nil {
		return Entry{}, fmt.Errorf("encode message for token counting: %w", err)
	}

	return m.add(
		message.Content,
		typ,
		Metadata{
			Role:       message.Role,
			ToolName:   toolName,
			ToolCallID: message.ToolCallID,
			ToolCalls:  message.ToolCalls,
		},
		string(encoded),
	)
}

func (m *Manager) add(
	content string,
	typ Type,
	metadata Metadata,
	tokenContent string,
) (Entry, error) {
	if m == nil {
		return Entry{}, fmt.Errorf("memory manager is nil")
	}
	if !typ.Valid() {
		return Entry{}, fmt.Errorf("invalid memory type %q", typ)
	}

	m.entryBuildMu.Lock()
	id, err := m.newID()
	if err != nil {
		m.entryBuildMu.Unlock()
		return Entry{}, fmt.Errorf("create memory entry ID: %w", err)
	}
	timestamp := m.now().UTC()
	tokenCount := m.tokenCounter.Count(tokenContent)
	m.entryBuildMu.Unlock()

	entry, err := NewEntry(
		id,
		content,
		typ,
		timestamp,
		metadata,
		tokenCount,
	)
	if err != nil {
		return Entry{}, err
	}

	if err := m.AddEntry(entry); err != nil {
		return Entry{}, err
	}
	return entry, nil
}

// ContextMessages returns all message-backed entries in insertion order.
// FACT and SUMMARY entries are excluded because they must be deliberately
// injected by a context-building policy rather than masquerading as dialogue.
func (m *Manager) ContextMessages() ([]llm.Message, error) {
	if m == nil {
		return nil, fmt.Errorf("memory manager is nil")
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	messages := make([]llm.Message, 0, len(m.entries)+1)
	if factMessage, ok := m.factMessageLocked(); ok {
		messages = append(messages, factMessage)
	}
	for _, entry := range m.entries {
		switch entry.Type() {
		case Fact:
			return nil, fmt.Errorf("fact entry %q is stored in conversation entries", entry.ID())
		case Summary:
			messages = append(messages, llm.SystemMessage(
				"Previous conversation summary:\n"+entry.Content(),
			))
		case Conversation, ToolResult:
			message, ok := entry.Message()
			if !ok {
				return nil, fmt.Errorf(
					"memory entry %q cannot be reconstructed as a message",
					entry.ID(),
				)
			}
			messages = append(messages, message)
		default:
			return nil, fmt.Errorf("memory entry %q has invalid type %q", entry.ID(), entry.Type())
		}
	}
	return messages, nil
}

// AddEntry stores a pre-built entry, which is useful when loading persisted
// memory. IDs must be unique.
func (m *Manager) AddEntry(entry Entry) error {
	if m == nil {
		return fmt.Errorf("memory manager is nil")
	}
	entry, err := normalizedEntry(entry)
	if err != nil {
		return err
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if entry.Type() == Fact {
		metadata := entry.Metadata()
		if !metadata.FactScope.Valid() {
			return fmt.Errorf("fact entry %q has invalid scope %q", entry.ID(), metadata.FactScope)
		}
		if strings.TrimSpace(metadata.FactKey) == "" {
			return fmt.Errorf("fact entry %q has an empty key", entry.ID())
		}
		expectedID := factStorageID(metadata.FactScope, metadata.FactKey)
		if entry.ID() != expectedID {
			return fmt.Errorf("fact entry ID %q does not match scope and key %q", entry.ID(), expectedID)
		}
		if _, exists := m.entryIndexes[entry.ID()]; exists {
			return fmt.Errorf("memory entry %q already exists", entry.ID())
		}
		if _, exists := m.facts[entry.ID()]; exists {
			return fmt.Errorf("memory entry %q already exists", entry.ID())
		}
		m.facts[entry.ID()] = entry
		m.factOrder = append(m.factOrder, entry.ID())
		m.factTokens += entry.TokenCount()
		return nil
	}

	if _, exists := m.entryIndexes[entry.ID()]; exists {
		return fmt.Errorf("memory entry %q already exists", entry.ID())
	}
	if _, exists := m.facts[entry.ID()]; exists {
		return fmt.Errorf("memory entry %q already exists", entry.ID())
	}

	m.entryIndexes[entry.ID()] = len(m.entries)
	m.entries = append(m.entries, entry)
	m.totalTokens += entry.TokenCount()
	return nil
}

func (m *Manager) Get(id string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	index, exists := m.entryIndexes[id]
	if exists {
		return m.entries[index], true
	}
	entry, exists := m.facts[id]
	return entry, exists
}

// Entries returns a snapshot in insertion order.
func (m *Manager) Entries() []Entry {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	return append([]Entry(nil), m.entries...)
}

// ConversationState returns the conversation-local state needed to resume
// this Manager. Persistent PROJECT and USER facts are intentionally excluded.
func (m *Manager) ConversationState() ConversationState {
	if m == nil {
		return ConversationState{}
	}

	m.entryBuildMu.Lock()
	updatedAt := m.now().UTC()
	m.entryBuildMu.Unlock()

	m.mu.RLock()
	defer m.mu.RUnlock()
	return ConversationState{
		Entries:      append([]Entry(nil), m.entries...),
		SessionFacts: m.factsLocked(FactScopeSession),
		UpdatedAt:    updatedAt,
	}
}

// RestoreConversationState atomically replaces active entries and SESSION
// facts while preserving PROJECT and USER facts already loaded by FactStore.
func (m *Manager) RestoreConversationState(state ConversationState) error {
	if m == nil {
		return fmt.Errorf("memory manager is nil")
	}

	entries := make([]Entry, 0, len(state.Entries))
	entryIndexes := make(map[string]int, len(state.Entries))
	entryTokens := 0
	allIDs := make(map[string]struct{}, len(state.Entries)+len(state.SessionFacts))
	for _, candidate := range state.Entries {
		entry, err := normalizedEntry(candidate)
		if err != nil {
			return fmt.Errorf("restore conversation entry: %w", err)
		}
		if entry.Type() == Fact {
			return fmt.Errorf("conversation entry %q is a fact", entry.ID())
		}
		if _, duplicate := allIDs[entry.ID()]; duplicate {
			return fmt.Errorf("duplicate conversation state entry %q", entry.ID())
		}
		allIDs[entry.ID()] = struct{}{}
		entryIndexes[entry.ID()] = len(entries)
		entries = append(entries, entry)
		entryTokens += entry.TokenCount()
	}

	sessionFacts := make([]Entry, 0, len(state.SessionFacts))
	for _, candidate := range state.SessionFacts {
		fact, err := normalizedEntry(candidate)
		if err != nil {
			return fmt.Errorf("restore session fact: %w", err)
		}
		if err := validateSessionFact(fact); err != nil {
			return err
		}
		if _, duplicate := allIDs[fact.ID()]; duplicate {
			return fmt.Errorf("duplicate conversation state entry %q", fact.ID())
		}
		allIDs[fact.ID()] = struct{}{}
		sessionFacts = append(sessionFacts, fact)
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	for id, fact := range m.facts {
		if fact.Metadata().FactScope == FactScopeSession {
			continue
		}
		if _, collision := allIDs[id]; collision {
			return fmt.Errorf("conversation state entry %q conflicts with a persistent fact", id)
		}
	}

	persistentFacts := make(map[string]Entry)
	persistentOrder := make([]string, 0, len(m.factOrder)+len(sessionFacts))
	persistentTokens := 0
	for _, id := range m.factOrder {
		fact, exists := m.facts[id]
		if !exists || fact.Metadata().FactScope == FactScopeSession {
			continue
		}
		persistentFacts[id] = fact
		persistentOrder = append(persistentOrder, id)
		persistentTokens += fact.TokenCount()
	}
	for _, fact := range sessionFacts {
		persistentFacts[fact.ID()] = fact
		persistentOrder = append(persistentOrder, fact.ID())
		persistentTokens += fact.TokenCount()
	}

	m.entries = entries
	m.entryIndexes = entryIndexes
	m.totalTokens = entryTokens
	m.facts = persistentFacts
	m.factOrder = persistentOrder
	m.factTokens = persistentTokens
	return nil
}

// SaveConversation writes a resumable checkpoint for this Manager.
func (m *Manager) SaveConversation(store ConversationStore, conversationID string) error {
	if m == nil {
		return fmt.Errorf("memory manager is nil")
	}
	if store == nil {
		return fmt.Errorf("conversation store is nil")
	}
	if err := store.Save(conversationID, m.ConversationState()); err != nil {
		return fmt.Errorf("save conversation %q: %w", conversationID, err)
	}
	return nil
}

// LoadConversation restores a checkpoint into this Manager. It returns false
// without changing the Manager when the conversation does not exist.
func (m *Manager) LoadConversation(store ConversationStore, conversationID string) (bool, error) {
	if m == nil {
		return false, fmt.Errorf("memory manager is nil")
	}
	if store == nil {
		return false, fmt.Errorf("conversation store is nil")
	}
	state, exists, err := store.Load(conversationID)
	if err != nil {
		return false, fmt.Errorf("load conversation %q: %w", conversationID, err)
	}
	if !exists {
		return false, nil
	}
	if err := m.RestoreConversationState(state); err != nil {
		return false, fmt.Errorf("restore conversation %q: %w", conversationID, err)
	}
	return true, nil
}

// EntriesByType returns a snapshot in insertion order.
func (m *Manager) EntriesByType(typ Type) []Entry {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if typ == Fact {
		return m.factsLocked()
	}

	entries := make([]Entry, 0)
	for _, entry := range m.entries {
		if entry.Type() == typ {
			entries = append(entries, entry)
		}
	}
	return entries
}

// Facts returns a snapshot in stable insertion order. With no scopes it returns
// all facts; otherwise it returns facts from the requested scopes.
func (m *Manager) Facts(scopes ...FactScope) []Entry {
	if m == nil {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.factsLocked(scopes...)
}

func (m *Manager) GetFact(scope FactScope, key string) (Entry, bool) {
	if m == nil {
		return Entry{}, false
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	entry, exists := m.facts[factStorageID(scope, key)]
	return entry, exists
}

func (m *Manager) RemoveFact(scope FactScope, key string) (bool, error) {
	if m == nil {
		return false, fmt.Errorf("memory manager is nil")
	}
	if !scope.Valid() {
		return false, fmt.Errorf("invalid fact scope %q", scope)
	}
	if strings.TrimSpace(key) == "" {
		return false, fmt.Errorf("fact key is empty")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	factID := factStorageID(scope, key)
	entry, exists := m.facts[factID]
	if !exists {
		return false, nil
	}
	if scope != FactScopeSession && m.factStore != nil {
		if err := m.factStore.Remove(scope, key); err != nil {
			return false, fmt.Errorf("remove persisted %s fact %q: %w", scope, key, err)
		}
	}

	delete(m.facts, factID)
	m.factTokens -= entry.TokenCount()
	for index, orderedKey := range m.factOrder {
		if orderedKey == factID {
			m.factOrder = append(m.factOrder[:index], m.factOrder[index+1:]...)
			break
		}
	}
	return true, nil
}

// RecentWithinTokenBudget selects the newest complete entries that fit and
// returns them in chronological order. This entry-level selector is intended
// for semantic memories; protocol context must be compacted in complete tool
// call groups so an assistant tool call is never separated from its result.
func (m *Manager) RecentWithinTokenBudget(maxTokens int) []Entry {
	if m == nil || maxTokens <= 0 {
		return nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	start := len(m.entries)
	used := 0
	for index := len(m.entries) - 1; index >= 0; index-- {
		entryTokens := m.entries[index].TokenCount()
		if used+entryTokens > maxTokens {
			break
		}
		used += entryTokens
		start = index
	}

	return append([]Entry(nil), m.entries[start:]...)
}

func (m *Manager) Remove(id string) bool {
	if m == nil {
		return false
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	index, exists := m.entryIndexes[id]
	if !exists {
		return false
	}

	m.totalTokens -= m.entries[index].TokenCount()
	m.entries = append(m.entries[:index], m.entries[index+1:]...)
	delete(m.entryIndexes, id)
	for i := index; i < len(m.entries); i++ {
		m.entryIndexes[m.entries[i].ID()] = i
	}
	return true
}

// ReplaceEntries atomically replaces one contiguous entry range with a single
// derived entry. It is used by compactors so readers never observe a partially
// compacted context.
func (m *Manager) ReplaceEntries(sourceIDs []string, replacement Entry) error {
	if m == nil {
		return fmt.Errorf("memory manager is nil")
	}
	if len(sourceIDs) == 0 {
		return fmt.Errorf("replacement source entries are empty")
	}
	replacement, err := normalizedEntry(replacement)
	if err != nil {
		return fmt.Errorf("invalid replacement entry: %w", err)
	}
	if replacement.Type() == Fact {
		return fmt.Errorf("replacement entry cannot be a fact")
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	start, exists := m.entryIndexes[sourceIDs[0]]
	if !exists {
		return fmt.Errorf("replacement source entry %q does not exist", sourceIDs[0])
	}
	if start+len(sourceIDs) > len(m.entries) {
		return fmt.Errorf("replacement source range exceeds active entries")
	}
	sourceTokens := 0
	for offset, sourceID := range sourceIDs {
		entry := m.entries[start+offset]
		if entry.ID() != sourceID {
			return fmt.Errorf("replacement source entries are not contiguous and ordered")
		}
		sourceTokens += entry.TokenCount()
	}
	if existing, duplicate := m.entryIndexes[replacement.ID()]; duplicate {
		if existing < start || existing >= start+len(sourceIDs) {
			return fmt.Errorf("memory entry %q already exists", replacement.ID())
		}
	}
	if _, duplicate := m.facts[replacement.ID()]; duplicate {
		return fmt.Errorf("memory entry %q already exists", replacement.ID())
	}

	updated := make([]Entry, 0, len(m.entries)-len(sourceIDs)+1)
	updated = append(updated, m.entries[:start]...)
	updated = append(updated, replacement)
	updated = append(updated, m.entries[start+len(sourceIDs):]...)
	m.entries = updated
	m.entryIndexes = make(map[string]int, len(updated))
	for index, entry := range updated {
		m.entryIndexes[entry.ID()] = index
	}
	m.totalTokens = m.totalTokens - sourceTokens + replacement.TokenCount()
	return nil
}

// Clear removes active conversation entries and session-scoped facts. Project
// and user facts survive because they represent persistent memory.
func (m *Manager) Clear() {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	m.entries = nil
	m.entryIndexes = make(map[string]int)
	m.totalTokens = 0
	m.removeFactsByScopeLocked(FactScopeSession)
}

// ClearAll removes active context and all in-memory facts. It deliberately
// leaves the configured FactStore unchanged, so durable memory is not erased
// by an ordinary context reset.
func (m *Manager) ClearAll() {
	if m == nil {
		return
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	m.entries = nil
	m.entryIndexes = make(map[string]int)
	m.totalTokens = 0
	m.facts = make(map[string]Entry)
	m.factOrder = nil
	m.factTokens = 0
}

func (m *Manager) Len() int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.entries)
}

func (m *Manager) FactLen(scopes ...FactScope) int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return len(m.factsLocked(scopes...))
}

// TotalTokens returns the combined token count of active entries and facts.
func (m *Manager) TotalTokens() int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalTokens + m.factTokens
}

func (m *Manager) EntryTokens() int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.totalTokens
}

func (m *Manager) FactTokens(scopes ...FactScope) int {
	if m == nil {
		return 0
	}

	m.mu.RLock()
	defer m.mu.RUnlock()
	if len(scopes) == 0 {
		return m.factTokens
	}
	total := 0
	for _, entry := range m.factsLocked(scopes...) {
		total += entry.TokenCount()
	}
	return total
}

func (m *Manager) factsLocked(scopes ...FactScope) []Entry {
	requested := make(map[FactScope]struct{}, len(scopes))
	for _, scope := range scopes {
		requested[scope] = struct{}{}
	}
	facts := make([]Entry, 0, len(m.factOrder))
	for _, key := range m.factOrder {
		if entry, exists := m.facts[key]; exists {
			if len(requested) > 0 {
				if _, included := requested[entry.Metadata().FactScope]; !included {
					continue
				}
			}
			facts = append(facts, entry)
		}
	}
	return facts
}

func (m *Manager) factMessageLocked() (llm.Message, bool) {
	if len(m.factOrder) == 0 {
		return llm.Message{}, false
	}

	var content strings.Builder
	content.WriteString("Important facts and constraints:")
	for _, scope := range []FactScope{FactScopeUser, FactScopeProject, FactScopeSession} {
		facts := m.factsLocked(scope)
		if len(facts) == 0 {
			continue
		}
		content.WriteString("\n[")
		content.WriteString(string(scope))
		content.WriteString("]")
		for _, entry := range facts {
			content.WriteString("\n- ")
			content.WriteString(entry.Content())
		}
	}
	return llm.SystemMessage(content.String()), true
}

func (m *Manager) removeFactsByScopeLocked(scope FactScope) {
	keptOrder := m.factOrder[:0]
	for _, factID := range m.factOrder {
		entry, exists := m.facts[factID]
		if !exists {
			continue
		}
		if entry.Metadata().FactScope == scope {
			delete(m.facts, factID)
			m.factTokens -= entry.TokenCount()
			continue
		}
		keptOrder = append(keptOrder, factID)
	}
	m.factOrder = keptOrder
}

func factStorageID(scope FactScope, key string) string {
	return string(scope) + ":" + key
}

func normalizedEntry(entry Entry) (Entry, error) {
	return NewEntry(
		entry.ID(),
		entry.Content(),
		entry.Type(),
		entry.Timestamp(),
		entry.Metadata(),
		entry.TokenCount(),
	)
}

func randomID() (string, error) {
	var value [16]byte
	if _, err := rand.Read(value[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(value[:]), nil
}
