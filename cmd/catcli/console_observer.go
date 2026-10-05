package main

import (
	"AgentCLI/internal/agent"
	"AgentCLI/internal/memory"
	"fmt"
	"io"
	"strings"
)

// consoleObserver controls how much of an agent event appears in the CLI.
// The agent continues to emit the same structured events in both modes.
type consoleObserver struct {
	output io.Writer
	debug  bool
}

func newConsoleObserver(output io.Writer, debug bool) agent.Observer {
	observer := &consoleObserver{output: output, debug: debug}
	return agent.SynchronizedObserver(observer.observe)
}

func (observer *consoleObserver) observe(event agent.Event) {
	prefix := "[agent]"
	if event.TaskID != "" {
		prefix = "[" + event.TaskID + "]"
	}

	switch event.Type {
	case agent.EventTokenUsage:
		if observer.debug {
			fmt.Fprintf(observer.output, "%s 🧮 token: %s\n", prefix, event.Content)
		}
	case agent.EventMemoryCompaction:
		if observer.debug {
			fmt.Fprintf(observer.output, "%s 🗜️ memory compact %s: %s\n", prefix, event.Title, event.Content)
		} else {
			fmt.Fprintf(observer.output, "%s 🗜️ memory compact %s\n", prefix, event.Title)
		}
	case agent.EventMemoryFact:
		fmt.Fprintf(observer.output, "%s 🧠 memory fact %s: %s\n", prefix, event.Title, event.Content)
	case agent.EventMemoryRetrieval:
		if event.Retrieval == nil {
			return
		}
		report := event.Retrieval
		fmt.Fprintf(observer.output, "%s 🔎 memory retrieve facts=%d/%d context=%d/%d duration=%s\n",
			prefix, len(report.IncludedFacts), len(report.FactMatches),
			len(report.IncludedContext), len(report.ContextMatches), report.Duration)
		if observer.debug {
			selected := make(map[string]bool, len(report.IncludedFacts)+len(report.IncludedContext))
			for _, doc := range report.IncludedFacts {
				selected[doc.ID] = true
			}
			for _, doc := range report.IncludedContext {
				selected[doc.ID] = true
			}
			for _, doc := range append(append([]memory.MemoryDocument(nil), report.FactMatches...), report.ContextMatches...) {
				fmt.Fprintf(observer.output, "%s   id=%q kind=%s scope=%s included=%t text=%q\n",
					prefix, doc.ID, doc.Kind, doc.Scope, selected[doc.ID], doc.Text)
			}
		}
	case agent.EventToolCall:
		if observer.debug {
			fmt.Fprintf(observer.output, "%s 🔧 tool call %s: %s\n", prefix, event.Title, event.Content)
		} else {
			fmt.Fprintf(observer.output, "%s 🔧 tool call %s\n", prefix, event.Title)
		}
	case agent.EventToolResult:
		if observer.debug || strings.HasPrefix(strings.TrimSpace(event.Content), "ERROR:") {
			fmt.Fprintf(observer.output, "%s 📦 tool result %s:\n%s\n", prefix, event.Title, event.Content)
		} else {
			fmt.Fprintf(observer.output, "%s 📦 tool result %s\n", prefix, event.Title)
		}
	case agent.EventTaskStarted:
		fmt.Fprintf(observer.output, "\n%s ▶️ 开始执行：%s\n%s\n", prefix, event.Title, event.Content)
	case agent.EventTaskCompleted:
		fmt.Fprintf(observer.output, "\n%s ✅ 任务完成：%s\n%s\n", prefix, event.Title, event.Content)
	case agent.EventTaskFailed:
		fmt.Fprintf(observer.output, "\n%s ❌ 任务失败：%s\n%s\n", prefix, event.Title, event.Content)
	case agent.EventTaskCancelled:
		fmt.Fprintf(observer.output, "\n%s ⏹️ 任务已取消：%s\n%s\n", prefix, event.Title, event.Content)
	case agent.EventTaskTimeout:
		fmt.Fprintf(observer.output, "\n%s ⏱️ 任务执行超时：%s\n%s\n", prefix, event.Title, event.Content)
	case agent.EventPlanGenerated:
		fmt.Fprintf(observer.output, "\n[plan] 🗺️ %s\n", event.Title)
	case agent.EventPlanRevised:
		fmt.Fprintf(observer.output, "\n[plan] ✏️ %s\n", event.Title)
	case agent.EventPlanCancelled:
		fmt.Fprintf(observer.output, "\n[plan] ⏹️ %s\n", event.Title)
	case agent.EventPlanReplanning:
		fmt.Fprintf(observer.output, "\n[plan] 🔄 %s\n%s\n", event.Title, event.Content)
	case agent.EventPlanCompleted:
		fmt.Fprintf(observer.output, "\n[plan] ✅ %s\n%s\n", event.Title, event.Content)
	case agent.EventPlanFailed:
		fmt.Fprintf(observer.output, "\n[plan] ❌ %s\n%s\n", event.Title, event.Content)
	case agent.EventPlanTimeout:
		fmt.Fprintf(observer.output, "\n[plan] ⏱️ %s\n%s\n", event.Title, event.Content)
	}
}
