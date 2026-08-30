package agent

import (
	"AgentCLI/internal/plan"
	"path/filepath"
	"strings"
)

// resourceTracker records the file resources held by running plan tasks.
// Multiple readers may share a resource, while a writer is exclusive.
type resourceTracker struct {
	readers map[string]int
	writers map[string]int
}

func newResourceTracker() *resourceTracker {
	return &resourceTracker{
		readers: make(map[string]int),
		writers: make(map[string]int),
	}
}

func (r *resourceTracker) conflicts(task *plan.Task) bool {
	reads, writes := taskResourceSets(task)
	for resource := range reads {
		if r.writers[resource] > 0 {
			return true
		}
	}
	for resource := range writes {
		if r.writers[resource] > 0 || r.readers[resource] > 0 {
			return true
		}
	}
	return false
}

func (r *resourceTracker) acquire(task *plan.Task) {
	reads, writes := taskResourceSets(task)
	for resource := range reads {
		r.readers[resource]++
	}
	for resource := range writes {
		r.writers[resource]++
	}
}

func (r *resourceTracker) release(task *plan.Task) {
	reads, writes := taskResourceSets(task)
	for resource := range reads {
		decrementResource(r.readers, resource)
	}
	for resource := range writes {
		decrementResource(r.writers, resource)
	}
}

func taskResourceSets(task *plan.Task) (map[string]struct{}, map[string]struct{}) {
	reads := canonicalResourceSet(task.ReadResources())
	writes := canonicalResourceSet(task.WriteResources())

	// A resource declared as both read and write is treated as write-only for
	// scheduling, avoiding duplicate acquisition while preserving exclusivity.
	for resource := range writes {
		delete(reads, resource)
	}
	return reads, writes
}

func canonicalResourceSet(resources []string) map[string]struct{} {
	result := make(map[string]struct{}, len(resources))
	for _, resource := range resources {
		resource = strings.TrimSpace(resource)
		if resource == "" {
			continue
		}

		resource = filepath.Clean(resource)
		if absolute, err := filepath.Abs(resource); err == nil {
			resource = absolute
		}
		result[resource] = struct{}{}
	}
	return result
}

func decrementResource(resources map[string]int, resource string) {
	if resources[resource] <= 1 {
		delete(resources, resource)
		return
	}
	resources[resource]--
}

func firstRunnableTask(
	ready []string,
	tasks map[string]*plan.Task,
	resources *resourceTracker,
) int {
	for index, taskID := range ready {
		if !resources.conflicts(tasks[taskID]) {
			return index
		}
	}
	return -1
}
