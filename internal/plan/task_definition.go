package plan

// TaskDefinition describes the work independently of execution progress. Both
// runtime tasks and persisted plan events use this serializable definition.
type TaskDefinition struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Description    string   `json:"description"`
	Type           TaskType `json:"type"`
	Dependencies   []string `json:"dependencies,omitempty"`
	ReadResources  []string `json:"read_resources,omitempty"`
	WriteResources []string `json:"write_resources,omitempty"`
}

func cloneTaskDefinition(def TaskDefinition) TaskDefinition {
	def.Dependencies = append([]string(nil), def.Dependencies...)
	def.ReadResources = append([]string(nil), def.ReadResources...)
	def.WriteResources = append([]string(nil), def.WriteResources...)
	return def
}
