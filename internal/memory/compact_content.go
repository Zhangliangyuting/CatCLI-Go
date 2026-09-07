package memory

import (
	"fmt"
	"strings"
)

// ToolResultCompactContent is the structured output of MicroCompact. It is
// rendered into the Content of a TOOL_RESULT entry, not a SUMMARY entry.
type ToolResultCompactContent struct {
	Overview      string   `json:"overview"`
	KeyFindings   []string `json:"key_findings"`
	Errors        []string `json:"errors"`
	References    []string `json:"references"`
	OmittedReason string   `json:"omitted_reason"`
}

func (content ToolResultCompactContent) Validate() error {
	if strings.TrimSpace(content.Overview) == "" {
		return fmt.Errorf("tool result compact overview is empty")
	}
	if len(content.KeyFindings) == 0 && len(content.Errors) == 0 {
		return fmt.Errorf("tool result compact has neither findings nor errors")
	}
	if strings.TrimSpace(content.OmittedReason) == "" {
		return fmt.Errorf("tool result compact omitted reason is empty")
	}
	if err := validateNonEmptyStrings("tool result key findings", content.KeyFindings); err != nil {
		return err
	}
	if err := validateNonEmptyStrings("tool result errors", content.Errors); err != nil {
		return err
	}
	return validateNonEmptyStrings("tool result references", content.References)
}

func (content ToolResultCompactContent) RenderMarkdown() (string, error) {
	if err := content.Validate(); err != nil {
		return "", err
	}
	var output strings.Builder
	appendParagraphSection(&output, "Overview", content.Overview)
	appendListSection(&output, "Key Findings", content.KeyFindings)
	appendListSection(&output, "Errors", content.Errors)
	appendListSection(&output, "References", content.References)
	appendParagraphSection(&output, "Omitted", content.OmittedReason)
	return strings.TrimSpace(output.String()), nil
}

// SummaryContent is shared by Session and Full compaction. The compaction kind
// and source range live in Metadata.Compaction rather than in this content.
type SummaryContent struct {
	Goal             string             `json:"goal"`
	UserRequirements []string           `json:"user_requirements"`
	Decisions        []SummaryDecision  `json:"decisions"`
	Completed        []SummaryCompleted `json:"completed"`
	CurrentState     []string           `json:"current_state"`
	Pending          []string           `json:"pending"`
	ImportantFiles   []SummaryFile      `json:"important_files"`
	Errors           []SummaryError     `json:"errors"`
	FactReferences   []string           `json:"fact_references"`
	Continuation     string             `json:"continuation"`
}

type SummaryDecision struct {
	Topic  string `json:"topic"`
	Choice string `json:"choice"`
	Reason string `json:"reason,omitempty"`
}

type SummaryCompleted struct {
	Action string `json:"action"`
	Result string `json:"result"`
}

type SummaryFile struct {
	Path   string `json:"path"`
	Status string `json:"status"`
	Notes  string `json:"notes,omitempty"`
}

type SummaryError struct {
	Message    string `json:"message"`
	Resolution string `json:"resolution,omitempty"`
}

func (content SummaryContent) Validate() error {
	if strings.TrimSpace(content.Goal) == "" {
		return fmt.Errorf("summary goal is empty")
	}
	if len(content.CurrentState) == 0 {
		return fmt.Errorf("summary current state is empty")
	}
	if strings.TrimSpace(content.Continuation) == "" {
		return fmt.Errorf("summary continuation is empty")
	}
	for name, values := range map[string][]string{
		"user requirements": content.UserRequirements,
		"current state":     content.CurrentState,
		"pending items":     content.Pending,
		"fact references":   content.FactReferences,
	} {
		if err := validateNonEmptyStrings("summary "+name, values); err != nil {
			return err
		}
	}
	for index, decision := range content.Decisions {
		if strings.TrimSpace(decision.Topic) == "" || strings.TrimSpace(decision.Choice) == "" {
			return fmt.Errorf("summary decision %d requires topic and choice", index)
		}
	}
	for index, completed := range content.Completed {
		if strings.TrimSpace(completed.Action) == "" || strings.TrimSpace(completed.Result) == "" {
			return fmt.Errorf("summary completed item %d requires action and result", index)
		}
	}
	for index, file := range content.ImportantFiles {
		if strings.TrimSpace(file.Path) == "" || strings.TrimSpace(file.Status) == "" {
			return fmt.Errorf("summary file %d requires path and status", index)
		}
	}
	for index, summaryError := range content.Errors {
		if strings.TrimSpace(summaryError.Message) == "" {
			return fmt.Errorf("summary error %d has an empty message", index)
		}
	}
	return nil
}

