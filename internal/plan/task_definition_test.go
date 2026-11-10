package plan

import (
	"encoding/json"
	"testing"
)

func TestTaskDefinitionRoundTripKeepsProgressAndMutableSlicesSeparate(t *testing.T) {
	const recorded = `{"id":"edit","name":"Edit file","description":"update code","type":"FILE_WRITE","dependencies":["read"],"read_resources":["config.yaml"],"write_resources":["main.go"]}`
	var definition TaskDefinition
	if err := json.Unmarshal([]byte(recorded), &definition); err != nil {
		t.Fatal(err)
	}
	task := NewTaskFromDefinition(definition)
	definition.Dependencies[0] = "changed by caller"
	definition.ReadResources[0] = "other config"
	definition.WriteResources[0] = "other file"
	if task.ID() != "edit" || task.Type() != FILE_WRITE || task.Dependencies()[0] != "read" || task.ReadResources()[0] != "config.yaml" || task.WriteResources()[0] != "main.go" {
		t.Fatal("runtime task retained aliases into the decoded definition")
	}
	task.MarkCompleted("file updated")
	saved := task.Definition()
	task.SetDependencies([]string{"new dependency"})
	task.SetResources([]string{"new config"}, []string{"new file"})
	if saved.Dependencies[0] != "read" || saved.ReadResources[0] != "config.yaml" || saved.WriteResources[0] != "main.go" {
		t.Fatal("runtime updates changed the saved plan definition")
	}
	data, err := json.Marshal(saved)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"status", "result", "error", "start_time", "end_time"} {
		if _, exists := fields[field]; exists {
			t.Fatalf("runtime field %s leaked into the plan definition", field)
		}
	}
	var restored TaskDefinition
	if err := json.Unmarshal(data, &restored); err != nil {
		t.Fatal(err)
	}
	copy := NewTaskFromDefinition(restored)
	if copy.Status() != PENDING || copy.Result() != "" || copy.Dependencies()[0] != "read" || copy.WriteResources()[0] != "main.go" {
		t.Fatal("definition restoration mixed execution state with task data")
	}
}