func (content SummaryContent) RenderMarkdown() (string, error) {
	if err := content.Validate(); err != nil {
		return "", err
	}
	var output strings.Builder
	appendParagraphSection(&output, "Goal", content.Goal)
	appendListSection(&output, "User Requirements", content.UserRequirements)

	appendHeading(&output, "Decisions")
	if len(content.Decisions) == 0 {
		output.WriteString("- None.\n\n")
	} else {
		for _, decision := range content.Decisions {
			fmt.Fprintf(&output, "- Topic: %s\n  Choice: %s\n", cleanText(decision.Topic), cleanText(decision.Choice))
			if strings.TrimSpace(decision.Reason) != "" {
				fmt.Fprintf(&output, "  Reason: %s\n", cleanText(decision.Reason))
			}
		}
		output.WriteString("\n")
	}

	appendHeading(&output, "Completed")
	if len(content.Completed) == 0 {
		output.WriteString("- None.\n\n")
	} else {
		for _, completed := range content.Completed {
			fmt.Fprintf(&output, "- Action: %s\n  Result: %s\n", cleanText(completed.Action), cleanText(completed.Result))
		}
		output.WriteString("\n")
	}

	appendListSection(&output, "Current State", content.CurrentState)
	appendListSection(&output, "Pending", content.Pending)

	appendHeading(&output, "Important Files")
	if len(content.ImportantFiles) == 0 {
		output.WriteString("- None.\n\n")
	} else {
		for _, file := range content.ImportantFiles {
			fmt.Fprintf(&output, "- Path: %s\n  Status: %s\n", cleanText(file.Path), cleanText(file.Status))
			if strings.TrimSpace(file.Notes) != "" {
				fmt.Fprintf(&output, "  Notes: %s\n", cleanText(file.Notes))
			}
		}
		output.WriteString("\n")
	}

	appendHeading(&output, "Errors")
	if len(content.Errors) == 0 {
		output.WriteString("- None.\n\n")
	} else {
		for _, summaryError := range content.Errors {
			fmt.Fprintf(&output, "- Message: %s\n", cleanText(summaryError.Message))
			if strings.TrimSpace(summaryError.Resolution) != "" {
				fmt.Fprintf(&output, "  Resolution: %s\n", cleanText(summaryError.Resolution))
			}
		}
		output.WriteString("\n")
	}

	appendListSection(&output, "Fact References", content.FactReferences)
	appendParagraphSection(&output, "Continuation", content.Continuation)
	return strings.TrimSpace(output.String()), nil
}

func validateNonEmptyStrings(name string, values []string) error {
	for index, value := range values {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("%s item %d is empty", name, index)
		}
	}
	return nil
}

func appendHeading(output *strings.Builder, heading string) {
	fmt.Fprintf(output, "## %s\n\n", heading)
}

func appendParagraphSection(output *strings.Builder, heading, value string) {
	appendHeading(output, heading)
	output.WriteString(strings.TrimSpace(value))
	output.WriteString("\n\n")
}

func appendListSection(output *strings.Builder, heading string, values []string) {
	appendHeading(output, heading)
	if len(values) == 0 {
		output.WriteString("- None.\n\n")
		return
	}
	for _, value := range values {
		fmt.Fprintf(output, "- %s\n", cleanText(value))
	}
	output.WriteString("\n")
}

func cleanText(value string) string {
	return strings.Join(strings.Fields(value), " ")
}
